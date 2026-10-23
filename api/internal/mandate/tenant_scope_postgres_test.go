package mandate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// Cards live in cloud_meta, a schema shared by every tenant, so the only
// boundary is the tenant_id each query carries. Another tenant must not
// see, revoke, archive, re-hold or read the history of a Card, even when
// it knows the Card's id and holder.
func TestPostgres_CardsAreTenantScoped(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-iso-own")
	seedTenant(t, pool, "t-iso-other")
	s := mandate.NewPostgresStore(pool)
	ctx := context.Background()

	card := pgMandate("t-iso-own", "card-iso")
	if err := s.Issue(ctx, card, "owner"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := s.Record(ctx, mandate.Draw{
		TenantID: "t-iso-own", MandateID: "card-iso", ProposalID: "p-iso",
		Subject: "agent://a", QuantityKey: "sum(amount_cents)", Amount: 100, DrawnAt: base,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if ms, err := s.List(ctx, "t-iso-other"); err != nil || len(ms) != 0 {
		t.Fatalf("List across tenants = %d cards, %v", len(ms), err)
	}
	if ms, err := s.ForSubject(ctx, "t-iso-other", card.Subject); err != nil || len(ms) != 0 {
		t.Fatalf("ForSubject across tenants = %d cards, %v", len(ms), err)
	}
	if rs, err := s.Revisions(ctx, "t-iso-other", "card-iso"); err != nil || len(rs) != 0 {
		t.Fatalf("Revisions across tenants = %d, %v", len(rs), err)
	}
	if ds, err := s.Draws(ctx, "t-iso-other", "card-iso", base.Add(-time.Hour)); err != nil || len(ds) != 0 {
		t.Fatalf("Draws across tenants = %d, %v", len(ds), err)
	}
	if n, err := s.Used(ctx, "t-iso-other", "card-iso", "sum(amount_cents)", base.Add(-time.Hour), ""); err != nil || n != 0 {
		t.Fatalf("Used across tenants = %d, %v", n, err)
	}
	if err := s.Revoke(ctx, "t-iso-other", "card-iso", base); !errors.Is(err, mandate.ErrNotFound) {
		t.Fatalf("Revoke across tenants: %v", err)
	}
	if err := s.Archive(ctx, "t-iso-other", "card-iso", base); !errors.Is(err, mandate.ErrNotFound) {
		t.Fatalf("Archive across tenants: %v", err)
	}
	if _, err := s.Move(ctx, "t-iso-other", "card-iso", "agent://intruder", "x", base); err == nil {
		t.Fatal("Move across tenants succeeded")
	}

	// The owner's Card is untouched: still active, same holder, one draw.
	ms, err := s.ForSubject(ctx, "t-iso-own", card.Subject)
	if err != nil || len(ms) != 1 {
		t.Fatalf("owner lost the Card: %d, %v", len(ms), err)
	}
	if !ms[0].Active(base) {
		t.Fatal("the owner's Card was revoked from another tenant")
	}
	if ms, _ := s.ForSubject(ctx, "t-iso-own", "agent://intruder"); len(ms) != 0 {
		t.Fatal("another tenant moved the owner's Card")
	}
	if n, err := s.Used(ctx, "t-iso-own", "card-iso", "sum(amount_cents)", base.Add(-time.Hour), ""); err != nil || n != 100 {
		t.Fatalf("owner's usage = %d, %v; want 100", n, err)
	}
}
