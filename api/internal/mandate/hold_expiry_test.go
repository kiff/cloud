package mandate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// A held action never runs on silence. Against the in-memory
// store; the Postgres store is checked in hold_expiry_postgres_test.go.

// Invariant: Work held for a Card's owner is refused once its hold expires unanswered, and stays refused.
// Category: authority
// Attack: let a €1,500 refund held for the owner sit past its hold expiry, then retry it and have the owner approve it late
// Refused: every retry is refused as owner_expired, the late approval is refused, and nothing is drawn
func TestHeldWorkExpires(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	held := r.decide("p1", 150000)
	if held.Outcome != mandate.OutcomeOwnerRequired || held.ExceptionExpiresAt == nil ||
		!held.ExceptionExpiresAt.Equal(base.Add(mandate.DefaultHoldExpiry)) {
		t.Fatalf("held = %+v", held)
	}

	r.clock = base.Add(mandate.DefaultHoldExpiry)
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, held.ExceptionID, "owner@x", r.clock); !errors.Is(err, mandate.ErrExceptionExpired) {
		t.Fatalf("late approval: %v", err)
	}
	for i := 0; i < 2; i++ {
		d := r.decide("p1", 150000)
		if d.Outcome != mandate.OutcomeOwnerExpired || d.Allowed() || d.ExceptionID != held.ExceptionID {
			t.Fatalf("retry %d after expiry = %+v", i, d)
		}
	}
	e, err := r.store.Exception(context.Background(), tenantID, held.ExceptionID)
	if err != nil || e.Status != mandate.ExceptionExpired || e.ResolvedAt == nil || !e.ResolvedAt.Equal(base.Add(mandate.DefaultHoldExpiry)) {
		t.Fatalf("stored exception = %+v, %v", e, err)
	}
	if _, err := r.store.Reject(context.Background(), tenantID, held.ExceptionID, "owner@x", r.clock, mandate.Refusal{}); !errors.Is(err, mandate.ErrExceptionExpired) {
		t.Fatalf("reject after expiry: %v", err)
	}
	if r.used("card-a") != 0 || r.outside("card-a") != 0 {
		t.Fatalf("drawn %d, outside %d", r.used("card-a"), r.outside("card-a"))
	}

	// A new attempt is a new proposal, held anew with its own expiry.
	again := r.decide("p2", 150000)
	if again.Outcome != mandate.OutcomeOwnerRequired || !again.ExceptionExpiresAt.Equal(r.clock.Add(mandate.DefaultHoldExpiry)) {
		t.Fatalf("new attempt = %+v", again)
	}
}

// Retrying a held action never buys it more time.
func TestRetryDoesNotExtendTheHold(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	first := r.decide("p1", 150000)
	r.clock = base.Add(9 * time.Minute)
	retry := r.decide("p1", 150000)
	if retry.Outcome != mandate.OutcomeOwnerRequired || !retry.ExceptionExpiresAt.Equal(*first.ExceptionExpiresAt) {
		t.Fatalf("retry moved the expiry: %v -> %v", first.ExceptionExpiresAt, retry.ExceptionExpiresAt)
	}
	r.clock = base.Add(10 * time.Minute)
	if d := r.decide("p1", 150000); d.Outcome != mandate.OutcomeOwnerExpired {
		t.Fatalf("after the original expiry = %+v", d)
	}
}

// An answer given in time stands: approve once is collected after the
// hold's expiry has passed.
func TestApprovalInTimeOutlivesTheHold(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	held := r.decide("p1", 150000)
	r.clock = base.Add(5 * time.Minute)
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, held.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	r.clock = base.Add(time.Hour)
	if d := r.decide("p1", 150000); d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("approved, collected after the expiry = %+v", d)
	}
	if r.outside("card-a") != 150000 {
		t.Fatalf("outside = %d", r.outside("card-a"))
	}
}

// A Card's own hold expiry applies; with several Cards the shortest wins.
func TestCardHoldExpiry(t *testing.T) {
	long := homeCard("card-long", 50000, 500000)
	long.HoldExpiry = 24 * time.Hour
	r := newRig(t, long)
	if d := r.decide("p1", 150000); !d.ExceptionExpiresAt.Equal(base.Add(24 * time.Hour)) {
		t.Fatalf("expiry = %v", d.ExceptionExpiresAt)
	}
	r.clock = base.Add(23 * time.Hour)
	if d := r.decide("p1", 150000); d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("still held = %+v", d)
	}

	short := homeCard("card-short", 50000, 500000)
	short.HoldExpiry = 2 * time.Minute
	r2 := newRig(t, long, short)
	if d := r2.decide("p1", 150000); !d.ExceptionExpiresAt.Equal(base.Add(2 * time.Minute)) {
		t.Fatalf("two Cards: expiry = %v", d.ExceptionExpiresAt)
	}
}

// A reissue that names no hold expiry keeps the Card's; a new Card gets
// the default.
func TestReissueKeepsHoldExpiry(t *testing.T) {
	c := homeCard("card-a", 50000, 500000)
	c.HoldExpiry = time.Hour
	r := newRig(t, c)
	re := homeCard("card-a", 60000, 500000)
	re.HoldExpiry = mandate.HoldExpiryKeep
	fresh := homeCard("card-b", 1, 1)
	fresh.HoldExpiry = mandate.HoldExpiryKeep
	for _, m := range []mandate.Mandate{re, fresh} {
		if err := r.store.Issue(context.Background(), m, "owner@x"); err != nil {
			t.Fatal(err)
		}
	}
	ms, _ := r.store.List(context.Background(), tenantID)
	got := map[string]time.Duration{}
	for _, m := range ms {
		got[m.ID] = m.HoldFor()
	}
	if got["card-a"] != time.Hour || got["card-b"] != mandate.DefaultHoldExpiry {
		t.Fatalf("hold expiry = %v", got)
	}
}

func TestHoldExpiryBounds(t *testing.T) {
	for _, h := range []time.Duration{30 * time.Second, 8 * 24 * time.Hour} {
		m := homeCard("card-a", 1, 1)
		m.HoldExpiry = h
		if err := mandate.Validate(m, nil); !errors.Is(err, mandate.ErrInvalidMandate) {
			t.Errorf("%s accepted: %v", h, err)
		}
	}
}

// StatusAt reads a held exception past its expiry as expired before any
// retry has written that down; one without an expiry never expires.
func TestStatusAt(t *testing.T) {
	at := base.Add(time.Minute)
	e := mandate.Exception{Status: mandate.ExceptionHeld, ExpiresAt: &at}
	if e.StatusAt(base) != mandate.ExceptionHeld || e.StatusAt(at) != mandate.ExceptionExpired {
		t.Fatal("held exception past expiry")
	}
	e.Status = mandate.ExceptionApproved
	if e.StatusAt(at.Add(time.Hour)) != mandate.ExceptionApproved {
		t.Fatal("an answered exception expired")
	}
	old := mandate.Exception{Status: mandate.ExceptionHeld}
	if old.StatusAt(base.Add(365*24*time.Hour)) != mandate.ExceptionHeld {
		t.Fatal("an exception held before expiry existed expired")
	}
}

// The owner is told once: only the decision that first holds work says
// so, never a retry of it; a new attempt is a new hold, and is told.
func TestNewlyHeldOnlyOnce(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	if d := r.decide("p1", 150000); !d.NewlyHeld {
		t.Fatalf("first hold not marked new: %+v", d)
	}
	r.clock = base.Add(time.Minute)
	if d := r.decide("p1", 150000); d.Outcome != mandate.OutcomeOwnerRequired || d.NewlyHeld {
		t.Fatalf("retry marked new: %+v", d)
	}
	if d := r.decide("p2", 150000); !d.NewlyHeld {
		t.Fatalf("a new proposal's hold not marked new: %+v", d)
	}
}
