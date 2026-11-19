package tenant_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/handoff"
	"github.com/kiff/cloud/api/internal/tenant"
)

// A prune takes a hand-off only once it closed with its settled receipt
// saved, and is older than the cutoff: one still waiting, or still owed its
// receipt, stays.
func TestPruneTakesOnlySettledHandoffs(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	dropTenantSchemas(t, pool)
	ctx := context.Background()
	defer func() { dropTenantSchemas(t, pool); truncateTenants(t, pool) }()
	if err := handoff.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	a := sampleTenant("prune-handoffs")
	if err := store.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := prov.Provision(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-72 * time.Hour)
	for id, settled := range map[string]any{"ho_settled": old, "ho_waiting": nil, "ho_owed": nil} {
		status := "waiting"
		if id != "ho_waiting" {
			status = "skipped"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.handoffs (tenant_id, id, agent_id, operation, what, why, status, created_at, expires_at, settled_at)
			VALUES ($1, $2, 'agent-a', $2, 'w', 'y', $3, $4, $5, $6)`, a.ID, id, status, old, old.Add(handoff.Wait), settled); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := prov.Prune(ctx, a.ID, 48*time.Hour, now, false); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT id FROM cloud_meta.handoffs WHERE tenant_id = $1 ORDER BY id`, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if len(left) != 2 || left[0] != "ho_owed" || left[1] != "ho_waiting" {
		t.Fatalf("left after the prune: %v", left)
	}
}
