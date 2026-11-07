package tenant_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
)

// The spend limit is raised only from the limit it is, so raises at once
// from the same limit add one step; an account with none raises from the
// unset value. Run on both stores.
func testRaiseSpendLimit(t *testing.T, store tenant.Store) {
	t.Helper()
	ctx := context.Background()
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "limit-" + tenant.NewID()[4:12], Status: tenant.StatusActive}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatal(err)
	}
	if err := store.RaiseSpendLimit(ctx, tt.ID, 200, 300, 100); !errors.Is(err, tenant.ErrLimitChanged) {
		t.Fatalf("from a limit it isn't: %v", err)
	}
	if err := store.RaiseSpendLimit(ctx, tt.ID, 100, 200, 100); err != nil {
		t.Fatalf("from the unset value: %v", err)
	}
	got, _ := store.Get(ctx, tt.ID)
	if tenant.SpendLimitCents(got) != 200 {
		t.Fatalf("the limit: %d", tenant.SpendLimitCents(got))
	}
	var ok atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := store.RaiseSpendLimit(ctx, tt.ID, 200, 300, 100); {
			case err == nil:
				ok.Add(1)
			case !errors.Is(err, tenant.ErrLimitChanged):
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ = store.Get(ctx, tt.ID)
	if ok.Load() != 1 || tenant.SpendLimitCents(got) != 300 {
		t.Fatalf("8 at once: %d applied, limit %d", ok.Load(), tenant.SpendLimitCents(got))
	}
	if err := store.RaiseSpendLimit(ctx, tenant.NewID(), 100, 200, 100); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("an unknown account: %v", err)
	}
}

// A warning is marked once a period at each share: again at the same or a
// lower share is false, a higher one or a new period true. Run on both
// stores.
func testMarkCreditsWarned(t *testing.T, store tenant.Store) {
	t.Helper()
	ctx := context.Background()
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "warned-" + tenant.NewID()[4:12], Status: tenant.StatusActive}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatal(err)
	}
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		period time.Time
		pct    int
		want   bool
	}{
		{nov, 80, true}, {nov, 80, false}, {nov, 100, true}, {nov, 80, false}, {nov, 100, false},
		{nov.AddDate(0, 1, 0), 80, true},
	} {
		if first, err := store.MarkCreditsWarned(ctx, tt.ID, c.period, c.pct); err != nil || first != c.want {
			t.Fatalf("mark %d (%s at %d): %v %v, want %v", i, c.period.Format("Jan"), c.pct, first, err, c.want)
		}
	}
	var firsts atomic.Int32
	var wg sync.WaitGroup
	dec := nov.AddDate(0, 2, 0)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if first, err := store.MarkCreditsWarned(ctx, tt.ID, dec, 80); err != nil {
				t.Error(err)
			} else if first {
				firsts.Add(1)
			}
		}()
	}
	wg.Wait()
	if firsts.Load() != 1 {
		t.Fatalf("8 at once: %d firsts", firsts.Load())
	}
	if _, err := store.MarkCreditsWarned(ctx, tenant.NewID(), nov, 80); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("an unknown account: %v", err)
	}
}

func TestMemoryStoreSpendLimitAndWarnings(t *testing.T) {
	t.Parallel()
	testRaiseSpendLimit(t, tenant.NewMemoryStore())
	testMarkCreditsWarned(t, tenant.NewMemoryStore())
}

func TestPostgresStoreSpendLimitAndWarnings(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	defer truncateTenants(t, pool)
	testRaiseSpendLimit(t, tenant.NewPostgresStore(pool))
	testMarkCreditsWarned(t, tenant.NewPostgresStore(pool))
}
