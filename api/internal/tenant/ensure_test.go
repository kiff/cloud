package tenant_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
)

// fakeProvisioner implements tenant.TenantProvisioner without
// touching Postgres. It records calls so tests can assert
// idempotency and cleanup paths fire correctly.
type fakeProvisioner struct {
	mu             sync.Mutex
	provisioned    map[string]int
	deprovisioned  map[string]int
	provisionErr   error
	deprovisionErr error
	store          tenant.Store // optional: stamp StatusActive on Provision
}

func (f *fakeProvisioner) Provision(ctx context.Context, id string) error {
	f.mu.Lock()
	if f.provisioned == nil {
		f.provisioned = map[string]int{}
	}
	f.provisioned[id]++
	err := f.provisionErr
	store := f.store
	f.mu.Unlock()
	if err != nil {
		return err
	}
	// Mirror real Provisioner: stamp StatusActive on the row.
	if store != nil {
		now := time.Now().UTC()
		if serr := store.SetStatus(ctx, id, tenant.StatusActive, &now); serr != nil {
			return serr
		}
	}
	return nil
}

func (f *fakeProvisioner) Deprovision(ctx context.Context, id string) error {
	f.mu.Lock()
	if f.deprovisioned == nil {
		f.deprovisioned = map[string]int{}
	}
	f.deprovisioned[id]++
	err := f.deprovisionErr
	store := f.store
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if store != nil {
		_ = store.SetStatus(ctx, id, tenant.StatusDeleted, nil)
	}
	return nil
}

func TestEnsureForSubject_FirstSignInProvisions(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	subjects := tenant.NewMemorySubjectStore()
	prov := &fakeProvisioner{store: store}

	got, err := tenant.EnsureForSubject(context.Background(), store, prov, subjects, "subj-1")
	if err != nil {
		t.Fatalf("EnsureForSubject: %v", err)
	}
	if got.ID == "" {
		t.Fatalf("returned tenant has empty id: %+v", got)
	}
	if got.Status != tenant.StatusActive {
		t.Fatalf("status = %q, want active", got.Status)
	}
	if !strings.HasPrefix(got.Slug, "user-") {
		t.Fatalf("slug = %q, want prefix user-", got.Slug)
	}
	if cnt := prov.provisioned[got.ID]; cnt != 1 {
		t.Fatalf("Provision called %d times, want 1", cnt)
	}

	mapping, err := subjects.Get(context.Background(), "subj-1")
	if err != nil {
		t.Fatalf("subject mapping not recorded: %v", err)
	}
	if mapping.TenantID != got.ID {
		t.Fatalf("mapping.TenantID = %q, want %q", mapping.TenantID, got.ID)
	}
}

func TestEnsureForSubject_SecondCallReturnsSameTenant(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	subjects := tenant.NewMemorySubjectStore()
	prov := &fakeProvisioner{store: store}

	first, err := tenant.EnsureForSubject(context.Background(), store, prov, subjects, "subj-1")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := tenant.EnsureForSubject(context.Background(), store, prov, subjects, "subj-1")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotency broken: first=%q second=%q", first.ID, second.ID)
	}
	if cnt := prov.provisioned[first.ID]; cnt != 1 {
		t.Fatalf("Provision called %d times for the same tenant; want 1", cnt)
	}
}

func TestEnsureForSubject_DifferentSubjectsGetDifferentTenants(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	subjects := tenant.NewMemorySubjectStore()
	prov := &fakeProvisioner{store: store}

	a, err := tenant.EnsureForSubject(context.Background(), store, prov, subjects, "subj-a")
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, err := tenant.EnsureForSubject(context.Background(), store, prov, subjects, "subj-b")
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if a.ID == b.ID {
		t.Fatalf("two subjects collapsed to one tenant: %q", a.ID)
	}
	if a.Slug == b.Slug {
		t.Fatalf("two subjects produced the same slug: %q", a.Slug)
	}
}

func TestEnsureForSubject_RejectsEmptySubject(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	subjects := tenant.NewMemorySubjectStore()
	prov := &fakeProvisioner{store: store}
	if _, err := tenant.EnsureForSubject(context.Background(), store, prov, subjects, ""); err == nil {
		t.Fatalf("expected error for empty subject")
	}
}

func TestEnsureForSubject_ProvisionFailureCleansUp(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	subjects := tenant.NewMemorySubjectStore()
	prov := &fakeProvisioner{store: store, provisionErr: errors.New("boom")}

	if _, err := tenant.EnsureForSubject(context.Background(), store, prov, subjects, "subj-1"); err == nil {
		t.Fatalf("expected provision error to surface")
	}
	// Subject should not be mapped after a provision failure.
	if _, err := subjects.Get(context.Background(), "subj-1"); !errors.Is(err, tenant.ErrSubjectNotMapped) {
		t.Fatalf("subject got mapped despite provision failure: %v", err)
	}
	// Best-effort Deprovision should have fired.
	if len(prov.deprovisioned) == 0 {
		t.Fatalf("Deprovision was not called after provision failure")
	}
}

func TestEnsureForSubject_ConcurrentFirstSignIn(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	subjects := tenant.NewMemorySubjectStore()
	prov := &fakeProvisioner{store: store}

	const N = 10
	var wg sync.WaitGroup
	results := make(chan tenant.Tenant, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t1, err := tenant.EnsureForSubject(context.Background(), store, prov, subjects, "subj-race")
			if err == nil {
				results <- t1
			}
		}()
	}
	wg.Wait()
	close(results)

	// Every successful caller must see the same tenant id.
	var first string
	count := 0
	for tt := range results {
		count++
		if first == "" {
			first = tt.ID
		} else if tt.ID != first {
			t.Fatalf("concurrent callers saw different tenant ids: %q vs %q", first, tt.ID)
		}
	}
	if count != N {
		t.Fatalf("expected %d successful results, got %d", N, count)
	}
	// Exactly one mapping in the subject store.
	mapping, err := subjects.Get(context.Background(), "subj-race")
	if err != nil {
		t.Fatalf("Get mapping: %v", err)
	}
	if mapping.TenantID != first {
		t.Fatalf("mapping.TenantID = %q, want %q", mapping.TenantID, first)
	}
}

// fakeRacingSubjectStore lets us simulate the
// "another goroutine won the insert" path on a controllable
// timeline: the first Get returns ErrSubjectNotMapped (so the
// caller proceeds to provision), but by the time the caller's
// Set runs, the winner has been recorded so Set returns
// ErrSubjectAlreadyMapped.
type fakeRacingSubjectStore struct {
	mu       sync.Mutex
	winner   string
	getCalls atomic.Int32
	losses   atomic.Int32
	created  time.Time
	injectAt int32 // after this many Get calls, surface the winner
}

func (f *fakeRacingSubjectStore) Get(_ context.Context, subject string) (tenant.SubjectMapping, error) {
	calls := f.getCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	// Before the injection point, behave as if no mapping exists.
	if calls <= f.injectAt {
		return tenant.SubjectMapping{}, tenant.ErrSubjectNotMapped
	}
	if f.winner == "" {
		return tenant.SubjectMapping{}, tenant.ErrSubjectNotMapped
	}
	return tenant.SubjectMapping{Subject: subject, TenantID: f.winner, CreatedAt: f.created}, nil
}

func (f *fakeRacingSubjectStore) Set(_ context.Context, subject, tenantID string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.winner == "" {
		f.winner = tenantID
		f.created = now
		return nil
	}
	if f.winner == tenantID {
		return nil
	}
	f.losses.Add(1)
	return tenant.ErrSubjectAlreadyMapped
}

// Invariant: When two first sign-ins for the same subject race, exactly
// one tenant survives and the loser cleans up the one it provisioned.
// No orphan tenant is left behind.
// Category: failure-modes
// Attack: Two first sign-ins for the same subject arrive at once, each
// provisioning a tenant
// Refused: Exactly one survives and the loser deletes the tenant it
// created, leaving no orphan
func TestEnsureForSubject_LoserOfRaceCleansUpItsTenant(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	// injectAt=1: the first Get (the fast-path lookup) returns
	// ErrSubjectNotMapped, so the caller proceeds to provision.
	// Before the caller's Set runs we manually plant the winner.
	subjects := &fakeRacingSubjectStore{injectAt: 1}
	prov := &fakeProvisioner{store: store}

	// Pre-create the winner tenant so the post-conflict re-read
	// can find it.
	winnerID := tenant.NewID()
	if err := store.Create(context.Background(), tenant.Tenant{
		ID:     winnerID,
		Slug:   "user-winner",
		Status: tenant.StatusActive,
	}); err != nil {
		t.Fatalf("seed winner tenant: %v", err)
	}
	// Plant the winner mapping directly (bypassing Set so the
	// loser's Set will conflict).
	subjects.mu.Lock()
	subjects.winner = winnerID
	subjects.created = time.Now().UTC()
	subjects.mu.Unlock()

	got, err := tenant.EnsureForSubject(context.Background(), store, prov, subjects, "subj-r")
	if err != nil {
		t.Fatalf("EnsureForSubject (loser path): %v", err)
	}
	if got.ID != winnerID {
		t.Fatalf("got tenant %q, want winner %q", got.ID, winnerID)
	}
	if subjects.losses.Load() != 1 {
		t.Fatalf("losses = %d, want 1", subjects.losses.Load())
	}
	// The loser provisioned a tenant; that tenant must have been
	// deprovisioned to avoid a leaked schema.
	if len(prov.deprovisioned) != 1 {
		t.Fatalf("Deprovision call count = %d, want 1", len(prov.deprovisioned))
	}
}
