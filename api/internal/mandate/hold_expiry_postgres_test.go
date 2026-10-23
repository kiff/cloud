package mandate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// Against Postgres: a hold's expiry is stored when it is first
// held, a late answer is refused, the first retry after it records the
// expiry, and a Card's hold expiry round-trips and survives a reissue
// that does not name one.
func TestPostgres_HoldExpiry(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-expiry")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	card := pgHomeCard("t-expiry", "card-1", 50000, 500000)
	card.HoldExpiry = 30 * time.Minute
	if err := store.Issue(ctx, card, "owner"); err != nil {
		t.Fatal(err)
	}

	now := base
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	held, err := a.Authorize(ctx, "t-expiry", "p-1", pgRefund(150000))
	if err != nil || held.Outcome != mandate.OutcomeOwnerRequired || !held.ExceptionExpiresAt.Equal(base.Add(30*time.Minute)) {
		t.Fatalf("held = %+v, %v", held, err)
	}
	e, err := store.Exception(ctx, "t-expiry", held.ExceptionID)
	if err != nil || e.ExpiresAt == nil || !e.ExpiresAt.Equal(base.Add(30*time.Minute)) || e.Status != mandate.ExceptionHeld {
		t.Fatalf("stored = %+v, %v", e, err)
	}

	now = base.Add(31 * time.Minute)
	if e.StatusAt(now) != mandate.ExceptionExpired {
		t.Fatalf("status at %v = %s", now, e.StatusAt(now))
	}
	if _, err := store.ApproveOnce(ctx, "t-expiry", held.ExceptionID, "owner", now); !errors.Is(err, mandate.ErrExceptionExpired) {
		t.Fatalf("late approval: %v", err)
	}
	if _, err := store.ChangeCard(ctx, "t-expiry", held.ExceptionID, "owner", now); !errors.Is(err, mandate.ErrExceptionExpired) {
		t.Fatalf("late change card: %v", err)
	}
	d, err := a.Authorize(ctx, "t-expiry", "p-1", pgRefund(150000))
	if err != nil || d.Outcome != mandate.OutcomeOwnerExpired {
		t.Fatalf("retry after expiry = %+v, %v", d, err)
	}
	e, _ = store.Exception(ctx, "t-expiry", held.ExceptionID)
	if e.Status != mandate.ExceptionExpired || e.ResolvedAt == nil || !e.ResolvedAt.Equal(base.Add(30*time.Minute)) {
		t.Fatalf("expiry not recorded: %+v", e)
	}
	if outsideTotal(t, store, "t-expiry", "card-1") != 0 {
		t.Fatal("an expired hold authorized something")
	}

	// The Card's hold expiry round-trips, and a reissue that names none
	// keeps it.
	re := pgHomeCard("t-expiry", "card-1", 60000, 500000)
	re.HoldExpiry = mandate.HoldExpiryKeep
	if err := store.Issue(ctx, re, "old-client"); err != nil {
		t.Fatal(err)
	}
	ms, err := store.List(ctx, "t-expiry")
	if err != nil || len(ms) != 1 || ms[0].HoldFor() != 30*time.Minute || ms[0].Grants[0].PerAction[0].Max != 60000 {
		t.Fatalf("after reissue: %+v, %v", ms, err)
	}
	fresh := pgHomeCard("t-expiry", "card-2", 1, 1)
	fresh.HoldExpiry = mandate.HoldExpiryKeep
	if err := store.Issue(ctx, fresh, "owner"); err != nil {
		t.Fatal(err)
	}
	ms, _ = store.List(ctx, "t-expiry")
	for _, m := range ms {
		if m.ID == "card-2" && (m.HoldExpiry != 0 || m.HoldFor() != mandate.DefaultHoldExpiry) {
			t.Fatalf("new Card without a hold expiry: %v", m.HoldExpiry)
		}
	}
}
