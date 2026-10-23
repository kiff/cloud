package tenant_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// A prune deletes what a test account finished with before the cutoff and
// keeps the account going: its live key and live Card with what that Card
// has drawn, its newer records, any operation with recent activity or an
// open approval, and every other account's rows.
func TestPruneKeepsTheAccountGoing(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	dropTenantSchemas(t, pool)
	ctx := context.Background()
	defer func() { dropTenantSchemas(t, pool); truncateTenants(t, pool) }()
	for name, apply := range map[string]func(context.Context, *pgxpool.Pool) error{
		"keys": auth.ApplyAPIKeysSchema, "subjects": tenant.ApplySubjectTenantsSchema,
		"mandate": mandate.ApplySchema, "receipt": receipt.ApplySchema,
	} {
		if err := apply(ctx, pool); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	now := time.Now()
	old, recent := now.Add(-72*time.Hour), now.Add(-time.Hour)
	a, b := sampleTenant("prune-a"), sampleTenant("prune-b")
	for _, tt := range []tenant.Tenant{a, b} {
		if err := store.Create(ctx, tt); err != nil {
			t.Fatal(err)
		}
		if err := prov.Provision(ctx, tt.ID); err != nil {
			t.Fatal(err)
		}
		id, schema := tt.ID, tenant.SchemaNameFor(tt.ID)
		exec(`INSERT INTO cloud_meta.subject_tenants (subject, tenant_id) VALUES ($1 || '-owner', $1)`, id)
		exec(`INSERT INTO cloud_meta.mandates (id, tenant_id, subject, revoked_at) VALUES ($1 || '-ended', $1, 'agent-a', $2)`, id, old)
		exec(`INSERT INTO cloud_meta.mandates (id, tenant_id, subject) VALUES ($1 || '-live', $1, 'agent-a')`, id)
		exec(`INSERT INTO cloud_meta.mandate_draws (tenant_id, mandate_id, proposal_id, quantity_key, amount, drawn_at) VALUES
			($1, $1 || '-ended', 'p-ended', 'count', 1, $2), ($1, $1 || '-live', 'p-live', 'count', 1, $2)`, id, old)
		exec(`INSERT INTO cloud_meta.api_keys (id, tenant_id, subject, key_hash, revoked_at) VALUES ($1 || '-revoked', $1, 'k', $1 || '-h1', $2)`, id, old)
		exec(`INSERT INTO cloud_meta.api_keys (id, tenant_id, subject, key_hash, created_at) VALUES ($1 || '-live', $1, 'k', $1 || '-h2', $2)`, id, old)
		for trace, at := range map[string]time.Time{"tr-old": old, "tr-new": recent} {
			exec(`INSERT INTO cloud_meta.receipts (tenant_id, trace_id, schema_version, entity_id, entity_type, action_name, issued_at, causal_chain_hash, body, created_at)
				VALUES ($1, $2, '1', 'e', 'Order', 'REFUND', $3, 'h', '{}', $3)`, id, trace, at)
		}
		// In the account's schema: op-old is done; op-mixed has a recent
		// decision; op-waiting has an approval still pending.
		for _, row := range []struct {
			entity string
			at     time.Time
		}{{"op-old", old}, {"op-mixed", old}, {"op-waiting", old}} {
			exec(`INSERT INTO `+schema+`.kiff_events (id, type, entity_id, entity_type, source, actor_id, occurred_at, inserted_at)
				VALUES ($1 || '-evt', 'TOOL_CALL_REQUESTED', $1, 'ToolCall', 'gateway', 'agent-a', $2, $2)`, row.entity, row.at)
			exec(`INSERT INTO `+schema+`.kiff_audit (id, kind, entity_id, entity_type, created_at, inserted_at)
				VALUES ($1 || '-aud', 'proposal_decision', $1, 'ToolCall', $2, $2)`, row.entity, row.at)
		}
		exec(`INSERT INTO `+schema+`.kiff_decisions (id, entity_id, entity_type, kind, actor_id, created_at, inserted_at)
			VALUES ('op-mixed-dec', 'op-mixed', 'ToolCall', 'allow', 'agent-a', $1, $1)`, recent)
		exec(`INSERT INTO `+schema+`.kiff_approvals (id, entity_id, entity_type, action_name, requested_by, status, created_at, inserted_at)
			VALUES ('op-waiting-apr', 'op-waiting', 'ToolCall', 'refund', 'agent-a', 'pending', $1, $1)`, old)
	}

	if _, err := prov.Prune(ctx, a.ID, time.Hour, now, false); err == nil {
		t.Fatal("pruned with a cutoff younger than a day")
	}
	dry, err := prov.Prune(ctx, a.ID, 48*time.Hour, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM cloud_meta.receipts WHERE tenant_id = $1`, a.ID); n != 2 || dry.Total() == 0 {
		t.Fatalf("a dry run: %d receipts left, counted %v", n, dry)
	}
	got, err := prov.Prune(ctx, a.ID, 48*time.Hour, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total() != dry.Total() {
		t.Errorf("pruned %v, the dry run counted %v", got, dry)
	}

	schemaA, schemaB := tenant.SchemaNameFor(a.ID), tenant.SchemaNameFor(b.ID)
	for _, c := range []struct {
		name string
		q    string
		want int
	}{
		{"the ended Card", `SELECT count(*) FROM cloud_meta.mandates WHERE id = $1 || '-ended'`, 0},
		{"the ended Card's draw", `SELECT count(*) FROM cloud_meta.mandate_draws WHERE mandate_id = $1 || '-ended'`, 0},
		{"the live Card", `SELECT count(*) FROM cloud_meta.mandates WHERE id = $1 || '-live'`, 1},
		{"the live Card's old draw", `SELECT count(*) FROM cloud_meta.mandate_draws WHERE mandate_id = $1 || '-live'`, 1},
		{"the revoked key", `SELECT count(*) FROM cloud_meta.api_keys WHERE id = $1 || '-revoked'`, 0},
		{"the live key", `SELECT count(*) FROM cloud_meta.api_keys WHERE id = $1 || '-live'`, 1},
		{"the old receipt", `SELECT count(*) FROM cloud_meta.receipts WHERE tenant_id = $1 AND trace_id = 'tr-old'`, 0},
		{"the new receipt", `SELECT count(*) FROM cloud_meta.receipts WHERE tenant_id = $1 AND trace_id = 'tr-new'`, 1},
		{"the owner", `SELECT count(*) FROM cloud_meta.subject_tenants WHERE tenant_id = $1`, 1},
		{"the done operation", `SELECT count(*) FROM ` + schemaA + `.kiff_events WHERE entity_id = 'op-old' AND $1 <> ''`, 0},
		{"the done operation's Record", `SELECT count(*) FROM ` + schemaA + `.kiff_audit WHERE entity_id = 'op-old' AND $1 <> ''`, 0},
		{"an operation with recent activity", `SELECT count(*) FROM ` + schemaA + `.kiff_events WHERE entity_id = 'op-mixed' AND $1 <> ''`, 1},
		{"an operation waiting for an answer", `SELECT count(*) FROM ` + schemaA + `.kiff_audit WHERE entity_id = 'op-waiting' AND $1 <> ''`, 1},
	} {
		if n := count(c.q, a.ID); n != c.want {
			t.Errorf("%s: %d row(s), want %d", c.name, n, c.want)
		}
	}
	// The other account is untouched.
	if n := count(`SELECT count(*) FROM cloud_meta.receipts WHERE tenant_id = $1`, b.ID); n != 2 {
		t.Errorf("the other account lost receipts: %d left", n)
	}
	if n := count(`SELECT count(*) FROM ` + schemaB + `.kiff_events`); n != 3 {
		t.Errorf("the other account lost events: %d left", n)
	}
	if n := count(`SELECT count(*) FROM cloud_meta.mandates WHERE tenant_id = $1`, b.ID); n != 2 {
		t.Errorf("the other account lost Cards: %d left", n)
	}
	if st, err := store.Get(ctx, a.ID); err != nil || st.Status != tenant.StatusActive {
		t.Errorf("the account after a prune: %+v %v", st, err)
	}
}
