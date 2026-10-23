package mandate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

func externalFixture(t *testing.T) (*rig, mandate.ApproverGrant, mandate.Exception, mandate.ExternalReview) {
	t.Helper()
	r := newRig(t, homeCard("card", 50, 200))
	r.store.SetApproverPolicy(func(context.Context, string, string) error { return nil })
	d := r.decide("external", 75)
	if d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatal(d)
	}
	e, err := r.store.Exception(context.Background(), tenantID, d.ExceptionID)
	if err != nil {
		t.Fatal(err)
	}
	g := mandate.ApproverGrant{ID: "grant", TenantID: tenantID, Name: "night-shift", Principal: "night-shift", CardID: "card", Worker: e.Subject, Action: e.Action, Parameter: "amount_cents", Max: 100, CreatedBy: "admin", CreatedAt: r.clock, ExpiresAt: r.clock.Add(time.Hour), KeyHash: "fixture-hash"}
	if err = r.store.CreateApproverGrant(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	m := homeCard("card", 50, 200)
	review := mandate.ExternalReview{State: mandate.ReviewState(e), Card: mandate.ReviewCard(m), Answer: mandate.ExternalAnswer{GrantID: g.ID, Principal: g.Principal, Decision: "approve", ReasonCode: "fixture_approved", ReviewToken: "bound-review"}}
	// Issue fills holder/default fields; use that exact stored Card revision.
	cards, _ := r.store.ForSubject(context.Background(), tenantID, e.Subject)
	review.Card = mandate.ReviewCard(cards[0])
	return r, g, e, review
}

func TestExternalApproverChargesOnceAndReplays(t *testing.T) {
	r, g, e, review := externalFixture(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := r.store.AnswerExternal(ctx, tenantID, e.ID, g.ID, review, r.clock); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if d := r.decide("external", 75); !d.Allowed() {
			t.Fatal(d)
		}
	}
	if n := r.used("card"); n != 75 {
		t.Fatalf("used %d", n)
	}
	got, err := r.store.AnswerExternal(ctx, tenantID, e.ID, g.ID, review, r.clock)
	if err != nil || got.Status != mandate.ExceptionConsumed || got.ExternalAnswer.ReasonCode != "fixture_approved" {
		t.Fatalf("replay %+v %v", got, err)
	}
}

// Invariant: an external approver's answer cannot go past its grant. Over
// its per-call ceiling, outside its Card, past a total, after its grant is
// withdrawn or expires, after the Card changes, or for its own call, the
// answer is refused and the hold stays as it was.
// Category: authority
// Attack: have a connected approver service answer a held call outside what its grant covers
// Refused: the answer is refused; the call stays held with its deadline unchanged, and nothing is drawn
func TestExternalApproverBoundaries(t *testing.T) {
	for _, name := range []string{"stale", "ceiling", "scope", "withdraw", "expired", "defer", "record_failure", "cumulative", "changed_card", "self"} {
		t.Run(name, func(t *testing.T) {
			r, g, e, review := externalFixture(t)
			ctx := context.Background()
			wantErr := true
			switch name {
			case "stale":
				review.State = "old"
			case "ceiling":
				e.Request.Parameters["amount_cents"] = 101
				r.store.SaveException(ctx, e)
			case "scope":
				e.Breaches[0].MandateID = "another-card"
				r.store.SaveException(ctx, e)
			case "withdraw":
				r.store.RevokeApproverGrant(ctx, tenantID, g.ID, r.clock)
			case "expired":
				r.clock = r.clock.Add(2 * time.Hour)
			case "defer":
				review.Answer.Decision = "defer"
				wantErr = false
			case "record_failure":
				r.store.FailRecordWith(errors.New("write unavailable"))
			case "cumulative":
				e.Breaches[0].Outcome = mandate.OutcomeLimitReached
				r.store.SaveException(ctx, e)
				review.State = mandate.ReviewState(e)
			case "changed_card":
				r.store.Issue(ctx, homeCard("card", 45, 200), "admin")
			case "self":
				review.Answer.Principal = e.Subject
			}
			_, err := r.store.AnswerExternal(ctx, tenantID, e.ID, g.ID, review, r.clock)
			if (err != nil) != wantErr {
				t.Fatalf("error %v", err)
			}
			stored, _ := r.store.Exception(ctx, tenantID, e.ID)
			if stored.Status != mandate.ExceptionHeld {
				t.Fatalf("unexpected answer %+v", stored)
			}
			if stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(*e.ExpiresAt) {
				t.Fatal("deadline changed")
			}
			if r.used("card") != 0 {
				t.Fatal("answer spent budget")
			}
		})
	}
}

func TestExternalApproverWithdrawalRequiresFreshReview(t *testing.T) {
	r, g, e, review := externalFixture(t)
	ctx := context.Background()
	if _, err := r.store.AnswerExternal(ctx, tenantID, e.ID, g.ID, review, r.clock); err != nil {
		t.Fatal(err)
	}
	if err := r.store.RevokeApproverGrant(ctx, tenantID, g.ID, r.clock); err != nil {
		t.Fatal(err)
	}
	projection, err := mandate.ReadException(ctx, r.store, e, r.clock)
	if err != nil || projection.Status != mandate.ExceptionHeld || len(projection.Exception.PriorApprovals) != 1 {
		t.Fatalf("withdrawal projection %+v %v", projection, err)
	}
	stored, _ := r.store.Exception(ctx, tenantID, e.ID)
	if stored.Status != mandate.ExceptionApproved {
		t.Fatal("projection mutated the stored approval")
	}
	if d := r.decide("external", 75); d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatal(d)
	}
	got, _ := r.store.Exception(ctx, tenantID, e.ID)
	if len(got.PriorApprovals) != 1 || got.PriorApprovals[0].ExternalAnswer == nil || got.ExternalAnswer != nil {
		t.Fatalf("provenance %+v", got)
	}
	if !got.ExpiresAt.Equal(*e.ExpiresAt) || r.used("card") != 0 {
		t.Fatal("withdrawal changed deadline or spent")
	}
}

func TestExternalApproverPostgres(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tenant := "pg-external-approver"
	seedTenant(t, pool, tenant)
	if err := membership.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenant_memberships(id,tenant_id,subject,role,status) VALUES('external-owner',$1,'admin','admin','active')`, tenant); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM cloud_meta.approver_grants WHERE tenant_id=$1`, tenant)
		pool.Exec(ctx, `DELETE FROM cloud_meta.tenant_memberships WHERE tenant_id=$1`, tenant)
	})
	now := time.Now().UTC()
	s := mandate.NewPostgresStore(pool)
	m := homeCard("card", 50, 200)
	m.TenantID = tenant
	m.ValidFrom = now.Add(-time.Hour)
	m.ValidUntil = now.Add(time.Hour)
	if err := s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, func() time.Time { return now })
	req := req(m.Subject, m.Grants[0].Action, "PAID", map[string]int64{"amount_cents": 75})
	d, err := a.Authorize(ctx, tenant, "external", req)
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold %+v %v", d, err)
	}
	g := mandate.ApproverGrant{ID: "grant", TenantID: tenant, CardID: m.ID, Worker: m.Subject, Action: m.Grants[0].Action, Parameter: "amount_cents", Max: 100, Name: "night-shift", Principal: "night-shift", CreatedBy: "admin", CreatedAt: now, ExpiresAt: now.Add(time.Hour), KeyHash: "pg-external-fixture"}
	if err = s.CreateApproverGrant(ctx, g); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Exception(ctx, tenant, d.ExceptionID)
	cards, _ := s.ForSubject(ctx, tenant, m.Subject)
	review := mandate.ExternalReview{State: mandate.ReviewState(e), Card: mandate.ReviewCard(cards[0]), Answer: mandate.ExternalAnswer{GrantID: g.ID, Principal: g.Principal, Decision: "approve", ReasonCode: "fixture", ReviewToken: "bound"}}
	for i := 0; i < 2; i++ {
		if _, err = s.AnswerExternal(ctx, tenant, e.ID, g.ID, review, now); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Exception(ctx, tenant, e.ID)
	if got.ExternalAnswer == nil || got.ExternalAnswer.ReasonCode != "fixture" {
		t.Fatal("attribution not persisted")
	}
	if err = s.RevokeApproverGrant(ctx, tenant, g.ID, now); err != nil {
		t.Fatal(err)
	}
	projection, err := mandate.ReadException(ctx, s, e, now)
	if err != nil || projection.Status != mandate.ExceptionHeld || len(projection.Exception.PriorApprovals) != 1 {
		t.Fatalf("withdrawal snapshot %+v %v", projection, err)
	}
	d, err = a.Authorize(ctx, tenant, "external", req)
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("withdraw %+v %v", d, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO cloud_meta.tenant_governance(tenant_id,separation_of_duties) VALUES($1,true) ON CONFLICT(tenant_id) DO UPDATE SET separation_of_duties=true`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproverByHash(ctx, g.KeyHash); !errors.Is(err, mandate.ErrApproverGrant) {
		t.Fatalf("strict human-only policy: %v", err)
	}
}
