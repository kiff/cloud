package evidence_test

import (
	"context"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/evidence"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// TestCompose_PostgresEndToEnd wires the composer over the real
// Postgres-backed stores (decisions reader, receipt store, posture
// reader) and asserts it assembles a coherent, tenant-scoped object —
// the "is the SQL path covered?" integration. Gated on
// KIFF_TEST_POSTGRES_URL.
func TestCompose_PostgresEndToEnd(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := receipt.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("receipt.ApplySchema: %v", err)
	}
	// Clean slate.
	dropTenantSchemas(t, pool)
	if _, err := pool.Exec(ctx, "TRUNCATE cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	defer func() {
		dropTenantSchemas(t, pool)
		_, _ = pool.Exec(context.Background(), "TRUNCATE cloud_meta.tenants CASCADE")
	}()

	// Provision a tenant (creates the per-tenant kiff_audit schema).
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "ev-pg", DisplayName: "ev pg", Status: tenant.StatusProvisioning}
	if err := store.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := prov.Provision(ctx, tnt.ID); err != nil {
		t.Fatalf("provision: %v", err)
	}

	schema := tenant.NewSchemaPool(pool)
	base := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	win := evidence.Window{From: base.Add(-24 * time.Hour), To: base.Add(time.Hour)}

	// Seed a proposal_decision audit row the decisions reader will
	// return, scoped to control "ctrl-pg".
	seedDecision(t, schema, tnt.ID, "p-pg", "ctrl-pg", "REFUND_ORDER", "allowed", base)

	// Seed a signed receipt tied to the decision (proposal_id p-pg) so
	// the control-scope per-decision read surfaces it.
	if err := receipt.NewPostgresStore(pool).Save(ctx, receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion, TenantID: tnt.ID, TraceID: "exec-pg", ProposalID: "p-pg",
		EntityID: "o1", ActionName: "REFUND_ORDER", IssuedAt: base.Add(30 * time.Minute),
		Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig", WalletAddress: "0xw"},
	}); err != nil {
		t.Fatalf("save receipt: %v", err)
	}

	// Seed a posture transition (enforce->shadow->enforce).
	if _, _, err := posture.ChangeMode(ctx, schema, tnt.ID, tenant.ModeEnforce, tenant.ModeObserve, "alice", func() time.Time { return base.Add(-12 * time.Hour) }); err != nil {
		t.Fatalf("posture flip 1: %v", err)
	}
	if _, _, err := posture.ChangeMode(ctx, schema, tnt.ID, tenant.ModeObserve, tenant.ModeEnforce, "bob", func() time.Time { return base.Add(-6 * time.Hour) }); err != nil {
		t.Fatalf("posture flip 2: %v", err)
	}

	// A control record so the control_id scope resolves.
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID: "ctrl-pg", TenantID: tnt.ID, Domain: "refund", Entity: "Order",
		Actions: []string{"REFUND_ORDER"}, Environment: "dev", Workflow: "default", Version: 1, Status: "active",
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}

	c := evidence.NewComposer(
		evidence.WithControls(controls),
		evidence.WithDecisions(proposals.NewPostgresReader(schema, receipt.NewPostgresStore(pool))),
		evidence.WithReceipts(receipt.NewPostgresStore(pool)),
		evidence.WithPosture(posture.NewPostgresReader(schema)),
		evidence.WithClock(func() time.Time { return base }),
	)

	ev, err := c.Compose(ctx, evidence.Input{
		TenantID:  tnt.ID,
		ControlID: "ctrl-pg",
		Window:    win,
		Decisions: proposals.DecisionQuery{ControlID: "ctrl-pg", From: win.From, To: win.To},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	if ev.Decisions.Total != 1 || ev.Decisions.Counts["allowed"] != 1 {
		t.Errorf("decisions: total=%d counts=%+v", ev.Decisions.Total, ev.Decisions.Counts)
	}
	// Control scope: receipts are resolved per decision via the explicit
	// proposal->receipt link, so the receipt tied to decision p-pg
	// surfaces — mechanically scoped to this control, not the action.
	if ev.Receipts.Status != evidence.StatusComplete {
		t.Errorf("control-scope receipts status = %q, want complete", ev.Receipts.Status)
	}
	if ev.Receipts.Count != 1 || ev.Receipts.SignedCount != 1 {
		t.Errorf("control-scope receipts: %+v (want count 1 / signed 1, tied to decision p-pg)", ev.Receipts)
	}
	if len(ev.Receipts.Samples) != 1 || ev.Receipts.Samples[0].TraceID != "exec-pg" {
		t.Errorf("control-scope receipt sample wrong: %+v", ev.Receipts.Samples)
	}
	if ev.Posture.Status != evidence.StatusComplete || ev.Posture.BecameEnforceBy != "bob" {
		t.Errorf("posture: %+v", ev.Posture)
	}

	// Action scope (proof-pack route) DOES collect receipts from the PG
	// store, filtered by action + window, with computed verifiability.
	evAction, err := c.Compose(ctx, evidence.Input{
		TenantID:  tnt.ID,
		Action:    "REFUND_ORDER",
		Window:    win,
		Decisions: proposals.DecisionQuery{Action: "REFUND_ORDER", From: win.From, To: win.To},
	})
	if err != nil {
		t.Fatalf("Compose (action scope): %v", err)
	}
	if evAction.Receipts.Count != 1 || evAction.Receipts.SignedCount != 1 {
		t.Errorf("action-scope receipts: %+v", evAction.Receipts)
	}
	if v := evAction.Verifiability(); v.Anchored != "none" {
		t.Errorf("verifiability anchored = %q, want none (signed but not anchored)", v.Anchored)
	}
}

// seedDecision inserts a proposal_decision audit row into the tenant's
// kiff_audit (the shape proposals.PostgresReader reads).
func seedDecision(t *testing.T, schema *tenant.SchemaPool, tenantID, proposalID, controlID, action, outcome string, at time.Time) {
	t.Helper()
	err := schema.WithTenantConn(context.Background(), tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO kiff_audit (id, kind, entity_id, entity_type, data, trace_id, created_at)
			VALUES ($1, 'proposal_decision', 'o1', 'Order', $2::jsonb, $3, $4)
		`, proposalID+":"+outcome,
			`{"proposal_id":"`+proposalID+`","action":"`+action+`","outcome":"`+outcome+`","control_id":"`+controlID+`"}`,
			proposalID, at)
		return err
	})
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}
}

func dropTenantSchemas(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 'kiff_t_%'`)
	if err != nil {
		t.Fatalf("list schemas: %v", err)
	}
	var schemas []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		schemas = append(schemas, s)
	}
	rows.Close()
	for _, s := range schemas {
		if _, err := pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+s+" CASCADE"); err != nil {
			t.Fatalf("drop schema %s: %v", s, err)
		}
	}
}
