package halts_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

var ctx = context.Background()

// contract is what every Store does: a halt's first time is kept by a
// second halt and by a resume, an agent's halt and the account's are apart,
// and planque's ack is for the halt it saw, never a later one.
func contract(t *testing.T, s halts.Store, tenantID string) {
	t0 := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	if h, _ := s.Get(ctx, tenantID, "rae"); h.On || !h.HaltedAt.IsZero() {
		t.Fatalf("never halted reads %+v", h)
	}
	h, began, err := s.Halt(ctx, tenantID, "rae", t0, "owner")
	if err != nil || !began || !h.On || !h.HaltedAt.Equal(t0) || h.HaltedBy != "owner" || h.PlanqueAck != nil {
		t.Fatalf("halt: %+v %v %v", h, began, err)
	}
	if h, began, _ := s.Halt(ctx, tenantID, "rae", t0.Add(time.Minute), "admin"); began || !h.HaltedAt.Equal(t0) || h.HaltedBy != "owner" {
		t.Fatalf("a second halt began one, or moved its time: %+v %v", h, began)
	}
	if a, _ := s.Get(ctx, tenantID, halts.Account); a.On {
		t.Fatal("an agent's halt halted the account")
	}
	if err := s.AckPlanque(ctx, tenantID, "rae", t0.Add(time.Second), t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Get(ctx, tenantID, "rae"); h.PlanqueAck != nil {
		t.Fatal("an ack for another halt's time was kept")
	}
	if err := s.AckPlanque(ctx, tenantID, "rae", t0, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Get(ctx, tenantID, "rae"); h.PlanqueAck == nil || !h.PlanqueAck.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("ack: %+v", h)
	}
	h, ended, err := s.Resume(ctx, tenantID, "rae", t0.Add(time.Hour), "owner")
	if err != nil || !ended || h.On || !h.HaltedAt.Equal(t0) || h.ResumedAt == nil || h.ResumedBy != "owner" {
		t.Fatalf("resume: %+v %v %v", h, ended, err)
	}
	if _, ended, _ := s.Resume(ctx, tenantID, "rae", t0.Add(2*time.Hour), "owner"); ended {
		t.Fatal("resuming what isn't halted ended one")
	}
	// A new halt starts again: its own time, and no ack from the last one.
	t1 := t0.Add(3 * time.Hour)
	if h, began, _ := s.Halt(ctx, tenantID, "rae", t1, "owner"); !began || !h.HaltedAt.Equal(t1) || h.PlanqueAck != nil {
		t.Fatalf("the next halt: %+v %v", h, began)
	}
	if h, _, _ := s.Halt(ctx, tenantID, halts.Account, t1.Add(time.Minute), "owner"); !h.On || h.AgentID != halts.Account {
		t.Fatalf("the account's halt: %+v", h)
	}
	e, err := halts.For(ctx, s, tenantID, "rae")
	if err != nil || !e.On() || !e.At().Equal(t1.Add(time.Minute)) || e.Confirmed() {
		t.Fatalf("effective: %+v %v", e, err)
	}
	if !e.Before(t1) || e.Before(t1.Add(2*time.Minute)) {
		t.Fatal("Before doesn't follow the latest halt")
	}
	if other, _ := halts.For(ctx, s, tenantID, "ada"); !other.On() || other.Own.On {
		t.Fatalf("another agent under the account's halt: %+v", other)
	}
}

func TestMemoryStore(t *testing.T) { contract(t, halts.NewMemoryStore(), "t1") }

func TestPostgresStore(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip(testdb.EnvVar + " not set")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := halts.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.halts WHERE tenant_id = 'pg-halts'`)
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants (id, slug, status) VALUES ('pg-halts', 'pg-halts', 'active') ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	contract(t, halts.NewPostgresStore(pool), "pg-halts")
}

func TestAnAgentIDTooLongIsRefused(t *testing.T) {
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	if _, _, err := halts.NewMemoryStore().Halt(ctx, "t1", string(long), time.Now(), "owner"); err == nil {
		t.Fatal("halted an id no agent can have")
	}
}
