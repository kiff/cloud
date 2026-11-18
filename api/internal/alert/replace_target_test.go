package alert_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
)

// From a review: replacing a phone's target was create, list, then
// delete the others, so two setups at once could each delete the other's
// target and leave none. ReplaceTarget is one step: however many run at
// once, one target is left at the URL, and other URLs are untouched.
func checkConcurrentReplace(t *testing.T, store alert.Store, tenantID string) {
	t.Helper()
	c := context.Background()
	const url = "https://tafs.example.com/v1/kiff/alerts"
	if err := store.CreateTarget(c, alert.Target{ID: "alrt_ops", TenantID: tenantID, URL: "https://ops.example.com/hook",
		Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for round := range 5 {
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs <- store.ReplaceTarget(c, alert.Target{ID: fmt.Sprintf("alrt_%d_%d", round, i), TenantID: tenantID, URL: url,
					Outcomes: []string{"owner_required"}, Enabled: true, CreatedAt: time.Now().UTC(), Secret: fmt.Sprintf("whsec_%d_%d", round, i)})
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		got, err := store.ListTargets(c, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		var at []alert.Target
		ops := 0
		for _, tg := range got {
			switch tg.URL {
			case url:
				at = append(at, tg)
			default:
				ops++
			}
		}
		if len(at) != 1 || ops != 1 {
			t.Fatalf("round %d: %d targets at the URL, %d others: %+v", round, len(at), ops, got)
		}
		if want := "whsec" + at[0].ID[len("alrt"):]; at[0].Secret != want {
			t.Fatalf("round %d: the target left has secret %q, want its own %q", round, at[0].Secret, want)
		}
	}
}

func TestMemoryStore_ReplaceTargetConcurrent(t *testing.T) {
	t.Parallel()
	checkConcurrentReplace(t, alert.NewMemoryStore(), "tenant-1")
}

func TestPostgresStore_ReplaceTargetConcurrent(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	if err := alert.ApplySchema(context.Background(), pool); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	checkConcurrentReplace(t, alert.NewPostgresStore(pool), provisionTenantRow(t, pool).ID)
}
