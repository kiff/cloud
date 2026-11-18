package meter

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// KeepRun classifies r and keeps it, its run's first report: a run kept
// already stays as it was, and is returned with fresh false. A run of
// Setup's is measured against what Setup's runs that ended in [from, to)
// cost (the period r ended in), under a lock on the account, so its fair
// use is exact however many reports arrive at once. Which of Setup's runs
// crosses it still depends on the order their reports arrive.
func (p *Postgres) KeepRun(ctx context.Context, r RunReport, from, to time.Time) (RunEntry, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return RunEntry{}, false, fmt.Errorf("meter: keep run %s: %w", r.Run, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var spent int64
	if r.Setup {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('billable_runs:setup:' || $1))`, r.Tenant); err != nil {
			return RunEntry{}, false, fmt.Errorf("meter: keep run %s: %w", r.Run, err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(cost_micro_usd), 0)::bigint FROM cloud_meta.billable_runs
			 WHERE tenant_id = $1 AND setup AND ended_at >= $2 AND ended_at < $3`,
			r.Tenant, from.UTC(), to.UTC()).Scan(&spent); err != nil {
			return RunEntry{}, false, fmt.Errorf("meter: setup's spend: %w", err)
		}
	}
	e := ClassifyRun(r, spent)
	tag, err := tx.Exec(ctx, `
		INSERT INTO cloud_meta.billable_runs (tenant_id, run_id, agent_id, runtime_agent, setup, trigger, source,
		       requester_kind, asked_by, started_at, ended_at, cost_micro_usd, turns, status, stop, failed,
		       refused, declined, kept_until, counted, free, credits, no_plan)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
		ON CONFLICT (tenant_id, run_id) DO NOTHING`,
		e.Tenant, e.Run, e.Agent, e.RuntimeAgent, e.Setup, e.Trigger, e.Source, e.RequesterKind, e.AskedBy,
		e.Started.UTC(), e.Ended.UTC(), e.CostMicroUSD, e.Turns, e.Status, e.Stop, e.Failed,
		e.Refused, e.Declined, utcOrNil(e.KeptUntil), e.Counted, e.Free, e.Credits, e.NoPlan)
	if err != nil {
		return RunEntry{}, false, fmt.Errorf("meter: keep run %s: %w", e.Run, err)
	}
	fresh := tag.RowsAffected() == 1
	if !fresh {
		if e, err = scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM cloud_meta.billable_runs
			 WHERE tenant_id = $1 AND run_id = $2`, r.Tenant, r.Run), r.Tenant); err != nil {
			return RunEntry{}, false, fmt.Errorf("meter: run %s: %w", r.Run, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return RunEntry{}, false, fmt.Errorf("meter: keep run %s: %w", r.Run, err)
	}
	return e, fresh, nil
}

func utcOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

const runColumns = `run_id, agent_id, runtime_agent, setup, trigger, source, requester_kind, asked_by,
	started_at, ended_at, cost_micro_usd, turns, status, stop, failed, refused, declined, kept_until,
	counted, free, credits`

func scanRun(row pgx.Row, tenantID string) (RunEntry, error) {
	e := RunEntry{RunReport: RunReport{Tenant: tenantID}}
	var kept *time.Time
	if err := row.Scan(&e.Run, &e.Agent, &e.RuntimeAgent, &e.Setup, &e.Trigger, &e.Source, &e.RequesterKind,
		&e.AskedBy, &e.Started, &e.Ended, &e.CostMicroUSD, &e.Turns, &e.Status, &e.Stop, &e.Failed,
		&e.Refused, &e.Declined, &kept, &e.Counted, &e.Free, &e.Credits); err != nil {
		return RunEntry{}, err
	}
	e.Started, e.Ended = e.Started.UTC(), e.Ended.UTC()
	if kept != nil {
		k := kept.UTC()
		e.KeptUntil = &k
	}
	return e, nil
}

// RunTotals are tenantID's runs that ended in [from, to).
func (p *Postgres) RunTotals(ctx context.Context, tenantID string, from, to time.Time) (RunTotals, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT agent_id, runtime_agent, setup, counted, free, count(*), COALESCE(SUM(credits), 0)::bigint,
		       COALESCE(SUM(over_credits), 0)::bigint
		  FROM cloud_meta.billable_runs
		 WHERE tenant_id = $1 AND ended_at >= $2 AND ended_at < $3
		 GROUP BY agent_id, runtime_agent, setup, counted, free`, tenantID, from.UTC(), to.UTC())
	if err != nil {
		return RunTotals{}, fmt.Errorf("meter: run totals: %w", err)
	}
	defer rows.Close()
	t := RunTotals{ByAgent: []AgentRuns{}, Free: map[string]int{}}
	for _, k := range RunFreeKinds {
		t.Free[k] = 0
	}
	byAgent := map[[2]string]*AgentRuns{}
	for rows.Next() {
		var (
			agent, runtime, free string
			setup, counted       bool
			n                    int
			credits, over        int64
		)
		if err := rows.Scan(&agent, &runtime, &setup, &counted, &free, &n, &credits, &over); err != nil {
			return RunTotals{}, fmt.Errorf("meter: run totals: %w", err)
		}
		t.Over += over
		if !counted {
			t.Free[free] += n
			continue
		}
		t.Credits, t.Runs = t.Credits+credits, t.Runs+n
		if setup {
			t.Setup.Credits, t.Setup.Runs = t.Setup.Credits+credits, t.Setup.Runs+n
			continue
		}
		k := [2]string{agent, runtime}
		if byAgent[k] == nil {
			byAgent[k] = &AgentRuns{Agent: agent, RuntimeAgent: runtime}
		}
		byAgent[k].Credits, byAgent[k].Runs = byAgent[k].Credits+credits, byAgent[k].Runs+n
	}
	if err := rows.Err(); err != nil {
		return RunTotals{}, err
	}
	for _, a := range byAgent {
		t.ByAgent = append(t.ByAgent, *a)
	}
	sort.Slice(t.ByAgent, func(i, j int) bool {
		a, b := t.ByAgent[i], t.ByAgent[j]
		if a.Credits != b.Credits {
			return a.Credits > b.Credits
		}
		return a.RuntimeAgent+"\x00"+a.Agent < b.RuntimeAgent+"\x00"+b.Agent
	})
	return t, nil
}

// Runs lists tenantID's runs that ended in [from, to), newest first, up to
// limit, after page when it's set; only those of runtimeAgent when it isn't
// "", and only scope's when it's set: the subject's own, and the runs of
// the agents they manage. next is set when there are more.
func (p *Postgres) Runs(ctx context.Context, tenantID, runtimeAgent string, scope *TaskScope, from, to time.Time, page *RunPage, limit int) ([]RunEntry, *RunPage, error) {
	afterAt, afterRun := to.UTC(), ""
	if page != nil {
		afterAt, afterRun = page.At.UTC(), page.Run
	}
	scoped, subject, agents := scope != nil, "", []string{}
	if scope != nil {
		subject, agents = scope.Subject, scope.Agents
	}
	rows, err := p.pool.Query(ctx, `SELECT `+runColumns+` FROM cloud_meta.billable_runs
		 WHERE tenant_id = $1 AND ended_at >= $2 AND ended_at < $3
		   AND ($4 = '' OR (ended_at, run_id) < ($5, $4))
		   AND ($7 = '' OR runtime_agent = $7)
		   AND (NOT $8 OR (asked_by <> '' AND asked_by = $9) OR agent_id = ANY($10::text[]))
		 ORDER BY ended_at DESC, run_id DESC
		 LIMIT $6`, tenantID, from.UTC(), to.UTC(), afterRun, afterAt, limit+1, runtimeAgent, scoped, subject, agents)
	if err != nil {
		return nil, nil, fmt.Errorf("meter: runs: %w", err)
	}
	defer rows.Close()
	out := []RunEntry{}
	for rows.Next() {
		e, err := scanRun(rows, tenantID)
		if err != nil {
			return nil, nil, fmt.Errorf("meter: runs: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(out) > limit {
		last := out[limit-1]
		return out[:limit], &RunPage{At: last.Ended, Run: last.Run}, nil
	}
	return out, nil, nil
}

// RunDays are the runs that ended in [from, to), by account, agent and UTC
// day; only tenantID's when it isn't "".
func (p *Postgres) RunDays(ctx context.Context, tenantID string, from, to time.Time) ([]RunDay, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT tenant_id, agent_id, runtime_agent, setup, date_trunc('day', ended_at AT TIME ZONE 'UTC'),
		       count(*), count(*) FILTER (WHERE counted), COALESCE(SUM(credits), 0)::bigint,
		       COALESCE(SUM(over_credits), 0)::bigint, COALESCE(SUM(cost_micro_usd), 0)::bigint, max(ended_at)
		  FROM cloud_meta.billable_runs
		 WHERE ($1 = '' OR tenant_id = $1) AND ended_at >= $2 AND ended_at < $3
		 GROUP BY 1, 2, 3, 4, 5
		 ORDER BY 5, 1, 2, 3, 4`, tenantID, from.UTC(), to.UTC())
	if err != nil {
		return nil, fmt.Errorf("meter: run days: %w", err)
	}
	defer rows.Close()
	out := []RunDay{}
	for rows.Next() {
		var d RunDay
		if err := rows.Scan(&d.Tenant, &d.Agent, &d.RuntimeAgent, &d.Setup, &d.Day, &d.Runs, &d.Counted,
			&d.Credits, &d.Over, &d.CostMicroUSD, &d.Last); err != nil {
			return nil, fmt.Errorf("meter: run days: %w", err)
		}
		d.Day = time.Date(d.Day.Year(), d.Day.Month(), d.Day.Day(), 0, 0, 0, 0, time.UTC)
		d.Last = d.Last.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}
