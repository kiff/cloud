package mandate_test

import (
	"context"
	"errors"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tenant"
)

// These exercise behaviour the memory store cannot prove, because it
// lives in the SQL: the draw upsert's conflict target, LEAST on revoke,
// the columns deliberately missing from the issue upsert, and the
// exclusion predicate that makes a retry idempotent.
//
//	createdb kiff_test
//	KIFF_TEST_POSTGRES_URL=postgres://postgres:postgres@localhost:5432/kiff_test go test ./internal/mandate/
func requirePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := mandate.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedTenant(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO cloud_meta.tenants (id, slug, display_name) VALUES ($1, $1, $1)
		 ON CONFLICT (id) DO NOTHING`, id); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM cloud_meta.tenants WHERE id = $1`, id)
	})
}

func pgMandate(tenantID, id string) mandate.Mandate {
	return mandate.Mandate{
		ID: id, TenantID: tenantID, Subject: "agent://a", Domain: "billing", Environment: "prod",
		Grants:     []mandate.Grant{{Action: "REFUND_INVOICE", EntityStates: []string{"PAID"}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Limit: 500000, Window: mandate.WindowCalendarDay}},
		ValidFrom:  base.Add(-24 * time.Hour), ValidUntil: base.Add(24 * time.Hour),
	}
}

// ApplySchema must be safe to run on every boot.
func TestPostgres_ApplySchemaIsIdempotent(t *testing.T) {
	pool := requirePostgres(t)
	for i := 0; i < 3; i++ {
		if err := mandate.ApplySchema(context.Background(), pool); err != nil {
			t.Fatalf("ApplySchema run %d: %v", i, err)
		}
	}
}

// Grants and aggregates round-trip through JSONB unchanged, including
// the parsed quantity. A silent decode failure would leave a mandate
// that bounds nothing.
func TestPostgres_MandateRoundTrips(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "pg-t1")
	s := mandate.NewPostgresStore(pool)
	ctx := context.Background()

	want := pgMandate("pg-t1", "mdt-rt")
	want.DomainID = "dom-billing"
	if err := s.Issue(ctx, want, "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, err := s.ForSubject(ctx, "pg-t1", "agent://a")
	if err != nil {
		t.Fatalf("ForSubject: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d mandates, want 1", len(got))
	}
	m := got[0]
	if len(m.Grants) != 1 || m.Grants[0].Action != "REFUND_INVOICE" || len(m.Grants[0].EntityStates) != 1 {
		t.Errorf("grants did not round-trip: %+v", m.Grants)
	}
	if len(m.Aggregates) != 1 {
		t.Fatalf("aggregates did not round-trip: %+v", m.Aggregates)
	}
	a := m.Aggregates[0]
	if a.Quantity.Kind != mandate.KindSum || a.Quantity.Parameter != "amount_cents" || a.Limit != 500000 {
		t.Errorf("aggregate did not round-trip: %+v", a)
	}
	if !m.Active(base) {
		t.Error("a mandate inside its validity window should be active after a round-trip")
	}
	if m.DomainID != "dom-billing" || m.Domain != "billing" {
		t.Errorf("domain did not round-trip: name %q id %q", m.Domain, m.DomainID)
	}
}

// The conflict target on mandate_draws is what makes a retry idempotent
// in production. Memory proves the intent; only SQL proves the key.
func TestPostgres_RecordingTheSameProposalTwiceDrawsOnce(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "pg-t2")
	s := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := s.Issue(ctx, pgMandate("pg-t2", "mdt-1"), "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	d := mandate.Draw{MandateID: "mdt-1", TenantID: "pg-t2", ProposalID: "prop-1",
		QuantityKey: "sum(amount_cents)", Amount: 6000, DrawnAt: base}
	for i := 0; i < 3; i++ {
		if err := s.Record(ctx, d); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
	used, err := s.Used(ctx, "pg-t2", "mdt-1", "sum(amount_cents)", base.Add(-time.Hour), "")
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != 6000 {
		t.Errorf("used = %d after three identical records, want 6000", used)
	}
}

// The exclusion predicate: a retry must not be refused by its own
// earlier draw.
func TestPostgres_UsedExcludesTheProposalBeingRetried(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "pg-t3")
	s := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := s.Issue(ctx, pgMandate("pg-t3", "mdt-1"), "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	for _, p := range []struct {
		id  string
		amt int64
	}{{"prop-a", 1000}, {"prop-b", 2000}} {
		if err := s.Record(ctx, mandate.Draw{MandateID: "mdt-1", TenantID: "pg-t3", ProposalID: p.id,
			QuantityKey: "sum(amount_cents)", Amount: p.amt, DrawnAt: base}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	all, err := s.Used(ctx, "pg-t3", "mdt-1", "sum(amount_cents)", base.Add(-time.Hour), "")
	if err != nil || all != 3000 {
		t.Fatalf("total used = %d err = %v, want 3000", all, err)
	}
	excl, err := s.Used(ctx, "pg-t3", "mdt-1", "sum(amount_cents)", base.Add(-time.Hour), "prop-b")
	if err != nil || excl != 1000 {
		t.Fatalf("used excluding prop-b = %d err = %v, want 1000", excl, err)
	}
}

// Draws before the window start must not count, or a daily limit would
// never reset.
func TestPostgres_UsedRespectsTheWindow(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "pg-t4")
	s := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := s.Issue(ctx, pgMandate("pg-t4", "mdt-1"), "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	for _, d := range []mandate.Draw{
		{ProposalID: "old", Amount: 9999, DrawnAt: base.Add(-48 * time.Hour)},
		{ProposalID: "new", Amount: 11, DrawnAt: base},
	} {
		d.MandateID, d.TenantID, d.QuantityKey = "mdt-1", "pg-t4", "sum(amount_cents)"
		if err := s.Record(ctx, d); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	used, err := s.Used(ctx, "pg-t4", "mdt-1", "sum(amount_cents)", base.Add(-time.Hour), "")
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != 11 {
		t.Errorf("used = %d, want 11 — a draw outside the window must not count", used)
	}
}

// LEAST on revoke: revoking twice keeps the earlier instant, so a retry
// under uncertainty cannot quietly extend authority.
func TestPostgres_RevokingTwiceKeepsTheEarlierInstant(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "pg-t5")
	s := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := s.Issue(ctx, pgMandate("pg-t5", "mdt-1"), "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	early, late := base.Add(-2*time.Hour), base.Add(-time.Minute)
	if err := s.Revoke(ctx, "pg-t5", "mdt-1", late); err != nil {
		t.Fatalf("Revoke late: %v", err)
	}
	if err := s.Revoke(ctx, "pg-t5", "mdt-1", early); err != nil {
		t.Fatalf("Revoke early: %v", err)
	}
	if err := s.Revoke(ctx, "pg-t5", "mdt-1", late); err != nil {
		t.Fatalf("Revoke late again: %v", err)
	}
	got, err := s.List(ctx, "pg-t5")
	if err != nil || len(got) != 1 {
		t.Fatalf("List: %v (%d rows)", err, len(got))
	}
	if got[0].RevokedAt == nil || !got[0].RevokedAt.UTC().Equal(early.UTC()) {
		t.Errorf("RevokedAt = %v, want the earliest %v", got[0].RevokedAt, early)
	}
}

// revoked_at is absent from the issue upsert's UPDATE list, so
// re-issuing cannot restore withdrawn authority.
func TestPostgres_ReIssuingDoesNotUnrevoke(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "pg-t6")
	s := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	m := pgMandate("pg-t6", "mdt-1")
	if err := s.Issue(ctx, m, "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := s.Revoke(ctx, "pg-t6", "mdt-1", base.Add(-time.Hour)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	m.Aggregates[0].Limit = 99999999 // a generous re-issue
	if err := s.Issue(ctx, m, "tester"); err != nil {
		t.Fatalf("re-Issue: %v", err)
	}
	got, _ := s.List(ctx, "pg-t6")
	if len(got) != 1 || got[0].RevokedAt == nil {
		t.Fatalf("re-issuing cleared the revocation: %+v", got)
	}
	if got[0].Aggregates[0].Limit != 99999999 {
		t.Errorf("the re-issue should still update the limit, got %d", got[0].Aggregates[0].Limit)
	}
	if got[0].Active(base) {
		t.Error("a re-issued but revoked mandate must not be active")
	}
}

// Every query carries tenant_id, so one tenant's usage is unreachable
// from another's even when they share a mandate id.
func TestPostgres_UsageIsTenantScoped(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "pg-t7")
	seedTenant(t, pool, "pg-t8")
	s := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	for _, tn := range []string{"pg-t7", "pg-t8"} {
		if err := s.Issue(ctx, pgMandate(tn, "shared-id"), "tester"); err != nil {
			t.Fatalf("Issue %s: %v", tn, err)
		}
	}
	if err := s.Record(ctx, mandate.Draw{MandateID: "shared-id", TenantID: "pg-t7",
		ProposalID: "p1", QuantityKey: "sum(amount_cents)", Amount: 5000, DrawnAt: base}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	other, err := s.Used(ctx, "pg-t8", "shared-id", "sum(amount_cents)", base.Add(-time.Hour), "")
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if other != 0 {
		t.Errorf("tenant pg-t8 sees %d of pg-t7's usage", other)
	}
}

func TestPostgres_RevokingAnUnknownMandateReportsNotFound(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "pg-t9")
	s := mandate.NewPostgresStore(pool)
	if err := s.Revoke(context.Background(), "pg-t9", "nope", base); !errors.Is(err, mandate.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// Deleting a tenant must take its mandates and draws with it.
func TestPostgres_DrawsCascadeWithTheTenant(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	seedTenant(t, pool, "pg-t10")
	s := mandate.NewPostgresStore(pool)
	if err := s.Issue(ctx, pgMandate("pg-t10", "mdt-1"), "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := s.Record(ctx, mandate.Draw{MandateID: "mdt-1", TenantID: "pg-t10",
		ProposalID: "p1", QuantityKey: "sum(amount_cents)", Amount: 1, DrawnAt: base}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM cloud_meta.tenants WHERE id = $1`, "pg-t10"); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM cloud_meta.mandate_draws WHERE tenant_id = $1`, "pg-t10").Scan(&n); err != nil {
		t.Fatalf("count draws: %v", err)
	}
	if n != 0 {
		t.Errorf("%d draws survived the tenant", n)
	}
}

const pgSpentTenant = "pg-spent"

// A spent proposal id on the Postgres path, where the draw upsert used to overwrite a
// spent id's draw with a fresh one.
func TestPostgres_SpentProposalIDCannotAuthorizeAgain(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, pgSpentTenant)
	for _, stmt := range []string{
		`DELETE FROM cloud_meta.mandate_draws WHERE tenant_id = $1`,
		`DELETE FROM cloud_meta.mandate_proposal_claims WHERE tenant_id = $1`,
	} {
		if _, err := pool.Exec(context.Background(), stmt, pgSpentTenant); err != nil {
			t.Fatal(err)
		}
	}
	testSpentProposalID(t, mandate.NewPostgresStore(pool))
}

// The same race on Postgres, where the claim is a primary-key insert in
// RecordAll's transaction.
func TestPostgres_ConcurrentSameIDAuthorizesOnce(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, pgSpentTenant)
	for _, stmt := range []string{
		`DELETE FROM cloud_meta.mandate_draws WHERE tenant_id = $1`,
		`DELETE FROM cloud_meta.mandate_proposal_claims WHERE tenant_id = $1`,
	} {
		if _, err := pool.Exec(context.Background(), stmt, pgSpentTenant); err != nil {
			t.Fatal(err)
		}
	}
	testConcurrentSameID(t, mandate.NewPostgresStore(pool))
}
