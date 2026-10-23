package mandate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// A grant can name the resources it applies to.

func scopedCard(id string, values ...string) mandate.Mandate {
	m := homeCard(id, 50000, 500000)
	m.Grants[0].Resources = &mandate.ResourceScope{Parameter: "invoice_id", Values: values}
	return m
}

func refundOn(invoice string, amount int64) mandate.Request {
	r := refund(amount)
	if invoice != "" {
		r.Resources = map[string]string{"invoice_id": invoice}
	}
	return r
}

func (r *rig) decideOn(proposalID, invoice string, amount int64) mandate.Decision {
	r.t.Helper()
	d, err := r.a.Authorize(context.Background(), tenantID, proposalID, refundOn(invoice, amount))
	if err != nil {
		r.t.Fatalf("authorize %s: %v", proposalID, err)
	}
	return d
}

// Invariant: a Card scoped to named resources authorizes the action only
// on those resources.
// Category: authority
// Attack: an agent holding a refund Card for invoice inv-1 refunds inv-9, or sends no invoice at all
// Refused: the call is outside the Card (resource_not_on_card), checked before amounts, never read as "not covered"
func TestResourceScope(t *testing.T) {
	m := scopedCard("card-a", "inv-1", "inv-2")
	for _, tc := range []struct {
		name    string
		invoice string
		want    mandate.Outcome
	}{
		{"named", "inv-2", mandate.OutcomeWithinLimit},
		{"another", "inv-9", mandate.OutcomeResourceOutside},
		{"case differs", "INV-1", mandate.OutcomeResourceOutside},
		{"none sent", "", mandate.OutcomeResourceOutside},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := evaluate(t, []mandate.Mandate{m}, refundOn(tc.invoice, 100), 0)
			if d.Outcome != tc.want || d.Allowed() != (tc.want == mandate.OutcomeWithinLimit) {
				t.Fatalf("outcome = %s, want %s (%s)", d.Outcome, tc.want, d.Reason)
			}
			if tc.want == mandate.OutcomeResourceOutside && d.MandateID != "card-a" {
				t.Fatalf("decision should name the Card: %+v", d)
			}
		})
	}
	if d := evaluate(t, []mandate.Mandate{m}, refundOn("inv-9", 100), 0); !strings.Contains(d.Reason, `invoice_id "inv-9"`) {
		t.Errorf("reason = %q", d.Reason)
	}
	// The parameter name matches without regard to case, as for amounts.
	upper := refund(100)
	upper.Resources = map[string]string{"invoice_id": "inv-1"}
	m.Grants[0].Resources.Parameter = "INVOICE_ID"
	if d := evaluate(t, []mandate.Mandate{m}, upper, 0); d.Outcome != mandate.OutcomeWithinLimit {
		t.Errorf("parameter case: %s", d.Outcome)
	}
}

// Invariant: an unscoped Card covering the same action never widens a
// scoped one. Cards are conjunctive, so the scope binds.
// Category: authority
// Attack: hold a scoped Card for inv-1 and a broad refund Card, and refund inv-9
// Refused: the scoped Card is breached whichever order the Cards are read in
func TestResourceScopeStacksWithUnscopedCard(t *testing.T) {
	scoped := scopedCard("card-scoped", "inv-1")
	broad := homeCard("card-broad", 50000, 500000)
	for _, order := range [][]mandate.Mandate{{scoped, broad}, {broad, scoped}} {
		d := evaluate(t, order, refundOn("inv-9", 100), 0)
		if d.Outcome != mandate.OutcomeResourceOutside || d.MandateID != "card-scoped" {
			t.Fatalf("outcome = %s on %s", d.Outcome, d.MandateID)
		}
	}
	// A second grant for the same action on one Card does not lift it either.
	twoGrants := scopedCard("card-two", "inv-1")
	twoGrants.Grants = append(twoGrants.Grants, mandate.Grant{Action: "REFUND_INVOICE"})
	if d := evaluate(t, []mandate.Mandate{twoGrants}, refundOn("inv-9", 100), 0); d.Outcome != mandate.OutcomeResourceOutside {
		t.Fatalf("an unscoped second grant widened the Card: %s", d.Outcome)
	}
}

// Invariant: a call on a resource outside the Card comes home to its
// owner, and an approval covers that exact resource only.
// Category: authority
// Attack: get inv-9 approved once, then redeem the same proposal id for inv-8, or a new proposal for inv-9
// Refused: the approval binds to the resource in the request fingerprint; a new proposal is held again
func TestResourceOutsideComesHomeAndApprovalBindsTheResource(t *testing.T) {
	r := newRig(t, scopedCard("card-a", "inv-1"))
	d := r.decideOn("p1", "inv-9", 100)
	if d.Outcome != mandate.OutcomeOwnerRequired || len(d.Breaches) != 1 {
		t.Fatalf("decision = %+v", d)
	}
	b := d.Breaches[0]
	if b.Outcome != mandate.OutcomeResourceOutside || b.Resource != "invoice_id" || b.Value != "inv-9" {
		t.Fatalf("breach = %+v", b)
	}
	if r.used("card-a") != 0 {
		t.Fatal("a held action drew on the card")
	}
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatalf("a resource breach can be approved once: %v", err)
	}
	if got := r.decideOn("p1", "inv-8", 100); got.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("approval redeemed for another resource: %+v", got)
	}
	if got := r.decideOn("p1", "inv-9", 100); got.Outcome != mandate.OutcomeWithinLimit || !got.Consumed {
		t.Fatalf("redeem: %+v", got)
	}
	if r.used("card-a") != 100 {
		t.Fatalf("an approved call draws on the Card: used %d", r.used("card-a"))
	}
	if got := r.decideOn("p1", "inv-8", 100); got.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("after redemption, another resource under the id: %+v", got)
	}
	if got := r.decideOn("p2", "inv-9", 100); got.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("approve once widened the Card: %+v", got)
	}
	ms, _ := r.store.ForSubject(context.Background(), tenantID, "agent://refund-agent")
	if got := ms[0].Grants[0].Resources.Values; len(got) != 1 || got[0] != "inv-1" {
		t.Fatalf("approve once changed the scope: %v", got)
	}
}

// A Card set to refuse refuses a resource outside it, with its own reason.
func TestResourceOutsideRefuseCard(t *testing.T) {
	m := scopedCard("card-a", "inv-1")
	m.OnExceed = mandate.OnExceedRefuse
	r := newRig(t, m)
	if d := r.decideOn("p1", "inv-9", 100); d.Outcome != mandate.OutcomeResourceOutside || d.Allowed() {
		t.Fatalf("decision = %+v", d)
	}
	es, _ := r.store.Exceptions(context.Background(), tenantID, "")
	if len(es) != 0 {
		t.Fatalf("a refuse Card held work: %+v", es)
	}
}

// An approval for an over-limit amount does not cover a resource the
// owner never saw: the resource is a new question.
func TestApprovedAmountDoesNotWaiveAResource(t *testing.T) {
	m := scopedCard("card-a", "inv-1")
	r := newRig(t, m)
	d := r.decideOn("p1", "inv-1", 150000) // over 50000 each, on a named invoice
	if len(d.Breaches) != 1 || d.Breaches[0].Outcome != mandate.OutcomePerActionExceeded {
		t.Fatalf("breaches = %+v", d.Breaches)
	}
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	if got := r.decideOn("p1", "inv-9", 150000); got.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("amount approval redeemed on another invoice: %+v", got)
	}
}

// Change Card adds the held call's resource to the scope, as a recorded
// revision, and the call then fits.
func TestChangeCardAddsTheResource(t *testing.T) {
	r := newRig(t, scopedCard("card-a", "inv-1"))
	d := r.decideOn("p1", "inv-9", 100)
	if _, err := r.store.ChangeCard(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	ms, _ := r.store.ForSubject(context.Background(), tenantID, "agent://refund-agent")
	if got := ms[0].Grants[0].Resources.Values; len(got) != 2 || got[1] != "inv-9" {
		t.Fatalf("scope after change: %v", got)
	}
	if got := r.decideOn("p1", "inv-9", 100); !got.Allowed() {
		t.Fatalf("re-decide after change: %+v", got)
	}
	revs, _ := r.store.Revisions(context.Background(), tenantID, "card-a")
	if len(revs) != 2 || revs[1].By != "owner@x" {
		t.Fatalf("the change must be a recorded revision: %+v", revs)
	}
}

func TestResourceScopeValidation(t *testing.T) {
	actions := []domain.ActionSpec{{
		Name: "REFUND_INVOICE", AllowedStates: []string{"PAID"},
		Parameters: []domain.ParameterSpec{{Name: "amount_cents", Type: "integer"}, {Name: "invoice_id", Type: "string"},
			{Name: "region", Type: "enum"}},
	}}
	if err := mandate.Validate(scopedCard("card-a", "inv-1"), actions); err != nil {
		t.Fatalf("a valid scope must validate: %v", err)
	}
	enum := scopedCard("card-a", "eu")
	enum.Grants[0].Resources.Parameter = "region"
	if err := mandate.Validate(enum, actions); err != nil {
		t.Fatalf("an enum parameter can be scoped: %v", err)
	}
	many := make([]string, mandate.MaxResourceValues+1)
	for i := range many {
		many[i] = "inv-" + strings.Repeat("x", i+1)
	}
	for name, mutate := range map[string]func(*mandate.ResourceScope){
		"undeclared parameter": func(s *mandate.ResourceScope) { s.Parameter = "nope" },
		"numeric parameter":    func(s *mandate.ResourceScope) { s.Parameter = "amount_cents" },
		"no values":            func(s *mandate.ResourceScope) { s.Values = nil },
		"empty value":          func(s *mandate.ResourceScope) { s.Values = []string{""} },
		"spaces":               func(s *mandate.ResourceScope) { s.Values = []string{" inv-1"} },
		"too long":             func(s *mandate.ResourceScope) { s.Values = []string{strings.Repeat("x", mandate.MaxResourceLength+1)} },
		"listed twice":         func(s *mandate.ResourceScope) { s.Values = []string{"inv-1", "inv-1"} },
		"too many":             func(s *mandate.ResourceScope) { s.Values = many },
	} {
		m := scopedCard("card-a", "inv-1")
		mutate(m.Grants[0].Resources)
		if err := mandate.Validate(m, actions); !errors.Is(err, mandate.ErrInvalidMandate) {
			t.Errorf("%s: err = %v, want ErrInvalidMandate", name, err)
		}
	}
}

// The scope lives on the grant's JSONB and the breach on the exception's:
// both round-trip through Postgres, and the approval binds the resource
// on the production transaction path.
func TestPostgres_ResourceScopeRoundTrip(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-rs")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()

	m := pgMandate("t-rs", "card-rs")
	m.OnExceed = mandate.OnExceedOwner
	m.Grants[0].Resources = &mandate.ResourceScope{Parameter: "invoice_id", Values: []string{"inv-1"}}
	if err := store.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	ms, err := store.ForSubject(ctx, "t-rs", "agent://a")
	if err != nil || len(ms) != 1 || ms[0].Grants[0].Resources == nil || ms[0].Grants[0].Resources.Values[0] != "inv-1" {
		t.Fatalf("scope did not round-trip: %+v %v", ms, err)
	}

	a := mandate.NewAuthorizer(store, nil, at(base))
	on := func(invoice string) mandate.Request {
		return mandate.Request{Subject: "agent://a", Domain: "billing", Environment: "prod",
			Action: "REFUND_INVOICE", EntityState: "PAID", Parameters: map[string]int64{"amount_cents": 100},
			Resources: map[string]string{"invoice_id": invoice}}
	}
	d, err := a.Authorize(ctx, "t-rs", "p-1", on("inv-9"))
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("authorize: %+v %v", d, err)
	}
	ex, found, err := store.GetException(ctx, "t-rs", "p-1")
	if err != nil || !found || len(ex.Breaches) != 1 || ex.Breaches[0].Resource != "invoice_id" ||
		ex.Breaches[0].Value != "inv-9" || ex.Request.Resources["invoice_id"] != "inv-9" {
		t.Fatalf("held exception: %+v %v", ex, err)
	}
	if _, err := store.ApproveOnce(ctx, "t-rs", d.ExceptionID, "owner", base); err != nil {
		t.Fatal(err)
	}
	if d, err := a.Authorize(ctx, "t-rs", "p-1", on("inv-8")); err != nil || d.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("another resource under the approved id: %+v %v", d, err)
	}
	if d, err := a.Authorize(ctx, "t-rs", "p-1", on("inv-9")); err != nil || !d.Allowed() || !d.Consumed {
		t.Fatalf("redeem: %+v %v", d, err)
	}
}

func unreadableOn(digest string) mandate.Request {
	r := refund(100)
	r.Unreadable = map[string]string{"invoice_id": digest}
	return r
}

// A scoped value too long to list, or sent twice, is
// refused and never held, and two such calls never share a fingerprint.
func TestUnreadableScopedValueIsRefusedNotHeld(t *testing.T) {
	r := newRig(t, scopedCard("card-a", "inv-1"))
	d, err := r.a.Authorize(context.Background(), tenantID, "p1", unreadableOn("sha256:aaa"))
	if err != nil || d.Outcome != mandate.OutcomeUnreadableResource || d.Allowed() {
		t.Fatalf("decision = %+v %v", d, err)
	}
	if es, _ := r.store.Exceptions(context.Background(), tenantID, ""); len(es) != 0 {
		t.Fatalf("an unreadable value was held for approval: %+v", es)
	}
	if mandate.RequestFingerprint(unreadableOn("sha256:aaa")) == mandate.RequestFingerprint(unreadableOn("sha256:bbb")) {
		t.Fatal("two different unreadable values share a fingerprint")
	}
	// Unscoped, an unreadable string changes nothing.
	plain := homeCard("card-b", 50000, 500000)
	if d := evaluate(t, []mandate.Mandate{plain}, unreadableOn("sha256:aaa"), 0); d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("unscoped: %s", d.Outcome)
	}
}

// A call held and redeemed with no resource cannot be
// replayed under its id with one; the consumed replay binds the exact
// request redeemed.
func TestRedeemedCallWithoutAResourceCannotGainOne(t *testing.T) {
	r := newRig(t, scopedCard("card-a", "inv-1"))
	d := r.decideOn("p1", "", 100)
	if d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("held: %+v", d)
	}
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	if got := r.decideOn("p1", "inv-9", 100); got.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("approved, then a resource added: %+v", got)
	}
	if got := r.decideOn("p1", "", 100); !got.Allowed() || !got.Consumed {
		t.Fatalf("redeem: %+v", got)
	}
	if got := r.decideOn("p1", "inv-9", 100); got.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("consumed, then a resource added: %+v", got)
	}
	if got := r.decideOn("p1", "", 100); !got.Allowed() || got.Consumed {
		t.Fatalf("identical replay: %+v", got)
	}
}

// Exceptions recorded before string parameters were in the fingerprint
// match as they did when the request has none; with some, only while
// still unanswered.
func TestLegacyExceptionMatching(t *testing.T) {
	plain, withResource := refund(100), refundOn("inv-1", 100)
	legacy := func(status mandate.ExceptionStatus) mandate.Exception {
		v2 := mandate.Exception{Fingerprint: mandate.RequestFingerprint(plain)}
		if !strings.HasPrefix(v2.Fingerprint, "v2:") {
			t.Fatalf("new fingerprints are versioned: %s", v2.Fingerprint)
		}
		// The pre-#1147 form: the same hash, unversioned, of the numeric fields.
		return mandate.Exception{Fingerprint: mandate.LegacyFingerprintForTest(plain), Status: status}
	}
	for _, s := range []mandate.ExceptionStatus{mandate.ExceptionHeld, mandate.ExceptionApproved, mandate.ExceptionConsumed, mandate.ExceptionRejected} {
		if !legacy(s).Matches(plain) {
			t.Errorf("%s legacy exception, numeric request: must match as before", s)
		}
		if got, want := legacy(s).Matches(withResource), s == mandate.ExceptionHeld; got != want {
			t.Errorf("%s legacy exception, request with a resource: matches = %v, want %v", s, got, want)
		}
	}
	v2 := mandate.Exception{Fingerprint: mandate.RequestFingerprint(plain), Status: mandate.ExceptionHeld}
	if v2.Matches(withResource) {
		t.Error("a v2 exception recorded without strings matched a request with one")
	}
}

// Change Card on a hold that is outside a scope and past
// a total adds both the resource and the room, on this Card or another.
func TestChangeCardOnResourceAndTotal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cards []mandate.Mandate
	}{
		{"one card", []mandate.Mandate{func() mandate.Mandate {
			m := scopedCard("card-a", "inv-1")
			m.Aggregates[0].Limit = 200
			return m
		}()}},
		{"scope and total on different cards", []mandate.Mandate{scopedCard("card-a", "inv-1"), func() mandate.Mandate {
			m := homeCard("card-b", 50000, 200)
			return m
		}()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.cards...)
			if d := r.decideOn("p0", "inv-1", 150); !d.Allowed() {
				t.Fatalf("p0: %+v", d)
			}
			d := r.decideOn("p1", "inv-9", 100)
			if d.Outcome != mandate.OutcomeOwnerRequired || len(d.Breaches) != 2 {
				t.Fatalf("held on both: %+v", d)
			}
			if _, err := r.store.ChangeCard(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
				t.Fatal(err)
			}
			ms, _ := r.store.ForSubject(context.Background(), tenantID, "agent://refund-agent")
			for _, m := range ms {
				if m.ID == "card-a" && len(m.Grants[0].Resources.Values) != 2 {
					t.Fatalf("scope after change: %v", m.Grants[0].Resources.Values)
				}
			}
			if got := r.decideOn("p1", "inv-9", 100); !got.Allowed() {
				t.Fatalf("re-decide after the change: %+v", got)
			}
		})
	}
}

// On the production store: an omitted resource cannot be
// gained after approval or redemption, an unreadable one is refused and
// never held, and Change Card on a resource-and-total hold applies both.
func TestPostgres_ResourceReviewCases(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-rs2")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()

	m := pgMandate("t-rs2", "card-rs2")
	m.OnExceed = mandate.OnExceedOwner
	m.Aggregates[0].Limit = 200
	m.Grants[0].Resources = &mandate.ResourceScope{Parameter: "invoice_id", Values: []string{"inv-1"}}
	if err := store.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	on := func(invoice string, amount int64) mandate.Request {
		r := mandate.Request{Subject: "agent://a", Domain: "billing", Environment: "prod",
			Action: "REFUND_INVOICE", EntityState: "PAID", Parameters: map[string]int64{"amount_cents": amount}}
		if invoice != "" {
			r.Resources = map[string]string{"invoice_id": invoice}
		}
		return r
	}
	decide := func(id string, r mandate.Request) mandate.Decision {
		t.Helper()
		d, err := a.Authorize(ctx, "t-rs2", id, r)
		if err != nil {
			t.Fatalf("authorize %s: %v", id, err)
		}
		return d
	}

	// Omitted resource: held, approved, redeemed; adding one later is a reuse.
	d := decide("p-none", on("", 10))
	if d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("held: %+v", d)
	}
	if _, err := store.ApproveOnce(ctx, "t-rs2", d.ExceptionID, "owner", base); err != nil {
		t.Fatal(err)
	}
	if got := decide("p-none", on("inv-9", 10)); got.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("approved, then a resource added: %+v", got)
	}
	if got := decide("p-none", on("", 10)); !got.Allowed() || !got.Consumed {
		t.Fatalf("redeem: %+v", got)
	}
	if got := decide("p-none", on("inv-9", 10)); got.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("consumed, then a resource added: %+v", got)
	}

	// Unreadable: refused, nothing held.
	long := on("", 10)
	long.Unreadable = map[string]string{"invoice_id": "sha256:aaa"}
	if got := decide("p-long", long); got.Outcome != mandate.OutcomeUnreadableResource {
		t.Fatalf("unreadable: %+v", got)
	}
	if _, found, _ := store.GetException(ctx, "t-rs2", "p-long"); found {
		t.Fatal("an unreadable value was held")
	}

	// Resource and total: 10 used above, 150 more, then 100 on inv-9.
	if got := decide("p-150", on("inv-1", 150)); !got.Allowed() {
		t.Fatalf("p-150: %+v", got)
	}
	d = decide("p-mixed", on("inv-9", 100))
	if d.Outcome != mandate.OutcomeOwnerRequired || len(d.Breaches) != 2 {
		t.Fatalf("held on both: %+v", d)
	}
	if _, err := store.ChangeCard(ctx, "t-rs2", d.ExceptionID, "owner", base); err != nil {
		t.Fatal(err)
	}
	ms, _ := store.ForSubject(ctx, "t-rs2", "agent://a")
	if len(ms) != 1 || len(ms[0].Grants[0].Resources.Values) != 2 || ms[0].Aggregates[0].Ceiling(ms[0], base) < 260 {
		t.Fatalf("after the change: %+v", ms)
	}
	if got := decide("p-mixed", on("inv-9", 100)); !got.Allowed() {
		t.Fatalf("re-decide after the change: %+v", got)
	}
}
