package receipt_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/anchor"
	"github.com/kiff/cloud/api/internal/receipt"
)

// newLocalPipeline builds a Pipeline backed by an in-memory store
// and a LocalAttester (signs only, no chain). LocalAttester is
// enough for the reconcile + attest tests; the AnchorAttester path
// already has dedicated coverage in anchor_attester_test.go.
func newLocalPipeline(t *testing.T) (*receipt.Pipeline, *receipt.MemoryStore, *anchor.Wallet) {
	t.Helper()
	wallet, err := anchor.NewRandomWallet()
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	store := receipt.NewMemoryStore()
	now := func() time.Time { return time.Date(2026, time.May, 24, 12, 0, 5, 0, time.UTC) }
	pl, err := receipt.NewPipeline(store, receipt.NewLocalAttester(wallet, now))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	return pl, store, wallet
}

func TestPipeline_ReconcileTenant_BuildsReceiptForExecutedTrace(t *testing.T) {
	t.Parallel()
	pl, store, _ := newLocalPipeline(t)

	const tenantID = "ten-1"
	const traceID = "trace-1"
	auditStore := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	rt := &kiffruntime.Runtime{Audit: auditStore}

	built, err := pl.ReconcileTenant(context.Background(), tenantID, rt)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if built != 1 {
		t.Fatalf("built = %d, want 1", built)
	}

	r, err := store.Get(context.Background(), tenantID, traceID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if r.TenantID != tenantID || r.TraceID != traceID {
		t.Fatalf("receipt mismatch: tenant=%q trace=%q", r.TenantID, r.TraceID)
	}
	if r.ActionName != "MARK_PAID" {
		t.Fatalf("action_name = %q, want MARK_PAID", r.ActionName)
	}
	if r.Attestation != nil {
		t.Fatalf("reconcile should not attest; got attestation %+v", r.Attestation)
	}
}

func TestPipeline_ReconcileTenant_IsIdempotent(t *testing.T) {
	t.Parallel()
	pl, store, _ := newLocalPipeline(t)

	const tenantID = "ten-1"
	const traceID = "trace-1"
	auditStore := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	rt := &kiffruntime.Runtime{Audit: auditStore}

	if _, err := pl.ReconcileTenant(context.Background(), tenantID, rt); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	built, err := pl.ReconcileTenant(context.Background(), tenantID, rt)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if built != 0 {
		t.Fatalf("second reconcile built = %d, want 0 (already exists)", built)
	}
	// And the row count is exactly one.
	pending, err := store.ListUnanchored(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("list unanchored: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("unanchored count = %d, want 1", len(pending))
	}
}

func TestPipeline_AttestPending_PopulatesAttestation(t *testing.T) {
	t.Parallel()
	pl, store, wallet := newLocalPipeline(t)

	const tenantID = "ten-1"
	const traceID = "trace-1"
	auditStore := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	rt := &kiffruntime.Runtime{Audit: auditStore}

	if _, err := pl.ReconcileTenant(context.Background(), tenantID, rt); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	attested, err := pl.AttestPending(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	if attested != 1 {
		t.Fatalf("attested = %d, want 1", attested)
	}

	r, err := store.Get(context.Background(), tenantID, traceID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if r.Attestation == nil {
		t.Fatalf("attestation still nil after AttestPending")
	}
	if r.Attestation.Algorithm != receipt.AttestationAlgorithm {
		t.Fatalf("algorithm = %q, want %q", r.Attestation.Algorithm, receipt.AttestationAlgorithm)
	}
	if got := r.Attestation.WalletAddress; got == "" {
		t.Fatalf("wallet address empty")
	}
	if err := receipt.VerifyAttestation(r, r.Attestation); err != nil {
		t.Fatalf("verify attestation: %v (wallet=%s)", err, wallet.Address().Hex())
	}
}

func TestPipeline_AttestPending_NoOpAfterAttest(t *testing.T) {
	t.Parallel()
	pl, _, _ := newLocalPipeline(t)

	const tenantID = "ten-1"
	const traceID = "trace-1"
	auditStore := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	rt := &kiffruntime.Runtime{Audit: auditStore}

	if _, err := pl.ReconcileTenant(context.Background(), tenantID, rt); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := pl.AttestPending(context.Background(), tenantID); err != nil {
		t.Fatalf("first attest: %v", err)
	}
	attested, err := pl.AttestPending(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("second attest: %v", err)
	}
	if attested != 0 {
		t.Fatalf("second attest = %d, want 0", attested)
	}
}

func TestPipeline_ReconcileTenant_RejectsNilRuntime(t *testing.T) {
	t.Parallel()
	pl, _, _ := newLocalPipeline(t)
	if _, err := pl.ReconcileTenant(context.Background(), "ten-1", nil); !errors.Is(err, receipt.ErrRuntimeMissing) {
		t.Fatalf("err = %v, want ErrRuntimeMissing", err)
	}
}

func TestPipeline_ReconcileTenant_RejectsRuntimeWithoutAudit(t *testing.T) {
	t.Parallel()
	pl, _, _ := newLocalPipeline(t)
	if _, err := pl.ReconcileTenant(context.Background(), "ten-1", &kiffruntime.Runtime{}); !errors.Is(err, receipt.ErrAuditStoreMissing) {
		t.Fatalf("err = %v, want ErrAuditStoreMissing", err)
	}
}

func TestPipeline_AttestPending_RecoversAfterRestart(t *testing.T) {
	// Simulates: receipt was reconciled (saved unanchored), then
	// the process restarted with attest pending. AttestPending
	// run from a fresh Pipeline instance against the same store
	// must complete the attestation.
	t.Parallel()

	const tenantID = "ten-1"
	const traceID = "trace-1"
	store := receipt.NewMemoryStore()
	auditStore := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	rt := &kiffruntime.Runtime{Audit: auditStore}

	wallet, err := anchor.NewRandomWallet()
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	now := func() time.Time { return time.Date(2026, time.May, 24, 12, 0, 5, 0, time.UTC) }

	// "Pre-restart" pipeline: only reconciles, never attests.
	preRestart, err := receipt.NewPipeline(store, receipt.NewLocalAttester(wallet, now))
	if err != nil {
		t.Fatalf("pre pipeline: %v", err)
	}
	if _, err := preRestart.ReconcileTenant(context.Background(), tenantID, rt); err != nil {
		t.Fatalf("pre reconcile: %v", err)
	}

	// "Post-restart" pipeline: shares the store; attests pending.
	postRestart, err := receipt.NewPipeline(store, receipt.NewLocalAttester(wallet, now))
	if err != nil {
		t.Fatalf("post pipeline: %v", err)
	}
	attested, err := postRestart.AttestPending(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("post attest: %v", err)
	}
	if attested != 1 {
		t.Fatalf("post attested = %d, want 1", attested)
	}
}

// stubLister and stubResolver power the RunAnchorLoop test below.
type stubLister struct {
	ids  []string
	once sync.Once
	done chan struct{}
}

func (s *stubLister) ListActiveIDs(_ context.Context) ([]string, error) {
	s.once.Do(func() { close(s.done) })
	return append([]string(nil), s.ids...), nil
}

type stubResolver struct {
	rt *kiffruntime.Runtime
}

func (s *stubResolver) For(_ context.Context, _ string) (*kiffruntime.Runtime, error) {
	return s.rt, nil
}

func TestPipeline_RunAnchorLoop_ImmediateFirstSweep(t *testing.T) {
	t.Parallel()
	pl, store, _ := newLocalPipeline(t)

	const tenantID = "ten-1"
	const traceID = "trace-1"
	auditStore := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	rt := &kiffruntime.Runtime{Audit: auditStore}

	if _, err := pl.ReconcileTenant(context.Background(), tenantID, rt); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	lister := &stubLister{ids: []string{tenantID}, done: make(chan struct{})}
	resolver := &stubResolver{rt: rt}

	// Long interval so only the immediate first sweep runs.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	doneLoop := make(chan struct{})
	go func() {
		pl.RunAnchorLoop(ctx, lister, resolver, time.Hour)
		close(doneLoop)
	}()

	// Wait until the loop ran ListActiveIDs at least once.
	select {
	case <-lister.done:
	case <-time.After(time.Second):
		t.Fatalf("loop did not call ListActiveIDs within 1s")
	}

	// Give the sweep a moment to write through.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		r, err := store.Get(context.Background(), tenantID, traceID)
		if err == nil && r.Attestation != nil {
			cancel()
			<-doneLoop
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-doneLoop
	t.Fatalf("attestation not populated within 500ms of immediate sweep")
}
