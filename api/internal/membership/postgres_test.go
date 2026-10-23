package membership_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

func pgStore(t *testing.T) (*pgxpool.Pool, *membership.PostgresStore) {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip(testdb.EnvVar + " not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := tenant.ApplySubjectTenantsSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := membership.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := membership.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("schema is not idempotent: %v", err)
	}
	return pool, membership.NewPostgresStore(pool)
}

func seedTenant(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.membership_audit WHERE tenant_id = $1`, id)
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.tenant_invites WHERE tenant_id = $1`, id)
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.tenant_memberships WHERE tenant_id = $1`, id)
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.tenant_governance WHERE tenant_id = $1`, id)
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.subject_tenants WHERE tenant_id = $1`, id)
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants (id, slug, status) VALUES ($1, $1, 'active') ON CONFLICT (id) DO NOTHING`, id); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresBackfillIsIdempotent(t *testing.T) {
	pool, st := pgStore(t)
	ctx := context.Background()
	seedTenant(t, pool, "pg-backfill")
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.subject_tenants (subject, tenant_id) VALUES ('pg-owner', 'pg-backfill')
		ON CONFLICT (subject) DO UPDATE SET tenant_id = EXCLUDED.tenant_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := membership.Backfill(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := membership.Backfill(ctx, pool); err != nil {
		t.Fatal(err)
	}
	ms, err := st.List(ctx, "pg-backfill")
	if err != nil || len(ms) != 1 || ms[0].Role != membership.RoleAdmin || ms[0].Subject != "pg-owner" {
		t.Fatalf("backfill = %+v, %v; want one admin", ms, err)
	}
	evs, _ := st.Audit(ctx, "pg-backfill", 0)
	if len(evs) != 1 || evs[0].Action != membership.AuditBackfilled {
		t.Fatalf("backfill audit = %+v", evs)
	}
	if g, err := st.Governance(ctx, "pg-backfill"); err != nil || g.SeparationOfDuties {
		t.Fatalf("governance = %+v, %v", g, err)
	}
}

func TestPostgresServiceFlow(t *testing.T) {
	pool, st := pgStore(t)
	ctx := context.Background()
	seedTenant(t, pool, "pg-flow")
	svc := membership.NewService(st)
	if _, err := svc.EnsureFounder(ctx, "pg-flow", "pg-alice"); err != nil {
		t.Fatal(err)
	}
	inv, tok, err := svc.CreateInvite(ctx, "pg-flow", membership.Actor{Subject: "pg-alice"}, membership.InviteRequest{Email: "b@x.io", Role: membership.RoleApprover, Lifetime: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.InviteByHash(ctx, membership.HashInviteToken(tok)); err != nil || got.ID != inv.ID {
		t.Fatalf("invite by hash: %+v %v", got, err)
	}
	if _, err := svc.AcceptInvite(ctx, tok, membership.Actor{Subject: "pg-bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptInvite(ctx, tok, membership.Actor{Subject: "pg-carl"}); !errors.Is(err, membership.ErrInviteNotPending) {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := svc.SetSeparationOfDuties(ctx, "pg-flow", membership.Actor{Subject: "pg-alice"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Remove(ctx, "pg-flow", membership.Actor{Subject: "pg-alice"}, "pg-bob"); !errors.Is(err, membership.ErrLastApprover) {
		t.Fatalf("remove last approver under SoD: %v", err)
	}
	if _, err := svc.SetSeparationOfDuties(ctx, "pg-flow", membership.Actor{Subject: "pg-alice"}, false); err != nil {
		t.Fatal(err)
	}
	first, _ := st.Live(ctx, "pg-flow", "pg-bob")
	if _, err := svc.Remove(ctx, "pg-flow", membership.Actor{Subject: "pg-alice"}, "pg-bob"); err != nil {
		t.Fatal(err)
	}
	// Rejoining inserts a second row beside the removed one (partial unique index).
	_, tok2, _ := svc.CreateInvite(ctx, "pg-flow", membership.Actor{Subject: "pg-alice"}, membership.InviteRequest{Email: "b@x.io", Role: membership.RoleViewer})
	again, err := svc.AcceptInvite(ctx, tok2, membership.Actor{Subject: "pg-bob"})
	if err != nil || again.ID == first.ID {
		t.Fatalf("rejoin: %+v %v", again, err)
	}
	if err := st.RecordHoldAnswer(ctx, membership.HoldAnswer{TenantID: "pg-flow", HoldID: "h1", Answer: "approve-once", Answerer: "pg-bob", Role: membership.RoleApprover, SeparationOfDuties: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.HoldAnswers(ctx, "pg-flow", []string{"h1", "h2"}); len(got) != 1 || got["h1"].Role != membership.RoleApprover || got["h1"].Device != nil {
		t.Fatalf("hold answers = %+v", got)
	}
	// A phone's answer keeps its signed proof.
	proof := &membership.DeviceProof{Name: "tafs on iPhone", KeyID: "sess-1", PublicKey: []byte{0x30, 0x59, 0x01}, Message: "kiff.answer.v1\npg-flow\nh2", Signature: "MEUC"}
	if err := st.RecordHoldAnswer(ctx, membership.HoldAnswer{TenantID: "pg-flow", HoldID: "h2", Answer: "reject", Answerer: "pg-bob", Device: proof}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.HoldAnswers(ctx, "pg-flow", []string{"h2"}); got["h2"].Device == nil || got["h2"].Device.Name != proof.Name || got["h2"].Device.KeyID != proof.KeyID || (got["h2"].Device.Message != proof.Message || string(got["h2"].Device.PublicKey) != string(proof.PublicKey) || got["h2"].Device.Signature != proof.Signature) {
		t.Fatalf("device proof = %+v", got["h2"].Device)
	}
}

// Two admins demoting each other at once must not leave zero admins:
// Mutate's per-tenant lock serializes the check and the change.
func TestPostgresLastAdminUnderConcurrency(t *testing.T) {
	pool, st := pgStore(t)
	ctx := context.Background()
	seedTenant(t, pool, "pg-race")
	svc := membership.NewService(st)
	if _, err := svc.EnsureFounder(ctx, "pg-race", "pg-a"); err != nil {
		t.Fatal(err)
	}
	_, tok, _ := svc.CreateInvite(ctx, "pg-race", membership.Actor{Subject: "pg-a"}, membership.InviteRequest{Email: "b@x.io", Role: membership.RoleAdmin})
	if _, err := svc.AcceptInvite(ctx, tok, membership.Actor{Subject: "pg-b"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, pair := range [][2]string{{"pg-a", "pg-b"}, {"pg-b", "pg-a"}} {
		wg.Add(1)
		go func(i int, actor, target string) {
			defer wg.Done()
			_, _, errs[i] = svc.ChangeRole(ctx, "pg-race", membership.Actor{Subject: actor}, target, membership.RoleViewer)
		}(i, pair[0], pair[1])
	}
	wg.Wait()
	ms, _ := st.List(ctx, "pg-race")
	admins := 0
	for _, m := range ms {
		if m.Role == membership.RoleAdmin && m.Active() {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("admins after concurrent demotions = %d (errs %v); want exactly 1 (M9)", admins, errs)
	}
}
