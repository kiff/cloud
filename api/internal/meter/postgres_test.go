package meter_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/testdb"
)

// The gateway's record, as much of it as the meter reads, and the
// cloud_meta tables it joins.
const fixtureSchema = `
CREATE SCHEMA IF NOT EXISTS gateway;
CREATE TABLE IF NOT EXISTS gateway.invocations (
    key text PRIMARY KEY, tenant_id text NOT NULL, credential_id text NOT NULL DEFAULT '', agent_id text NOT NULL,
    tool text NOT NULL, state text NOT NULL, exception_id text NOT NULL DEFAULT '', message text NOT NULL DEFAULT '',
    result jsonb, updated_at timestamptz NOT NULL, reasons text[] NOT NULL DEFAULT '{}',
    tool_error text NOT NULL DEFAULT '', tool_outcome text NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS gateway.tools (tenant_id text NOT NULL, name text NOT NULL, read_only boolean NOT NULL DEFAULT false,
    PRIMARY KEY (tenant_id, name));
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS title text NOT NULL DEFAULT '';
CREATE SCHEMA IF NOT EXISTS cloud_meta;
CREATE TABLE IF NOT EXISTS cloud_meta.tenants (id text PRIMARY KEY);
CREATE TABLE IF NOT EXISTS cloud_meta.agents (tenant_id text NOT NULL, id text NOT NULL, kind text NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, id));
ALTER TABLE cloud_meta.agents ADD COLUMN IF NOT EXISTS archived_at timestamptz;
CREATE TABLE IF NOT EXISTS cloud_meta.api_keys (id text PRIMARY KEY, roles jsonb NOT NULL DEFAULT '[]');
CREATE SCHEMA IF NOT EXISTS kiff_t_t1;
CREATE TABLE IF NOT EXISTS kiff_t_t1.kiff_audit (id bigserial PRIMARY KEY, kind text NOT NULL, actor_id text,
    data jsonb NOT NULL DEFAULT '{}', trace_id text, created_at timestamptz NOT NULL);
`

// The meter reads the gateway's record once a call has ended: counted
// only when done and not a read, once per call, on the meter of the agent
// that made it, and every free case named.
// fixture is the package's database with the fixture schema and the
// meter's own.
func fixture(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), fixtureSchema); err != nil {
		t.Fatal(err)
	}
	if err := meter.ApplySchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestMeterSyncsTotalsAndLists(t *testing.T) {
	ctx := context.Background()
	pool := fixture(t)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
		INSERT INTO cloud_meta.tenants VALUES ('t1'), ('t2');
		INSERT INTO cloud_meta.agents VALUES ('t1', 'taf-refunds', 'taf'), ('t1', 'setup', 'taf'), ('t1', 'billing-bot', '');
		INSERT INTO cloud_meta.api_keys VALUES ('key-setup', '["setup_reader"]'), ('key-taf', '[]');
		INSERT INTO gateway.tools (tenant_id, name, read_only, title) VALUES ('t1', 'refund', false, 'Refund a payment'), ('t1', 'list_charges', true, ''), ('t1', 'docs', true, ''), ('t2', 'refund', false, '');`); err != nil {
		t.Fatal(err)
	}
	call := func(key, tenant, agent, cred, tool, state, outcome, exception string, reasons []string, at time.Time) {
		t.Helper()
		if reasons == nil {
			reasons = []string{}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO gateway.invocations
			(key, tenant_id, credential_id, agent_id, tool, state, tool_outcome, exception_id, reasons, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, key, tenant, cred, agent, tool, state, outcome, exception, reasons, at); err != nil {
			t.Fatal(err)
		}
	}
	call("c1", "t1", "taf-refunds", "key-taf", "refund", "forwarded", "done", "", nil, at)
	call("c2", "t1", "taf-refunds", "key-taf", "refund", "forwarded", "done", "exc_1", nil, at.Add(time.Minute)) // held, approved, done
	call("c3", "t1", "taf-refunds", "key-taf", "refund", "forwarded", "error", "", nil, at)
	call("c4", "t1", "taf-refunds", "key-taf", "refund", "unknown", "", "", nil, at)
	call("c5", "t1", "taf-refunds", "key-taf", "list_charges", "forwarded", "done", "", nil, at)
	call("c6", "t1", "setup", "key-setup", "docs", "forwarded", "done", "", nil, at)
	call("c7", "t1", "taf-refunds", "key-taf", "refund", "refused", "", "", []string{"halted"}, at)
	call("c8", "t1", "taf-refunds", "key-taf", "refund", "refused", "", "exc_2", []string{"mandate_exceeded"}, at)
	call("c9", "t1", "billing-bot", "key-bot", "refund", "forwarded", "done", "", nil, at)
	call("c10", "t1", "taf-refunds", "key-taf", "refund", "held", "", "exc_3", nil, at) // not ended
	call("c11", "t2", "other", "key-x", "refund", "forwarded", "done", "", nil, at)
	call("c12", "t9", "gone", "key-x", "refund", "forwarded", "done", "", nil, at) // no such account
	if _, err := pool.Exec(ctx, `INSERT INTO kiff_t_t1.kiff_audit (kind, actor_id, data, created_at) VALUES
		('proposal_decision', 'taf-refunds', '{"trial_draft": "d1", "proposal_id": "p1"}', $1),
		('proposal_decision', 'taf-refunds', '{"trial_draft": "d1", "proposal_id": "p1"}', $1),
		('proposal_decision', 'taf-refunds', '{"proposal_id": "c1"}', $1)`, at); err != nil {
		t.Fatal(err)
	}

	m := meter.NewPostgres(pool)
	n, err := m.Sync(ctx)
	if err != nil || n != 10 {
		t.Fatalf("sync: %d %v; want the 10 ended calls of existing accounts", n, err)
	}
	// Once each: a second Sync records nothing new.
	if n, err := m.Sync(ctx); err != nil || n != 0 {
		t.Fatalf("sync again: %d %v", n, err)
	}

	from, to := at.Add(-time.Hour), at.Add(time.Hour)
	tot, err := m.Totals(ctx, "t1", from, to)
	if err != nil {
		t.Fatal(err)
	}
	tasks, actions := tot[meter.UnitTask], tot[meter.UnitAction]
	if tasks.Counted != 2 || tasks.ByAgent["taf-refunds"] != 2 || actions.Counted != 1 || actions.ByAgent["billing-bot"] != 1 {
		t.Fatalf("counted: tasks %+v, actions %+v", tasks, actions)
	}
	want := map[string]int{"failed": 2, "read": 1, "setup": 1, "halted": 1, "held_refused": 1, "trial": 1, "refused": 0, "invalid": 0, "retry": 0}
	for k, v := range want {
		if tasks.Free[k] != v {
			t.Errorf("tasks free %s = %d, want %d (%v)", k, tasks.Free[k], v, tasks.Free)
		}
	}
	if len(tasks.Free) != len(meter.FreeKinds) || len(actions.Free) != len(meter.FreeKinds) {
		t.Errorf("a free case is missing: %v %v", tasks.Free, actions.Free)
	}
	if n, err := m.Counted(ctx, "t1", meter.UnitTask, from); err != nil || n != 2 {
		t.Errorf("counted tasks: %d %v", n, err)
	}
	if n, err := m.Counted(ctx, "t2", meter.UnitAction, from); err != nil || n != 1 {
		t.Errorf("another account's: %d %v", n, err)
	}

	// The counted tasks, newest first, a page at a time, each by its
	// Record id.
	// Each taf's own free cases, trials included; summed,
	// they're the meter's.
	if f := tasks.FreeByAgent["taf-refunds"]; f["failed"] != 2 || f["halted"] != 1 || f["held_refused"] != 1 || f["trial"] != 1 || f["read"] != 1 || len(f) != len(meter.FreeKinds) {
		t.Errorf("taf-refunds' free cases: %v", f)
	}
	if f := tasks.FreeByAgent["setup"]; f["setup"] != 1 {
		t.Errorf("setup's free cases: %v", f)
	}
	sum := map[string]int{}
	for _, f := range tasks.FreeByAgent {
		for k, v := range f {
			sum[k] += v
		}
	}
	for k, v := range tasks.Free {
		if sum[k] != v {
			t.Errorf("free %s: the agents add up to %d, the meter says %d", k, sum[k], v)
		}
	}
	// The account's tafs that aren't archived, and which agents are its.
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.agents (tenant_id, id, kind, archived_at) VALUES ('t1', 'taf-old', 'taf', now())`); err != nil {
		t.Fatal(err)
	}
	if tafs, err := m.Tafs(ctx, "t1"); err != nil || len(tafs) != 2 || tafs[0] != "setup" || tafs[1] != "taf-refunds" {
		t.Errorf("tafs: %v %v", tafs, err)
	}
	for id, want := range map[string]bool{"billing-bot": true, "taf-old": true, "other": false} {
		if ok, err := m.IsAgent(ctx, "t1", id); err != nil || ok != want {
			t.Errorf("is %s t1's agent: %v %v", id, ok, err)
		}
	}
	// One taf's counted tasks; an own agent has none (its calls are actions).
	if mine, _, err := m.Tasks(ctx, "t1", "taf-refunds", nil, from, to, nil, 10); err != nil || len(mine) != 2 {
		t.Errorf("taf-refunds' tasks: %+v %v", mine, err)
	}
	if none, _, err := m.Tasks(ctx, "t1", "billing-bot", nil, from, to, nil, 10); err != nil || len(none) != 0 {
		t.Errorf("an own agent's tasks: %+v %v", none, err)
	}

	page, next, err := m.Tasks(ctx, "t1", "", nil, from, to, nil, 1)
	if err != nil || len(page) != 1 || page[0].Record != "c2" || next == nil {
		t.Fatalf("first page: %+v %+v %v", page, next, err)
	}
	page, next, err = m.Tasks(ctx, "t1", "", nil, from, to, next, 1)
	if err != nil || len(page) != 1 || page[0].Record != "c1" || page[0].Agent != "taf-refunds" || page[0].Tool != "refund" || next != nil {
		t.Fatalf("second page: %+v %+v %v", page, next, err)
	}
	// Each keeps its tool's title from when it was recorded,
	// even once the tool is disconnected and its gateway row is gone.
	if page[0].ToolTitle != "Refund a payment" {
		t.Fatalf("the tool's title: %q", page[0].ToolTitle)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM gateway.tools WHERE tenant_id = 't1' AND name = 'refund'`); err != nil {
		t.Fatal(err)
	}
	if kept, _, err := m.Tasks(ctx, "t1", "", nil, from, to, nil, 10); err != nil || len(kept) != 2 || kept[0].ToolTitle != "Refund a payment" || kept[1].ToolTitle != "Refund a payment" {
		t.Fatalf("after the tool is disconnected: %+v %v", kept, err)
	}

	// The held call, once it ends, is classified then.
	if _, err := pool.Exec(ctx, `UPDATE gateway.invocations SET state = 'refused', updated_at = now() WHERE key = 'c10'`); err != nil {
		t.Fatal(err)
	}
	if n, err := m.Sync(ctx); err != nil || n != 1 {
		t.Fatalf("sync after the hold ended: %d %v", n, err)
	}
}

// Setup's calls stored as 'setup_read', while it could only read, are read
// as 'setup' (kiff/tafs#206), so the usage read keeps one line for them; the
// rows aren't rewritten, and other free cases stay as they were.
func TestOldSetupReadRowsReadAsSetup(t *testing.T) {
	ctx := context.Background()
	pool := fixture(t)
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants VALUES ('t-old-setup') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO cloud_meta.billable_calls (tenant_id, key, agent_id, tool, unit, counted, free, at) VALUES
		  ('t-old-setup', 'old', 'setup', 'docs', 'action', false, 'setup_read', $1),
		  ('t-old-setup', 'new', 'setup', 'taf_task', 'action', false, 'setup', $1),
		  ('t-old-setup', 'a-read', 'taf', 'docs', 'task', false, 'read', $1)`, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	m := meter.NewPostgres(pool)
	got, err := m.Totals(ctx, "t-old-setup", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	actions := got[meter.UnitAction]
	if actions.Free[meter.FreeSetup] != 2 || actions.FreeByAgent["setup"][meter.FreeSetup] != 2 || got[meter.UnitTask].Free[meter.FreeRead] != 1 {
		t.Fatalf("totals: actions %v by agent %v, tasks %v", actions.Free, actions.FreeByAgent, got[meter.UnitTask].Free)
	}
	if _, ok := actions.Free["setup_read"]; ok {
		t.Fatalf("an old kind shown on its own: %v", actions.Free)
	}
}

// Usage by person: KIFF notes who asked for a call when it
// decides it, and the meter carries it onto the call once it has ended. What
// no person's run made counts under no one; free calls count for no one.
func TestUsageByPerson(t *testing.T) {
	ctx := context.Background()
	pool := fixture(t)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
		INSERT INTO cloud_meta.tenants VALUES ('tp');
		INSERT INTO cloud_meta.agents VALUES ('tp', 'taf-refunds', 'taf'), ('tp', 'bot', '');
		INSERT INTO cloud_meta.api_keys VALUES ('key-p', '[]');
		INSERT INTO gateway.tools (tenant_id, name, read_only, title) VALUES ('tp', 'refund', false, '');`); err != nil {
		t.Fatal(err)
	}
	m := meter.NewPostgres(pool)
	for _, c := range []struct{ key, agent, outcome, asked string }{
		{"p1", "taf-refunds", "done", "ana"}, {"p2", "taf-refunds", "done", "ana"}, {"p3", "taf-refunds", "done", "luis"},
		{"p4", "taf-refunds", "error", "luis"}, {"p5", "taf-refunds", "done", ""}, {"p6", "bot", "done", "ana"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO gateway.invocations
			(key, tenant_id, credential_id, agent_id, tool, state, tool_outcome, exception_id, reasons, updated_at)
			VALUES ($1, 'tp', 'key-p', $2, 'refund', 'forwarded', $3, '', '{}', $4)`, c.key, c.agent, c.outcome, at); err != nil {
			t.Fatal(err)
		}
		if err := m.AskedBy(ctx, "tp", c.key, c.asked); err != nil {
			t.Fatal(err)
		}
	}
	// Once per call: a second note for the same call changes nothing.
	if err := m.AskedBy(ctx, "tp", "p1", "luis"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := m.ByPerson(ctx, "tp", at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []meter.PersonCount{{Subject: "ana", Tasks: 2, Actions: 1}, {Subject: "", Tasks: 1}, {Subject: "luis", Tasks: 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("by person %+v, want %+v", got, want)
	}

	// A person's own calls: the ones their runs made.
	mine, err := m.KeysAskedBy(ctx, "tp", "luis")
	if err != nil || len(mine) != 2 || !mine["p3"] || !mine["p4"] {
		t.Fatalf("luis's calls: %v %v", mine, err)
	}
	if none, err := m.KeysAskedBy(ctx, "tp", ""); err != nil || len(none) != 0 {
		t.Fatalf("no one's calls: %v %v", none, err)
	}
	// Counted tasks, as luis reads them: only his own run's.
	from, to := at.Add(-time.Hour), at.Add(time.Hour)
	keys := func(ts []meter.Task) string {
		var out []string
		for _, t := range ts {
			out = append(out, t.Record)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if luis, _, err := m.Tasks(ctx, "tp", "", &meter.TaskScope{Subject: "luis"}, from, to, nil, 10); err != nil || keys(luis) != "p3" {
		t.Fatalf("luis's tasks: %v %v", keys(luis), err)
	}
	// Someone who manages taf-refunds reads all of its tasks, whoever asked.
	if all, _, err := m.Tasks(ctx, "tp", "", &meter.TaskScope{Subject: "luis", Agents: []string{"taf-refunds"}}, from, to, nil, 10); err != nil || keys(all) != "p1,p2,p3,p5" {
		t.Fatalf("a manager's tasks: %v %v", keys(all), err)
	}
	// A scope with no person and no agent reads nothing: not every task with no requester.
	if none, _, err := m.Tasks(ctx, "tp", "", &meter.TaskScope{}, from, to, nil, 10); err != nil || len(none) != 0 {
		t.Fatalf("an empty scope's tasks: %v %v", keys(none), err)
	}
}
