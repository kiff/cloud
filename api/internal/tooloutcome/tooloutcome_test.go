package tooloutcome_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/testdb"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

func TestFromRecord(t *testing.T) {
	at := time.Date(2026, 10, 8, 9, 36, 0, 0, time.UTC)
	rec := func(r tooloutcome.Record) tooloutcome.Record { r.At = at; return r }
	for name, c := range map[string]struct {
		r    tooloutcome.Record
		want tooloutcome.Outcome
		ok   bool
	}{
		"recorded done":    {rec(tooloutcome.Record{State: "forwarded", ToolOutcome: "done", HasResult: true}), tooloutcome.Outcome{Status: tooloutcome.Done, At: at}, true},
		"recorded refusal": {rec(tooloutcome.Record{State: "forwarded", ToolOutcome: "error", ToolError: "card declined"}), tooloutcome.Outcome{Status: tooloutcome.RefusedByTool, Message: "card declined", At: at}, true},
		// Rows from before tool_outcome, or from an older
		// gateway during a rollout, are read from their result while it is
		// kept, and as not recorded once it is purged, never as done.
		"legacy refusal, result kept": {rec(tooloutcome.Record{State: "forwarded", HasResult: true, ResultError: true, ResultText: " Refund  declined "}),
			tooloutcome.Outcome{Status: tooloutcome.RefusedByTool, Message: "Refund declined", At: at}, true},
		"legacy success, result kept": {rec(tooloutcome.Record{State: "forwarded", HasResult: true}), tooloutcome.Outcome{Status: tooloutcome.Done, At: at}, true},
		"legacy, result purged":       {rec(tooloutcome.Record{State: "forwarded"}), tooloutcome.Outcome{Status: tooloutcome.NotRecorded, At: at}, true},
		"unknown":                     {rec(tooloutcome.Record{State: "unknown", Message: "response lost"}), tooloutcome.Outcome{Status: tooloutcome.Unknown, Message: "response lost", At: at}, true},
		"failed":                      {rec(tooloutcome.Record{State: "failed", Message: "credential unreadable"}), tooloutcome.Outcome{Status: tooloutcome.NotSent, Message: "credential unreadable", At: at}, true},
		"forwarding":                  {rec(tooloutcome.Record{State: "forwarding"}), tooloutcome.Outcome{Status: tooloutcome.Sending, At: at}, true},
		"held":                        {rec(tooloutcome.Record{State: "held"}), tooloutcome.Outcome{}, false},
		"refused":                     {rec(tooloutcome.Record{State: "refused"}), tooloutcome.Outcome{}, false},
	} {
		got, ok := tooloutcome.FromRecord(c.r)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: %+v %v, want %+v %v", name, got, ok, c.want, c.ok)
		}
	}
}

// postgresURL is this package's test database; without
// KIFF_TEST_POSTGRES_URL the Postgres-backed tests skip.
func postgresURL(t *testing.T) string {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	return url
}

// The API reads the gateway's own records in the shared database, scoped
// to the tenant, and only by proposal id. Without the gateway's tables it
// reads nothing.
func TestPostgresReadsTheGatewayRecord(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	withoutGatewaySchema(t, pool)
	r := tooloutcome.NewPostgres(pool)
	if got, err := r.Outcomes(ctx, "t1", []string{"tc-1"}); err != nil || len(got) != 0 {
		t.Fatalf("without the gateway's tables: %v %v", got, err)
	}
	// The table as an older gateway left it: no tool_error or
	// tool_outcome. Delivered calls read from their result while it is
	// kept, and as not recorded once purged.
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA gateway;
		CREATE TABLE gateway.invocations (key text PRIMARY KEY, tenant_id text NOT NULL, state text NOT NULL,
			message text NOT NULL DEFAULT '', result jsonb, updated_at timestamptz NOT NULL);
		INSERT INTO gateway.invocations VALUES
			('tc-old-refused', 't1', 'forwarded', '', '{"content":[{"type":"text","text":"Refund declined"}],"isError":true}', now()),
			('tc-old-purged', 't1', 'forwarded', '', NULL, now()),
			('tc-bad-iserror', 't1', 'forwarded', '', '{"content":[{"type":"text","text":"ok"}],"isError":"maybe"}', now())`); err != nil {
		t.Fatal(err)
	}
	// A tool's result with a non-boolean isError does not
	// fail the read, so the refusal next to it still shows. The malformed
	// flag itself reads as a refusal, never as done.
	got, err := r.Outcomes(ctx, "t1", []string{"tc-old-refused", "tc-old-purged", "tc-bad-iserror"})
	if err != nil || got["tc-old-refused"].Status != tooloutcome.RefusedByTool || got["tc-old-refused"].Message != "Refund declined" ||
		got["tc-old-purged"].Status != tooloutcome.NotRecorded || got["tc-bad-iserror"].Status != tooloutcome.RefusedByTool {
		t.Fatalf("before the columns: %+v %v", got, err)
	}

	// Then as this gateway leaves it: the columns added with their
	// defaults, so old rows keep tool_outcome '' (not recorded).
	if _, err := pool.Exec(ctx, `
		ALTER TABLE gateway.invocations ADD COLUMN tool_error text NOT NULL DEFAULT '';
		ALTER TABLE gateway.invocations ADD COLUMN tool_outcome text NOT NULL DEFAULT '';
		INSERT INTO gateway.invocations (key, tenant_id, state, tool_error, tool_outcome, result, updated_at) VALUES
			('tc-done', 't1', 'forwarded', '', 'done', NULL, now()),
			('tc-refused', 't1', 'forwarded', 'Refund amount is greater than unrefunded amount', 'error', NULL, now()),
			('tc-other-tenant', 't2', 'forwarded', 'not yours', 'error', NULL, now()),
			('tc-held', 't1', 'held', '', '', NULL, now())`); err != nil {
		t.Fatal(err)
	}
	got, err = r.Outcomes(ctx, "t1", []string{"tc-done", "tc-refused", "tc-other-tenant", "tc-held", "tc-missing", "tc-old-refused", "tc-old-purged"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got["tc-done"].Status != tooloutcome.Done ||
		got["tc-refused"].Status != tooloutcome.RefusedByTool || got["tc-refused"].Message != "Refund amount is greater than unrefunded amount" ||
		got["tc-old-refused"].Status != tooloutcome.RefusedByTool || got["tc-old-purged"].Status != tooloutcome.NotRecorded {
		t.Errorf("outcomes: %+v", got)
	}
}

// The person answering a held call sees the arguments it will be sent
// with, read from the gateway's record for this tenant only, as
// raw JSON: a large integer keeps every digit. A call
// with no arguments reads as "{}", one with no record is absent.
func TestPostgresReadsAHeldCallsArguments(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	withoutGatewaySchema(t, pool)
	r := tooloutcome.NewPostgres(pool)
	if got, err := r.Arguments(ctx, "t1", []string{"tc-1"}); err != nil || len(got) != 0 {
		t.Fatalf("without the gateway's tables: %v %v", got, err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA gateway;
		CREATE TABLE gateway.invocations (key text PRIMARY KEY, tenant_id text NOT NULL, state text NOT NULL,
			arguments jsonb NOT NULL, updated_at timestamptz NOT NULL);
		INSERT INTO gateway.invocations VALUES
			('tc-held', 't1', 'held', '{"order_id":"70349","account":9007199254740993,"nested":{"n":1.10}}', now()),
			('tc-noargs', 't1', 'held', '{}', now()),
			('tc-other-tenant', 't2', 'held', '{"order_id":"1"}', now())`); err != nil {
		t.Fatal(err)
	}
	got, err := r.Arguments(ctx, "t1", []string{"tc-held", "tc-noargs", "tc-other-tenant", "tc-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["tc-noargs"]) != "{}" {
		t.Fatalf("arguments: %q", got)
	}
	var held map[string]json.RawMessage
	if err := json.Unmarshal(got["tc-held"], &held); err != nil {
		t.Fatal(err)
	}
	if string(held["account"]) != "9007199254740993" || string(held["order_id"]) != `"70349"` || string(held["nested"]) != `{"n": 1.10}` {
		t.Errorf("held arguments: %q", held)
	}
}

// withoutGatewaySchema starts the test with no gateway tables and drops
// what it created: the package's tests share one database.
func withoutGatewaySchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	drop := func() error {
		_, err := pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS gateway CASCADE`)
		return err
	}
	if err := drop(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = drop() })
}

// The API reads who connected each gateway tool, scoped to the
// tenant. Without the table, or with a gateway older than its created_by
// column, the read fails, so a connector key's writes fail closed.
func TestPostgresReadsToolOwners(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	withoutGatewaySchema(t, pool)
	r := tooloutcome.NewPostgres(pool)
	if _, err := r.ToolOwners(ctx, "t1"); err == nil {
		t.Fatal("read owners without the gateway's tables")
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA gateway; CREATE TABLE gateway.tools (tenant_id text NOT NULL, name text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ToolOwners(ctx, "t1"); err == nil {
		t.Fatal("read owners from a gateway older than created_by")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE gateway.tools ADD COLUMN created_by text NOT NULL DEFAULT '';
		INSERT INTO gateway.tools VALUES ('t1', 'owner_refund', ''), ('t1', 'planque_refund', 'key-c'), ('t2', 'other', 'key-x')`); err != nil {
		t.Fatal(err)
	}
	got, err := r.ToolOwners(ctx, "t1")
	if err != nil || len(got) != 2 || got["owner_refund"] != "" || got["planque_refund"] != "key-c" {
		t.Fatalf("owners: %v %v", got, err)
	}
}

// Each gateway tool's title for people, by its name; tools without one are
// left out.
func TestPostgresReadsToolTitles(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	withoutGatewaySchema(t, pool)
	r := tooloutcome.NewPostgres(pool)
	if _, err := r.ToolTitles(ctx, "t1"); err == nil {
		t.Fatal("read titles without the gateway's tables")
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA gateway; CREATE TABLE gateway.tools (tenant_id text NOT NULL, name text NOT NULL, title text NOT NULL DEFAULT '');
		INSERT INTO gateway.tools VALUES ('t1', 'create_refund_k3f9', 'Refund a payment'), ('t1', 'untitled', ''), ('t2', 'other', 'Other')`); err != nil {
		t.Fatal(err)
	}
	got, err := r.ToolTitles(ctx, "t1")
	if err != nil || len(got) != 1 || got["create_refund_k3f9"] != "Refund a payment" {
		t.Fatalf("titles: %v %v", got, err)
	}
}
