package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// idemClock is a tiny mutable clock so tests can advance time past
// the idempotency window deterministically without sleeping.
type idemClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *idemClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *idemClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// idemRig is a proposal rig with a controllable clock and a
// configurable idempotency window, used by the dedup tests. It
// reuses the package fixture (proposalYAML) and the fake-identity
// middleware.
type idemRig struct {
	mux      http.Handler
	rt       *cloudruntime.TenantRuntime
	tenantID string
	clock    *idemClock
}

func newIdemRig(t *testing.T, window time.Duration, extra ...handlers.ProposalsOption) *idemRig {
	t.Helper()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "tenant-idem",
		DisplayName: "tenant idem",
		Status:      tenant.StatusActive,
	}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	clock := &idemClock{t: time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)}
	opts := []handlers.ProposalsOption{
		handlers.WithProposalsClock(clock.now),
		handlers.WithProposalsIdempotencyWindow(window),
	}
	opts = append(opts, extra...)
	h := handlers.NewProposalsHandler(rt, opts...)
	innerMux := http.NewServeMux()
	h.Register(innerMux)

	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "test-subject",
	}))

	rig := &idemRig{mux: wrapped, rt: rt, tenantID: tnt.ID, clock: clock}
	rig.seedPaid(t, "order-idem")
	return rig
}

// seedPaid puts orderID into PAID so AUTO_REFUND is allowed.
func (rig *idemRig) seedPaid(t *testing.T, orderID string) {
	t.Helper()
	rt, err := rig.rt.For(context.Background(), rig.tenantID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID:         kind + "-" + orderID,
			Adapter:    "cloud",
			Type:       kind,
			Source:     "test",
			ReceivedAt: time.Now().UTC(),
			EntityID:   orderID,
			EntityType: "Order",
			ActorID:    "actor-test",
			Metadata:   kiffevent.Metadata{TraceID: "trace-" + orderID},
			Payload:    map[string]any{},
		}
		if _, err := rt.IngestRaw(context.Background(), raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}
}

// driveJSON posts a proposal with optional Idempotency-Key header.
func (rig *idemRig) driveJSON(t *testing.T, body any, header string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/proposals/decide", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if header != "" {
		req.Header.Set("Idempotency-Key", header)
	}
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	return rec
}

// decisionRowCount returns how many proposal_decision audit rows
// exist for the given idempotency key (trace id).
func (rig *idemRig) decisionRowCount(t *testing.T, key string) int {
	t.Helper()
	rt, err := rig.rt.For(context.Background(), rig.tenantID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	recs, err := rt.Audit.Query(context.Background(), kiffaudit.Filter{
		TraceID: key,
		Kind:    kiffaudit.Kind("proposal_decision"),
	})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	return len(recs)
}

func decodeProposal(t *testing.T, rec *httptest.ResponseRecorder) struct {
	ProposalID   string   `json:"proposal_id"`
	Outcome      string   `json:"outcome"`
	Reasons      []string `json:"reasons"`
	Deduplicated bool     `json:"deduplicated"`
} {
	t.Helper()
	var out struct {
		ProposalID   string   `json:"proposal_id"`
		Outcome      string   `json:"outcome"`
		Reasons      []string `json:"reasons"`
		Deduplicated bool     `json:"deduplicated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

// ─── tests ────────────────────────────────────────────────────

// A repeated body id within the window returns the recorded
// decision, flags deduplicated, and writes no second decision row.
func TestProposals_DedupesRepeatedBodyIDWithinWindow(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)
	body := map[string]any{
		"id":          "rfnd-body-1",
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}

	first := decodeProposal(t, rig.driveJSON(t, body, ""))
	if first.Outcome != "allowed" {
		t.Fatalf("first outcome: got %q want allowed", first.Outcome)
	}
	if first.Deduplicated {
		t.Fatalf("first call should not be marked deduplicated")
	}

	second := decodeProposal(t, rig.driveJSON(t, body, ""))
	if second.Outcome != "allowed" {
		t.Fatalf("second outcome: got %q want allowed", second.Outcome)
	}
	if !second.Deduplicated {
		t.Fatalf("second call with same id should be deduplicated")
	}
	if n := rig.decisionRowCount(t, "rfnd-body-1"); n != 1 {
		t.Fatalf("decision rows for key: got %d want 1 (no duplicate on retry)", n)
	}
}

// The Idempotency-Key header dedupes the same way when no body id
// is supplied.
func TestProposals_DedupesViaIdempotencyHeader(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)
	body := map[string]any{
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}

	first := decodeProposal(t, rig.driveJSON(t, body, "hdr-key-1"))
	if first.Deduplicated {
		t.Fatalf("first call should not be deduplicated")
	}
	if first.ProposalID != "hdr-key-1" {
		t.Fatalf("proposal id: got %q want hdr-key-1 (header should seed the id)", first.ProposalID)
	}

	second := decodeProposal(t, rig.driveJSON(t, body, "hdr-key-1"))
	if !second.Deduplicated {
		t.Fatalf("second call with same header key should be deduplicated")
	}
	if n := rig.decisionRowCount(t, "hdr-key-1"); n != 1 {
		t.Fatalf("decision rows for header key: got %d want 1", n)
	}
}

// After the window expires the proposal re-evaluates fresh instead
// of returning the prior decision.
func TestProposals_ReevaluatesAfterWindow(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, time.Hour)
	body := map[string]any{
		"id":          "rfnd-expire-1",
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}

	_ = decodeProposal(t, rig.driveJSON(t, body, ""))
	rig.clock.advance(2 * time.Hour) // past the 1h window

	second := decodeProposal(t, rig.driveJSON(t, body, ""))
	if second.Deduplicated {
		t.Fatalf("call after the window should re-evaluate, not dedupe")
	}
	if second.Outcome != "allowed" {
		t.Fatalf("re-evaluated outcome: got %q want allowed", second.Outcome)
	}
}

// With no caller-supplied key, each call mints a distinct id and
// is never deduped (even when the clock is pinned and the
// generated ids would collide, the idempotent path is skipped).
func TestProposals_NoKeyIsNeverDeduped(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)
	body := map[string]any{
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}

	first := decodeProposal(t, rig.driveJSON(t, body, ""))
	second := decodeProposal(t, rig.driveJSON(t, body, ""))
	if first.Deduplicated || second.Deduplicated {
		t.Fatalf("calls without an idempotency key must never be deduped")
	}
}

// countingLimiter records how many times AllowProposal is consulted
// so a test can assert that a deduped retry never reaches the
// limiter (and so never consumes plan quota). It always allows.
type countingLimiter struct{ calls int }

func (c *countingLimiter) AllowProposal(_ context.Context, _ string) (bool, uint64, int, bool, error) {
	c.calls++
	return true, 0, 1000, false, nil
}

// A deduped retry must not consult the limiter — the review's
// blocking point: a retry storm sharing one key must not burn quota.
func TestProposals_DedupHitSkipsLimiter(t *testing.T) {
	t.Parallel()
	lim := &countingLimiter{}
	rig := newIdemRig(t, 24*time.Hour, handlers.WithProposalsLimiter(lim))
	body := map[string]any{
		"id":          "rfnd-lim-1",
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}

	first := decodeProposal(t, rig.driveJSON(t, body, ""))
	if first.Deduplicated {
		t.Fatalf("first call should not be deduplicated")
	}
	second := decodeProposal(t, rig.driveJSON(t, body, ""))
	if !second.Deduplicated {
		t.Fatalf("second identical call should be deduplicated")
	}
	if lim.calls != 1 {
		t.Fatalf("limiter consulted %d times; want 1 (the deduped retry must not consume quota)", lim.calls)
	}
}

// Reusing an idempotency key for a different request (different
// action) must reject with idempotency_key_reuse, not echo the
// prior decision.
func TestProposals_KeyReuseWithDifferentPayloadRejected(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)

	first := decodeProposal(t, rig.driveJSON(t, map[string]any{
		"id":          "rfnd-reuse-1",
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}, ""))
	if first.Outcome != "allowed" {
		t.Fatalf("first outcome: got %q want allowed", first.Outcome)
	}

	// Same key, different action.
	reuse := decodeProposal(t, rig.driveJSON(t, map[string]any{
		"id":          "rfnd-reuse-1",
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "REFUND_ORDER",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 9900, "reason": "different"},
	}, ""))
	if reuse.Outcome != "invalid" {
		t.Fatalf("key reuse outcome: got %q want invalid", reuse.Outcome)
	}
	found := false
	for _, r := range reuse.Reasons {
		if r == "idempotency_key_reuse" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected idempotency_key_reuse reason; got %v", reuse.Reasons)
	}
}

// A prior decision whose JSONB data decoded reasons as []any (the
// Postgres path) must still coerce to []string on a dedup hit.
// This exercises stringSliceFromData's []any branch without a real
// Postgres store, and the backward-compat path where an older row
// has no request_fingerprint.
func TestProposals_DedupCoercesJSONBReasons(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)

	rt, err := rig.rt.For(context.Background(), rig.tenantID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	// Synthetic prior decision shaped like a JSONB-decoded row:
	// reasons is []any, and there is no request_fingerprint (older
	// row) so the payload-match guard is skipped.
	if err := rt.Audit.Append(context.Background(), kiffaudit.Record{
		ID:         "synth-key:blocked",
		Kind:       kiffaudit.Kind("proposal_decision"),
		EntityID:   "order-idem",
		EntityType: "Order",
		TraceID:    "synth-key",
		CreatedAt:  rig.clock.now(),
		Data: map[string]any{
			"outcome": "blocked",
			"reasons": []any{"state_not_allowed"},
		},
	}); err != nil {
		t.Fatalf("append synthetic decision: %v", err)
	}

	resp := decodeProposal(t, rig.driveJSON(t, map[string]any{
		"id":          "synth-key",
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}, ""))
	if !resp.Deduplicated {
		t.Fatalf("expected dedup hit on the synthetic prior decision")
	}
	if resp.Outcome != "blocked" {
		t.Fatalf("outcome: got %q want blocked (from the synthetic prior)", resp.Outcome)
	}
	if len(resp.Reasons) != 1 || resp.Reasons[0] != "state_not_allowed" {
		t.Fatalf("reasons coercion from []any failed; got %v", resp.Reasons)
	}
}

// recordingLocker is a ProposalLocker stub that records the keys
// it was asked to lock and how many releases ran, so a test can
// assert the handler acquires + releases for idempotent requests
// and skips the lock for non-idempotent ones.
type recordingLocker struct {
	mu       sync.Mutex
	acquired []string
	released int
}

func (l *recordingLocker) Acquire(_ context.Context, key string) (func(), error) {
	l.mu.Lock()
	l.acquired = append(l.acquired, key)
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		l.released++
		l.mu.Unlock()
	}, nil
}

func (l *recordingLocker) calls() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.acquired), l.released
}

// An idempotent request acquires and releases the per-key lock;
// a request with no caller-supplied key does not (its generated
// id is unique per call, so there is nothing to serialize).
func TestProposals_LockerAcquiredForIdempotentOnly(t *testing.T) {
	t.Parallel()
	lock := &recordingLocker{}
	rig := newIdemRig(t, 24*time.Hour, handlers.WithProposalsLocker(lock))
	base := map[string]any{
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}

	// No key → no lock.
	_ = decodeProposal(t, rig.driveJSON(t, base, ""))
	if acq, _ := lock.calls(); acq != 0 {
		t.Fatalf("non-idempotent request acquired the lock %d times; want 0", acq)
	}

	// Body id → lock acquired + released once.
	withID := map[string]any{}
	for k, v := range base {
		withID[k] = v
	}
	withID["id"] = "rfnd-lock-1"
	_ = decodeProposal(t, rig.driveJSON(t, withID, ""))
	acq, rel := lock.calls()
	if acq != 1 || rel != 1 {
		t.Fatalf("idempotent request: acquired=%d released=%d; want 1/1", acq, rel)
	}
	if lock.acquired[0] == "" || !strings.Contains(lock.acquired[0], "rfnd-lock-1") {
		t.Fatalf("lock key %q should include the proposal id", lock.acquired[0])
	}
}
