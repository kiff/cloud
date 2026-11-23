package mandate_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// Which proposals a Card gave its room back for, read by proposal: only a call
// with a release is named, never one that merely finished, and never across
// tenants. The apps say "doesn't count toward today's limit" only for
// these.
func testReleasedProposals(t *testing.T, s mandate.Store, tenant, other string) {
	t.Helper()
	ctx := context.Background()
	m := homeCard("released-lookup", 50000, 500000)
	m.TenantID = tenant
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	for _, id := range []string{"op-released", "op-counted"} {
		if d, err := a.Authorize(ctx, tenant, id, refund(100)); err != nil || !d.Allowed() {
			t.Fatal(id, d, err)
		}
	}
	rs := s.(mandate.EffectReleaseStore)
	if _, err := rs.ReleaseEffects(ctx, effectEvidence(tenant, "op-released", base.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	r := s.(mandate.ReleasedProposalsReader)
	got, err := r.ReleasedProposals(ctx, tenant, []string{"op-released", "op-counted", "op-unknown"})
	if err != nil || len(got) != 1 || !got["op-released"] {
		t.Fatalf("released: %v %v", got, err)
	}
	if got, err := r.ReleasedProposals(ctx, other, []string{"op-released"}); err != nil || len(got) != 0 {
		t.Fatalf("another tenant sees %v %v", got, err)
	}
	if got, err := r.ReleasedProposals(ctx, tenant, nil); err != nil || len(got) != 0 {
		t.Fatalf("no ids: %v %v", got, err)
	}
}

func TestReleasedProposalsNamesOnlyReleasedCalls(t *testing.T) {
	testReleasedProposals(t, mandate.NewMemoryStore(), tenantID, "tnt_other")
}

func TestPostgres_ReleasedProposalsNamesOnlyReleasedCalls(t *testing.T) {
	p := requirePostgres(t)
	seedTenant(t, p, "released-lookup")
	seedTenant(t, p, "released-lookup-other")
	testReleasedProposals(t, mandate.NewPostgresStore(p), "released-lookup", "released-lookup-other")
}
