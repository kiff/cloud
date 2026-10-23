package mandate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// The work comes home (exceptions), against the in-memory store.

const tenantID = "t1"

var amountQ = mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}

// homeCard allows up to perAction each and total a day, and sends work
// outside it home to its owner.
func homeCard(id string, perAction, total int64) mandate.Mandate {
	m := spendMandate(total)
	m.ID = id
	m.OnExceed = mandate.OnExceedOwner
	m.Grants[0].PerAction = []mandate.PerActionLimit{{Quantity: amountQ, Max: perAction}}
	return m
}

type rig struct {
	t     *testing.T
	store *mandate.MemoryStore
	a     *mandate.Authorizer
	clock time.Time
}

func newRig(t *testing.T, cards ...mandate.Mandate) *rig {
	t.Helper()
	r := &rig{t: t, store: mandate.NewMemoryStore(), clock: base}
	r.store.SetClock(func() time.Time { return r.clock })
	for _, m := range cards {
		if err := r.store.Issue(context.Background(), m, "owner@x"); err != nil {
			t.Fatal(err)
		}
	}
	r.a = mandate.NewAuthorizer(r.store, nil, func() time.Time { return r.clock })
	return r
}

func (r *rig) decide(proposalID string, amount int64) mandate.Decision {
	r.t.Helper()
	d, err := r.a.Authorize(context.Background(), tenantID, proposalID, refund(amount))
	if err != nil {
		r.t.Fatalf("authorize %s: %v", proposalID, err)
	}
	return d
}

func (r *rig) used(card string) int64 {
	r.t.Helper()
	n, err := r.store.Used(context.Background(), tenantID, card, amountQ.Key(), base.Add(-48*time.Hour), "")
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) outside(card string) int64 {
	r.t.Helper()
	es, err := r.store.ExceptionEntries(context.Background(), tenantID, card, base.Add(-48*time.Hour))
	if err != nil {
		r.t.Fatal(err)
	}
	var n int64
	for _, e := range es {
		n += e.Amount
	}
	return n
}

// Invariant: an action outside a Card set to come home is held for the
// Card's owner and authorizes nothing until the owner answers.
// Category: authority
// Attack: propose a €1,500 refund against a Card allowing €500 each whose work comes home, and retry it until it goes through
// Refused: it is held as one owner exception and every retry returns owner_required with the same handle; nothing is drawn
func TestOutsideTheCardComesHome(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	d := r.decide("p1", 150000)
	if d.Outcome != mandate.OutcomeOwnerRequired || d.ExceptionID == "" || d.Allowed() {
		t.Fatalf("decision = %+v", d)
	}
	for i := 0; i < 3; i++ {
		again := r.decide("p1", 150000)
		if again.Outcome != mandate.OutcomeOwnerRequired || again.ExceptionID != d.ExceptionID {
			t.Fatalf("retry %d: %+v", i, again)
		}
	}
	es, _ := r.store.Exceptions(context.Background(), tenantID, "")
	if len(es) != 1 || es[0].Status != mandate.ExceptionHeld || len(es[0].Breaches) != 1 {
		t.Fatalf("exceptions = %+v", es)
	}
	if r.used("card-a") != 0 {
		t.Fatal("a held action drew on the card")
	}
}

// A Card set to refuse (every Card issued before the amendment) refuses
// as Cards first did: no exception.
func TestRefuseCardRefuses(t *testing.T) {
	m := homeCard("card-a", 50000, 500000)
	m.OnExceed = mandate.OnExceedRefuse
	r := newRig(t, m)
	if d := r.decide("p1", 150000); d.Outcome != mandate.OutcomePerActionExceeded {
		t.Fatalf("decision = %+v", d)
	}
	legacy := homeCard("card-b", 50000, 500000)
	legacy.OnExceed = ""
	r = newRig(t, legacy)
	if d := r.decide("p1", 150000); d.Outcome != mandate.OutcomePerActionExceeded {
		t.Fatalf("a card with no policy must refuse: %+v", d)
	}
	if es, _ := r.store.Exceptions(context.Background(), tenantID, ""); len(es) != 0 {
		t.Fatalf("refusal held an exception: %+v", es)
	}
}

// Every applicable Card binds; refuse takes precedence; the result does
// not depend on the order Cards are read in.
func TestOverlappingCards(t *testing.T) {
	home := homeCard("card-home", 50000, 500000)
	refuse := homeCard("card-refuse", 50000, 500000)
	refuse.OnExceed = mandate.OnExceedRefuse
	loose := homeCard("card-loose", 1000000, 5000000)
	home2 := homeCard("card-home2", 30000, 500000)

	for _, order := range [][]mandate.Mandate{{home, refuse}, {refuse, home}} {
		r := newRig(t, order...)
		if d := r.decide("p1", 150000); d.Outcome != mandate.OutcomePerActionExceeded || d.MandateID != "card-refuse" {
			t.Fatalf("refuse must take precedence: %+v", d)
		}
	}
	for _, order := range [][]mandate.Mandate{{home, home2}, {home2, home}} {
		r := newRig(t, order...)
		d := r.decide("p1", 150000)
		if d.Outcome != mandate.OutcomeOwnerRequired || len(d.Breaches) != 2 {
			t.Fatalf("one exception listing both cards: %+v", d)
		}
	}
	// Fits A, exceeds B: approving B's breach draws A normally.
	for _, order := range [][]mandate.Mandate{{loose, home}, {home, loose}} {
		r := newRig(t, order...)
		d := r.decide("p1", 150000)
		if d.Outcome != mandate.OutcomeOwnerRequired || len(d.Breaches) != 1 || d.Breaches[0].MandateID != "card-home" {
			t.Fatalf("only the exceeded card is listed: %+v", d)
		}
		if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
			t.Fatal(err)
		}
		if d := r.decide("p1", 150000); d.Outcome != mandate.OutcomeWithinLimit {
			t.Fatalf("redeem: %+v", d)
		}
		if r.used("card-loose") != 150000 || r.used("card-home") != 150000 || r.outside("card-home") != 150000 {
			t.Fatalf("both Cards draw and the waived threshold is recorded: loose used %d, home used %d, home outside %d",
				r.used("card-loose"), r.used("card-home"), r.outside("card-home"))
		}
	}
}

// Invariant: approve once authorizes exactly one action, the one the
// owner saw, and does not raise the Card. That action still counts in
// full toward the Card's totals.
// Category: authority
// Attack: after the owner approves one €1,500 refund once, reuse the approval for a different amount, or send the same proposal again hoping for a second authorization
// Refused: the approval binds to the request's fingerprint and is consumed once; an identical retry replays the same authorization, a changed one is refused, and the full amount is drawn from the Card
func TestApproveOnce(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	d := r.decide("p1", 150000)
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); !errors.Is(err, mandate.ErrExceptionState) {
		t.Fatalf("approving twice: %v", err)
	}
	// A different request under the same id is not what was approved.
	if d := r.decide("p1", 160000); d.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("altered payload: %+v", d)
	}
	ok := r.decide("p1", 150000)
	if ok.Outcome != mandate.OutcomeWithinLimit || ok.ExceptionID != d.ExceptionID {
		t.Fatalf("redeem: %+v", ok)
	}
	if r.used("card-a") != 150000 || r.outside("card-a") != 150000 {
		t.Fatalf("full budget charge and approval provenance must be recorded: used %d outside %d", r.used("card-a"), r.outside("card-a"))
	}
	// Lost response: the identical retry gets the same authorization,
	// with nothing consumed or recorded twice.
	for i := 0; i < 2; i++ {
		if again := r.decide("p1", 150000); again.Outcome != mandate.OutcomeWithinLimit {
			t.Fatalf("replay %d: %+v", i, again)
		}
	}
	if r.outside("card-a") != 150000 {
		t.Fatalf("replay recorded twice: %d", r.outside("card-a"))
	}
	if d := r.decide("p1", 160000); d.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("altered payload after redemption: %+v", d)
	}
	// The Card is unchanged: the next one like it comes home again.
	if d := r.decide("p2", 150000); d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("the card was raised by an approval: %+v", d)
	}
	m, _ := r.store.ForSubject(context.Background(), tenantID, "agent://refund-agent")
	if m[0].Grants[0].PerAction[0].Max != 50000 {
		t.Fatal("approve once changed the card")
	}
}

func TestReject(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	d := r.decide("p1", 150000)
	if _, err := r.store.Reject(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock, mandate.Refusal{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if again := r.decide("p1", 150000); again.Outcome != mandate.OutcomeOwnerRejected {
			t.Fatalf("after reject: %+v", again)
		}
	}
	if r.used("card-a") != 0 || r.outside("card-a") != 0 {
		t.Fatal("a rejected action drew or was recorded")
	}
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); !errors.Is(err, mandate.ErrExceptionState) {
		t.Fatalf("approving a rejected exception: %v", err)
	}
}

// A cumulative revision adds room for this window. It does not also
// waive the per-action review or permanently change the base ceiling.
func TestChangeCard(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 200000))
	if d := r.decide("p0", 40000); d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatal(d)
	}
	// 190000 is over 50000 per action and, after 40000, over 200000.
	d := r.decide("p1", 190000)
	if len(d.Breaches) != 2 {
		t.Fatalf("both limits breached: %+v", d.Breaches)
	}
	if _, err := r.store.ChangeCard(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	ms, _ := r.store.ForSubject(context.Background(), tenantID, "agent://refund-agent")
	if ms[0].Grants[0].PerAction[0].Max != 50000 || ms[0].Aggregates[0].Limit != 200000 || ms[0].Aggregates[0].Ceiling(ms[0], r.clock) != 230000 {
		t.Fatalf("card after change: per-action %d, total %d", ms[0].Grants[0].PerAction[0].Max, ms[0].Aggregates[0].Limit)
	}
	fresh := r.decide("p1", 190000)
	if fresh.Outcome != mandate.OutcomeOwnerRequired || len(fresh.Breaches) != 1 {
		t.Fatalf("per-action review after adding room: %+v", fresh)
	}
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, fresh.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	if got := r.decide("p1", 190000); !got.Allowed() {
		t.Fatalf("re-decide after approval: %+v", got)
	}
	if r.used("card-a") != 230000 {
		t.Fatalf("used %d, want exactly the new total", r.used("card-a"))
	}
	revs, _ := r.store.Revisions(context.Background(), tenantID, "card-a")
	if len(revs) != 3 || revs[1].By != "owner@x" || revs[2].By != "owner@x" {
		t.Fatalf("the change must be a recorded revision: %+v", revs)
	}
	es, _ := r.store.Exceptions(context.Background(), tenantID, "")
	if len(es) != 1 || es[0].Status != mandate.ExceptionConsumed || len(es[0].Breaches) != 1 {
		t.Fatalf("the room revision must still require per-action approval: %+v", es)
	}
	r.clock = ms[0].Aggregates[0].ExtraRoom[0].ExpiresAt
	if ms[0].Aggregates[0].Ceiling(ms[0], r.clock) != 200000 || r.used("card-a") != 230000 {
		t.Fatal("expiry must remove extra room without erasing usage")
	}
}

// Invariant: an approval not yet used stops authorizing when its Card is
// revoked, moved to another holder, or expires.
// Category: authority
// Attack: have the owner approve an action once, then revoke or move the card, and redeem the approval anyway
// Refused: revocation and moves invalidate open exceptions under the card's lock, and redemption rechecks the card under the same lock
func TestUnusedApprovalIsWithdrawn(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		mutate func(r *rig)
		reason string
	}{
		{"revoke", func(r *rig) {
			if err := r.store.Revoke(ctx, tenantID, "card-a", r.clock); err != nil {
				t.Fatal(err)
			}
		}, "card_revoked"},
		{"move", func(r *rig) {
			if _, err := r.store.Move(ctx, tenantID, "card-a", "agent://other", "owner@x", r.clock); err != nil {
				t.Fatal(err)
			}
		}, "holder_changed"},
		{"expire", func(r *rig) { r.clock = base.Add(25 * time.Hour) }, "card_expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, homeCard("card-a", 50000, 500000))
			d := r.decide("p1", 150000)
			if _, err := r.store.ApproveOnce(ctx, tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
				t.Fatal(err)
			}
			tc.mutate(r)
			if got := r.decide("p1", 150000); got.Outcome == mandate.OutcomeWithinLimit {
				t.Fatalf("a withdrawn approval authorized: %+v", got)
			}
			if r.outside("card-a") != 0 {
				t.Fatal("a withdrawn approval was recorded")
			}
			e, _ := r.store.Exception(ctx, tenantID, d.ExceptionID)
			if e.Status == mandate.ExceptionConsumed || e.Status == mandate.ExceptionApproved {
				t.Fatalf("exception still usable: %+v", e)
			}
			if tc.name != "expire" && (e.Status != mandate.ExceptionInvalidated || e.Reason != tc.reason) {
				t.Fatalf("exception = %s/%s, want invalidated/%s", e.Status, e.Reason, tc.reason)
			}
		})
	}
}

// An approval covers the Cards it listed. A Card breached since is a new
// question for the owner, so the action comes home again.
// Invariant: an approval cannot carry an action past a Card's total. If the
// total has no room when the approved action is retried, it waits and
// nothing is authorized.
// Category: accounting
// Attack: approve one refund over the per-action limit, spend the Card's total down, then retry the approved refund
// Refused: the retry is answered approved_no_room: the approval is kept, nothing is drawn or authorized
func TestNewCumulativeBreachAfterApprovalWaitsForRoom(t *testing.T) {
	a := homeCard("card-a", 50000, 5000000)
	b := homeCard("card-b", 1000000, 200000)
	r := newRig(t, a, b)
	d := r.decide("p1", 150000) // over a's per-action only
	if len(d.Breaches) != 1 || d.Breaches[0].MandateID != "card-a" {
		t.Fatalf("breaches = %+v", d.Breaches)
	}
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	// Spend b down so p1 no longer fits it.
	bOnly := refund(40000)
	if _, err := r.a.Authorize(context.Background(), tenantID, "p-b", bOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := r.a.Authorize(context.Background(), tenantID, "p-b2", bOnly); err != nil {
		t.Fatal(err)
	}
	got := r.decide("p1", 150000)
	if got.Outcome != mandate.OutcomeApprovedNoRoom || len(got.Breaches) != 2 {
		t.Fatalf("a cumulative breach preserves consent but cannot be waived: %+v", got)
	}
}

// Limits changed while an action is held: the next decision re-evaluates
// and the action draws if it now fits.
func TestHeldActionFitsAfterAReissue(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	d := r.decide("p1", 150000)
	if err := r.store.Issue(context.Background(), homeCard("card-a", 200000, 500000), "owner@x"); err != nil {
		t.Fatal(err)
	}
	if got := r.decide("p1", 150000); got.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("after the owner raised the limit: %+v", got)
	}
	e, _ := r.store.Exception(context.Background(), tenantID, d.ExceptionID)
	if e.Status != mandate.ExceptionChanged || r.used("card-a") != 150000 {
		t.Fatalf("exception %s, used %d", e.Status, r.used("card-a"))
	}
}

// Observe mode reports owner_required without holding anything: the
// caller executes anyway, so there is nothing to answer.
func TestObserveModeDoesNotHold(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	req := refund(150000)
	req.Observe = true
	d, err := r.a.Authorize(context.Background(), tenantID, "p1", req)
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("observe: %+v %v", d, err)
	}
	if es, _ := r.store.Exceptions(context.Background(), tenantID, ""); len(es) != 0 {
		t.Fatalf("observe mode held an exception: %+v", es)
	}
}

// A redemption whose ledger write fails authorizes nothing and consumes
// nothing: the approval is still there for the retry.
func TestRedemptionWriteFailure(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	d := r.decide("p1", 150000)
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	r.store.FailRecordWith(errors.New("disk full"))
	if _, err := r.a.Authorize(context.Background(), tenantID, "p1", refund(150000)); !errors.Is(err, mandate.ErrUsageUnavailable) {
		t.Fatalf("want fail closed, got %v", err)
	}
	r.store.FailRecordWith(nil)
	if got := r.decide("p1", 150000); got.Outcome != mandate.OutcomeWithinLimit || r.outside("card-a") != 150000 {
		t.Fatalf("retry after the failure: %+v outside %d", got, r.outside("card-a"))
	}
}

func TestExceptionReasonNamesEveryCard(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000), homeCard("card-b", 30000, 500000))
	d := r.decide("p1", 150000)
	if !strings.Contains(d.Reason, "card-a") || !strings.Contains(d.Reason, "card-b") {
		t.Fatalf("reason = %q", d.Reason)
	}
}
