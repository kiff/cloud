package meter

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
	"github.com/kiff/cloud/api/internal/tenant"
)

//go:embed sql/schema.sql
var schemaSQL string

// ApplySchema creates cloud_meta.billable_calls. Idempotent. Run after
// cloud_meta.tenants exists.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "meter/schema", schemaSQL); err != nil {
		return fmt.Errorf("apply meter schema: %w", err)
	}
	return nil
}

// Postgres is the meter on the shared database: it reads the gateway's
// call record and keeps cloud_meta.billable_calls.
type Postgres struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewPostgres keeps the meter through pool; the caller applies the schema.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool, now: time.Now}
}

// lookback is how far back Sync looks for calls that ended. A call is
// classified within a minute of ending; a week covers an API that was
// down, or a gateway row that ended late.
const lookback = 7 * 24 * time.Hour

// syncBatch bounds one Sync; the next one takes the rest.
const syncBatch = 2000

// Sync classifies the calls that ended since the last Sync, once each,
// and returns how many it recorded. Where the gateway's tables don't
// exist (an API without a gateway), there is nothing to do.
func (p *Postgres) Sync(ctx context.Context) (int, error) {
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass('gateway.invocations') IS NOT NULL AND to_regclass('gateway.tools') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return 0, err
	}
	// isError is compared as JSON, never cast, as tooloutcome reads it.
	rows, err := p.pool.Query(ctx, `
		SELECT i.tenant_id, i.key, i.agent_id, i.tool, i.state, i.message, i.tool_error, i.tool_outcome, i.updated_at,
		       i.result IS NOT NULL, COALESCE(i.result->'isError' NOT IN ('false'::jsonb, 'null'::jsonb), false),
		       i.exception_id, i.reasons,
		       COALESCE(t.read_only, false), COALESCE(t.title, ''),
		       COALESCE(a.kind, '') = 'taf',
		       EXISTS (SELECT 1 FROM cloud_meta.api_keys k WHERE k.id = i.credential_id AND k.roles ? 'setup_reader'),
		       COALESCE(q.asked_by, '')
		  FROM gateway.invocations i
		  LEFT JOIN cloud_meta.call_requesters q ON q.tenant_id = i.tenant_id AND q.key = i.key
		  LEFT JOIN gateway.tools t ON t.tenant_id = i.tenant_id AND t.name = i.tool
		  LEFT JOIN cloud_meta.agents a ON a.tenant_id = i.tenant_id AND a.id = i.agent_id
		 WHERE i.state IN ('refused', 'forwarded', 'unknown', 'failed')
		   AND i.updated_at > $1
		   AND EXISTS (SELECT 1 FROM cloud_meta.tenants tn WHERE tn.id = i.tenant_id)
		   AND NOT EXISTS (SELECT 1 FROM cloud_meta.billable_calls b WHERE b.tenant_id = i.tenant_id AND b.key = i.key)
		 ORDER BY i.updated_at
		 LIMIT $2`, p.now().Add(-lookback), syncBatch)
	if err != nil {
		return 0, fmt.Errorf("meter: read calls: %w", err)
	}
	var entries []Entry
	for rows.Next() {
		var c Call
		if err := rows.Scan(&c.Tenant, &c.Key, &c.Agent, &c.Tool, &c.Record.State, &c.Record.Message,
			&c.Record.ToolError, &c.Record.ToolOutcome, &c.Record.At, &c.Record.HasResult, &c.Record.ResultError,
			&c.ExceptionID, &c.Reasons, &c.ReadOnly, &c.ToolTitle, &c.Taf, &c.SetupReader, &c.AskedBy); err != nil {
			rows.Close()
			return 0, fmt.Errorf("meter: read calls: %w", err)
		}
		if e, ok := Classify(c); ok {
			entries = append(entries, e)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("meter: read calls: %w", err)
	}
	if len(entries) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, e := range entries {
		b.Queue(`INSERT INTO cloud_meta.billable_calls (tenant_id, key, agent_id, tool, tool_title, unit, counted, free, at, asked_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING`,
			e.Tenant, e.Key, e.Agent, e.Tool, e.ToolTitle, string(e.Unit), e.Counted, e.Free, e.At.UTC(), e.AskedBy)
	}
	if err := p.pool.SendBatch(ctx, b).Close(); err != nil {
		return 0, fmt.Errorf("meter: record calls: %w", err)
	}
	return len(entries), nil
}

// Totals is one meter over a period.
type Totals struct {
	Counted int
	// Free counts each free case; every one of FreeKinds is present.
	Free map[string]int
	// ByAgent counts what each agent's calls counted.
	ByAgent map[string]int
	// FreeByAgent counts each agent's free cases, by the same keys as Free;
	// an agent's map has every one of FreeKinds. Summed over the agents it
	// is Free.
	FreeByAgent map[string]map[string]int
}

func newTotals() Totals {
	t := Totals{Free: freeKinds(), ByAgent: map[string]int{}, FreeByAgent: map[string]map[string]int{}}
	return t
}

// freeKinds is a free-cases map with every one of FreeKinds at 0.
func freeKinds() map[string]int {
	m := make(map[string]int, len(FreeKinds))
	for _, k := range FreeKinds {
		m[k] = 0
	}
	return m
}

// freeSetupRead is how Setup's calls were stored while it could only read;
// they are read as FreeSetup, so the usage read keeps one line for them
// (kiff/tafs#206). The rows aren't rewritten.
const freeSetupRead = "setup_read"

// addFree counts n of the free case kind for agent.
func (t Totals) addFree(agent, kind string, n int) {
	if kind == freeSetupRead {
		kind = FreeSetup
	}
	t.Free[kind] += n
	if t.FreeByAgent[agent] == nil {
		t.FreeByAgent[agent] = freeKinds()
	}
	t.FreeByAgent[agent][kind] += n
}

// Totals reads both meters for tenantID over [from, to): the calls that
// ended then, and the trial calls decided then.
func (p *Postgres) Totals(ctx context.Context, tenantID string, from, to time.Time) (map[Unit]Totals, error) {
	out := map[Unit]Totals{UnitTask: newTotals(), UnitAction: newTotals()}
	rows, err := p.pool.Query(ctx, `
		SELECT unit, counted, free, agent_id, count(*)
		  FROM cloud_meta.billable_calls
		 WHERE tenant_id = $1 AND at >= $2 AND at < $3
		 GROUP BY unit, counted, free, agent_id`, tenantID, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("meter: totals: %w", err)
	}
	for rows.Next() {
		var unit, free, agent string
		var counted bool
		var n int
		if err := rows.Scan(&unit, &counted, &free, &agent, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("meter: totals: %w", err)
		}
		t, ok := out[Unit(unit)]
		if !ok {
			continue
		}
		if counted {
			t.Counted += n
			t.ByAgent[agent] += n
		} else {
			t.addFree(agent, free, n)
		}
		out[Unit(unit)] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meter: totals: %w", err)
	}
	trials, err := p.trials(ctx, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	for u, byAgent := range trials {
		for agent, n := range byAgent {
			out[u].addFree(agent, FreeTrial, n)
		}
	}
	return out, nil
}

// trials counts the trial calls decided in [from, to), once per call, by
// the meter of the agent that made them and by agent. A trial decides and
// sends nothing, so it is in the account's decisions only.
func (p *Postgres) trials(ctx context.Context, tenantID string, from, to time.Time) (map[Unit]map[string]int, error) {
	schema := tenant.SchemaNameFor(tenantID)
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".kiff_audit").Scan(&exists); err != nil {
		return nil, fmt.Errorf("meter: trials: %w", err)
	}
	out := map[Unit]map[string]int{UnitTask: {}, UnitAction: {}}
	if !exists {
		return out, nil
	}
	rows, err := p.pool.Query(ctx, `
		SELECT COALESCE(a.kind, '') = 'taf', COALESCE(d.actor_id, ''), count(DISTINCT COALESCE(NULLIF(d.data->>'proposal_id', ''), d.trace_id, d.id::text))
		  FROM `+pgx.Identifier{schema, "kiff_audit"}.Sanitize()+` d
		  LEFT JOIN cloud_meta.agents a ON a.tenant_id = $1 AND a.id = d.actor_id
		 WHERE d.kind IN ('proposal_decision', 'proposal_rejected')
		   AND COALESCE(d.data->>'trial_draft', '') <> ''
		   AND d.created_at >= $2 AND d.created_at < $3
		 GROUP BY 1, 2`, tenantID, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("meter: trials: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var taf bool
		var agent string
		var n int
		if err := rows.Scan(&taf, &agent, &n); err != nil {
			return nil, fmt.Errorf("meter: trials: %w", err)
		}
		if taf {
			out[UnitTask][agent] += n
		} else {
			out[UnitAction][agent] += n
		}
	}
	return out, rows.Err()
}

// AskedBy records that subject's run made the call key, as KIFF checked
// their run grant when it decided it: the call's person, for usage by
// person. Once per call; a second write changes nothing.
func (p *Postgres) AskedBy(ctx context.Context, tenantID, key, subject string) error {
	if tenantID == "" || key == "" || subject == "" {
		return nil
	}
	if _, err := p.pool.Exec(ctx, `INSERT INTO cloud_meta.call_requesters (tenant_id, key, asked_by, at)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, tenantID, key, subject, p.now().UTC()); err != nil {
		return fmt.Errorf("meter: record who asked: %w", err)
	}
	return nil
}

// PersonCount is what one person's runs counted on each meter in a
// period. Subject "" is everything no person's run made: schedules, doors
// and the account's own agents.
type PersonCount struct {
	Subject string
	Tasks   int
	Actions int
}

// ByPerson counts, for tenantID between from and to, what each person's
// runs counted on each meter, the most first. A cost and fair-use figure:
// counts of what got done, never a view of how people work.
func (p *Postgres) ByPerson(ctx context.Context, tenantID string, from, to time.Time) ([]PersonCount, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT asked_by,
		       count(*) FILTER (WHERE unit = 'task'),
		       count(*) FILTER (WHERE unit = 'action')
		  FROM cloud_meta.billable_calls
		 WHERE tenant_id = $1 AND counted AND at >= $2 AND at < $3
		 GROUP BY asked_by`, tenantID, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("meter: by person: %w", err)
	}
	defer rows.Close()
	var out []PersonCount
	for rows.Next() {
		var c PersonCount
		if err := rows.Scan(&c.Subject, &c.Tasks, &c.Actions); err != nil {
			return nil, fmt.Errorf("meter: by person: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("meter: by person: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Tasks+out[i].Actions, out[j].Tasks+out[j].Actions; a != b {
			return a > b
		}
		return out[i].Subject < out[j].Subject
	})
	return out, nil
}

// Counted reads how many calls counted on unit for tenantID since from:
// what the limits compare against.
func (p *Postgres) Counted(ctx context.Context, tenantID string, unit Unit, from time.Time) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `
		SELECT count(*) FROM cloud_meta.billable_calls
		 WHERE tenant_id = $1 AND unit = $2 AND counted AND at >= $3`, tenantID, string(unit), from.UTC()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("meter: counted: %w", err)
	}
	return n, nil
}

// Task is one counted tafs task, for the app's list.
type Task struct {
	Record string `json:"record"`
	Agent  string `json:"agent"`
	// AgentName is what people read the agent by, set by the usage read
	// from the agent's record; "" without one.
	AgentName string `json:"agent_name,omitempty"`
	Tool      string `json:"tool"`
	// ToolTitle is the tool's title for people, kept with the call when
	// it was recorded; "" when it had none.
	ToolTitle string    `json:"tool_title"`
	At        time.Time `json:"at"`
}

// TaskScope keeps the tasks one person reads: those
// their own runs made, and every task of the agents they manage. A nil
// scope reads the account's, as an admin does.
type TaskScope struct {
	Subject string
	Agents  []string
}

// KeysAskedBy is the calls subject's runs made in tenantID, by call key
// (proposal id): what of the account's record is theirs. Recorded from each call's verified run grant (AskedBy).
func (p *Postgres) KeysAskedBy(ctx context.Context, tenantID, subject string) (map[string]bool, error) {
	out := map[string]bool{}
	if tenantID == "" || subject == "" {
		return out, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT key FROM cloud_meta.call_requesters WHERE tenant_id = $1 AND asked_by = $2`, tenantID, subject)
	if err != nil {
		return nil, fmt.Errorf("meter: calls asked by: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("meter: calls asked by: %w", err)
		}
		out[k] = true
	}
	return out, rows.Err()
}

// TaskPage is where a Tasks listing goes on: after the task at At with
// call key Key, newest first.
type TaskPage struct {
	At  time.Time
	Key string
}

// Tasks lists tenantID's counted tasks in [from, to), newest first, up to
// limit, after page when it's set; only agent's when agent isn't "". next
// is set when there are more.
func (p *Postgres) Tasks(ctx context.Context, tenantID, agent string, scope *TaskScope, from, to time.Time, page *TaskPage, limit int) ([]Task, *TaskPage, error) {
	afterAt, afterKey := to.UTC(), ""
	if page != nil {
		afterAt, afterKey = page.At.UTC(), page.Key
	}
	scoped, subject, agents := scope != nil, "", []string{}
	if scope != nil {
		subject, agents = scope.Subject, scope.Agents
	}
	rows, err := p.pool.Query(ctx, `
		SELECT key, agent_id, tool, tool_title, at FROM cloud_meta.billable_calls
		 WHERE tenant_id = $1 AND unit = 'task' AND counted AND at >= $2 AND at < $3
		   AND ($4 = '' OR (at, key) < ($5, $4))
		   AND ($7 = '' OR agent_id = $7)
		   AND (NOT $8 OR (asked_by <> '' AND asked_by = $9) OR agent_id = ANY($10::text[]))
		 ORDER BY at DESC, key DESC
		 LIMIT $6`, tenantID, from.UTC(), to.UTC(), afterKey, afterAt, limit+1, agent, scoped, subject, agents)
	if err != nil {
		return nil, nil, fmt.Errorf("meter: tasks: %w", err)
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.Record, &t.Agent, &t.Tool, &t.ToolTitle, &t.At); err != nil {
			return nil, nil, fmt.Errorf("meter: tasks: %w", err)
		}
		t.At = t.At.UTC()
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(out) > limit {
		last := out[limit-1]
		return out[:limit], &TaskPage{At: last.At, Key: last.Record}, nil
	}
	return out, nil, nil
}

// Tafs lists tenantID's tafs that aren't archived: the agents the tafs app
// made, whose calls are tasks.
func (p *Postgres) Tafs(ctx context.Context, tenantID string) ([]string, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id FROM cloud_meta.agents
		 WHERE tenant_id = $1 AND kind = 'taf' AND archived_at IS NULL
		 ORDER BY id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("meter: tafs: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("meter: tafs: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// IsAgent reports whether id is one of tenantID's agents, archived or not.
func (p *Postgres) IsAgent(ctx context.Context, tenantID, id string) (bool, error) {
	var ok bool
	err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM cloud_meta.agents WHERE tenant_id = $1 AND id = $2)`, tenantID, id).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("meter: is agent: %w", err)
	}
	return ok, nil
}

// Run syncs m every interval until ctx ends, so a call is on its meter
// within a minute of ending. A failed sync is logged and tried again.
func Run(ctx context.Context, m *Postgres, every time.Duration, logger *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := m.Sync(ctx); err != nil {
			logger.WarnContext(ctx, "meter: sync failed", slog.String("err", err.Error()))
		} else if n > 0 {
			logger.InfoContext(ctx, "meter: calls recorded", slog.Int("calls", n))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pending lists the accounts with counted calls not yet settled with
// Stripe: reported as overage, or found within what's included.
func (p *Postgres) Pending(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT tenant_id FROM cloud_meta.billable_calls WHERE counted AND reported_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("meter: pending: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("meter: pending: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Placed is a counted call not yet settled, with its place among the
// period's counted calls on its meter: 1 is the period's first.
type Placed struct {
	Key   string
	At    time.Time
	Place int
}

// Unsettled lists tenantID's counted calls on unit in [from, to) not yet
// settled, in order, each with its place in the period.
func (p *Postgres) Unsettled(ctx context.Context, tenantID string, unit Unit, from, to time.Time) ([]Placed, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT key, at, place FROM (
			SELECT key, at, reported_at, row_number() OVER (ORDER BY at, key) AS place
			  FROM cloud_meta.billable_calls
			 WHERE tenant_id = $1 AND unit = $2 AND counted AND at >= $3 AND at < $4) x
		 WHERE reported_at IS NULL
		 ORDER BY place`, tenantID, string(unit), from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("meter: unsettled: %w", err)
	}
	defer rows.Close()
	var out []Placed
	for rows.Next() {
		var c Placed
		if err := rows.Scan(&c.Key, &c.At, &c.Place); err != nil {
			return nil, fmt.Errorf("meter: unsettled: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Settle marks tenantID's calls keys settled with Stripe.
func (p *Postgres) Settle(ctx context.Context, tenantID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := p.pool.Exec(ctx, `UPDATE cloud_meta.billable_calls SET reported_at = now()
		WHERE tenant_id = $1 AND key = ANY($2) AND reported_at IS NULL`, tenantID, keys)
	if err != nil {
		return fmt.Errorf("meter: settle: %w", err)
	}
	return nil
}

// SettleBefore marks every counted call of tenantID that ended before t
// settled, with nothing reported: a plan that isn't billed, or a period
// long gone.
func (p *Postgres) SettleBefore(ctx context.Context, tenantID string, t time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE cloud_meta.billable_calls SET reported_at = now()
		WHERE tenant_id = $1 AND counted AND reported_at IS NULL AND at < $2`, tenantID, t.UTC())
	if err != nil {
		return fmt.Errorf("meter: settle: %w", err)
	}
	return nil
}
