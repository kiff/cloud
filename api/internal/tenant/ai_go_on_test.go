package tenant_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
)

// A go-on past the AI fair-use bound counts in its period only: the first
// one of a new period is 1 whatever the previous period reached, and two at
// once both count. Run on both stores.
func testAddAIGoOn(t *testing.T, store tenant.Store) {
	t.Helper()
	ctx := context.Background()
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "go-on-" + tenant.NewID()[4:12], Status: tenant.StatusActive}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatal(err)
	}
	oct := time.Date(2026, 10, 20, 9, 12, 0, 0, time.UTC)
	nov := oct.AddDate(0, 1, 0)

	for want := 1; want <= 2; want++ {
		if n, err := store.AddAIGoOn(ctx, tt.ID, oct); err != nil || n != want {
			t.Fatalf("go-on %d in October: %d, %v", want, n, err)
		}
	}
	// The new period starts at 1, not at October's 2.
	if n, err := store.AddAIGoOn(ctx, tt.ID, nov); err != nil || n != 1 {
		t.Fatalf("the first go-on in November: %d, %v", n, err)
	}
	got, err := store.Get(ctx, tt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := got.Metadata[tenant.MetadataKeyAIGoOn].(string); p != nov.Format(time.RFC3339) {
		t.Fatalf("period %q", p)
	}

	// Concurrent go-ons each count.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.AddAIGoOn(ctx, tt.ID, nov); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n, err := store.AddAIGoOn(ctx, tt.ID, nov); err != nil || n != 10 {
		t.Fatalf("after 1 + 8 concurrent + 1: %d, %v", n, err)
	}

	if _, err := store.AddAIGoOn(ctx, tenant.NewID(), nov); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("an unknown account: %v", err)
	}
}

func TestMemoryStoreAddAIGoOn(t *testing.T) {
	t.Parallel()
	testAddAIGoOn(t, tenant.NewMemoryStore())
}

func TestPostgresStoreAddAIGoOn(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	defer truncateTenants(t, pool)
	testAddAIGoOn(t, tenant.NewPostgresStore(pool))
}
