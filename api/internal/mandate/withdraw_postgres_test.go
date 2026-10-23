package mandate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// On Postgres: an agent withdraws its own held call (again: unchanged;
// another agent: not found; once answered: refused), and the expiry sweep
// expires each unanswered call once.
func TestPostgres_WithdrawAndExpireHeld(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-wd")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, pgHomeCard("t-wd", "card-home", 50000, 500000), "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	hold := func(p string) mandate.Exception {
		t.Helper()
		d, err := a.Authorize(ctx, "t-wd", p, pgRefund(150000))
		if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
			t.Fatalf("hold %s: %+v %v", p, d, err)
		}
		e, err := store.Exception(ctx, "t-wd", d.ExceptionID)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}

	e := hold("p-wd-1")
	if _, err := store.Withdraw(ctx, "t-wd", e.ID, "agent://someone-else", base); !errors.Is(err, mandate.ErrExceptionNotFound) {
		t.Fatalf("another agent: %v", err)
	}
	got, err := store.Withdraw(ctx, "t-wd", e.ID, e.Subject, base)
	if err != nil || !got.Withdrawn() {
		t.Fatalf("withdraw: %+v %v", got, err)
	}
	again, err := store.Withdraw(ctx, "t-wd", e.ID, e.Subject, base.Add(time.Minute))
	if err != nil || !again.Withdrawn() || !again.ResolvedAt.Equal(*got.ResolvedAt) {
		t.Fatalf("withdraw again: %+v %v", again, err)
	}
	if _, err := store.ApproveOnce(ctx, "t-wd", e.ID, "owner@x", base); !errors.Is(err, mandate.ErrExceptionState) {
		t.Fatalf("approve after withdraw: %v", err)
	}
	// A retry of the withdrawn call is refused for good, and doesn't hold
	// it again.
	if d, err := a.Authorize(ctx, "t-wd", "p-wd-1", pgRefund(150000)); err != nil || d.Outcome != mandate.OutcomeWithdrawn {
		t.Fatalf("retry after withdraw: %+v %v", d, err)
	}
	if x, _ := store.Exception(ctx, "t-wd", e.ID); !x.Withdrawn() {
		t.Fatalf("the retry changed the withdrawn record: %+v", x)
	}

	refused := hold("p-wd-2")
	if _, err := store.Reject(ctx, "t-wd", refused.ID, "owner@x", base, mandate.Refusal{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Withdraw(ctx, "t-wd", refused.ID, refused.Subject, base); !errors.Is(err, mandate.ErrExceptionState) {
		t.Fatalf("withdraw after refusal: %v", err)
	}
	approved := hold("p-wd-5")
	if _, err := store.ApproveOnce(ctx, "t-wd", approved.ID, "owner@x", base); err != nil {
		t.Fatal(err)
	}
	if w, err := store.Withdraw(ctx, "t-wd", approved.ID, approved.Subject, base); err != nil || !w.Withdrawn() || len(w.PriorApprovals) != 1 {
		t.Fatalf("withdraw an approved, uncollected call: %+v %v", w, err)
	}

	waiting := hold("p-wd-3")
	if waiting.ExpiresAt == nil {
		t.Fatal("a held call without an expiry")
	}
	if es, err := store.ExpireHeld(ctx, waiting.ExpiresAt.Add(-time.Second), 10); err != nil || len(es) != 0 {
		t.Fatalf("before its expiry: %d %v", len(es), err)
	}
	es, err := store.ExpireHeld(ctx, *waiting.ExpiresAt, 10)
	if err != nil {
		t.Fatal(err)
	}
	var expired []string
	for _, x := range es {
		if x.TenantID == "t-wd" {
			expired = append(expired, x.ID)
		}
	}
	if len(expired) != 1 || expired[0] != waiting.ID {
		t.Fatalf("expired %v, want only %s (not the withdrawn or the refused)", expired, waiting.ID)
	}
	if x, _ := store.Exception(ctx, "t-wd", waiting.ID); x.Status != mandate.ExceptionExpired {
		t.Fatalf("stored status %s", x.Status)
	}
	if es, err := store.ExpireHeld(ctx, *waiting.ExpiresAt, 10); err != nil {
		t.Fatal(err)
	} else {
		for _, x := range es {
			if x.ID == waiting.ID {
				t.Fatal("expired twice")
			}
		}
	}

	// A retry records an expiry before the sweep: the sweep still returns
	// it, once, so its alert goes out.
	retried := hold("p-wd-4")
	retryAt := retried.ExpiresAt.Add(10 * time.Second)
	if d, err := mandate.NewAuthorizer(store, nil, at(retryAt)).Authorize(ctx, "t-wd", "p-wd-4", pgRefund(150000)); err != nil || d.Outcome != mandate.OutcomeOwnerExpired {
		t.Fatalf("retry: %+v %v", d, err)
	}
	told := func() int {
		t.Helper()
		es, err := store.ExpireHeld(ctx, retryAt, 50)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, x := range es {
			if x.ID == retried.ID {
				n++
			}
		}
		return n
	}
	if n := told(); n != 1 {
		t.Fatalf("a retry-recorded expiry told %d times", n)
	}
	if n := told(); n != 0 {
		t.Fatal("told twice")
	}

	// An expiry older than ExpiryTellWindow is recorded, not returned.
	old := hold("p-wd-6")
	es, err = store.ExpireHeld(ctx, old.ExpiresAt.Add(2*time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range es {
		if x.ID == old.ID {
			t.Fatal("an old expiry was returned to be announced")
		}
	}
	if x, _ := store.Exception(ctx, "t-wd", old.ID); x.Status != mandate.ExceptionExpired {
		t.Fatalf("old hold status %s", x.Status)
	}
}
