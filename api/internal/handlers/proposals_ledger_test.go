package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// ledgerRig wires a proposal handler with a MemoryUsageLedger and an
// optional limiter, against a PAID-state order so AUTO_REFUND is
// allowed. Returns the mux, the ledger, and the tenant id.
func newLedgerRig(t *testing.T, limiter handlers.Limiter) (http.Handler, *billing.MemoryUsageLedger, string) {
	t.Helper()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-ledger", Status: tenant.StatusActive}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	resolved, err := rt.For(context.Background(), tnt.ID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID: kind + "-order-led", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: "order-led", EntityType: "Order",
			ActorID: "actor-test", Metadata: kiffevent.Metadata{TraceID: "trace-order-led"},
			Payload: map[string]any{},
		}
		if _, err := resolved.IngestRaw(context.Background(), raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}

	ledger := billing.NewMemoryUsageLedger()
	opts := []handlers.ProposalsOption{handlers.WithProposalsUsageLedger(ledger)}
	if limiter != nil {
		opts = append(opts, handlers.WithProposalsLimiter(limiter))
	}
	h := handlers.NewProposalsHandler(rt, opts...)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant: tnt.ID, Subject: "principal", Roles: []string{"ops_agent"},
	}))
	return wrapped, ledger, tnt.ID
}

func postDecide(t *testing.T, mux http.Handler, body any, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/proposals/decide", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func allowedRefundBody() map[string]any {
	return map[string]any{
		"entity_id": "order-led", "entity_type": "Order", "action_name": "AUTO_REFUND",
		"actor_id": "agent-refunds", "parameters": map[string]any{"amount": 4900, "reason": "damaged"},
	}
}

// A governed outcome (allowed) writes exactly one ledger row.
func TestProposals_RecordsGovernedOperationOnce(t *testing.T) {
	t.Parallel()
	mux, ledger, _ := newLedgerRig(t, nil)
	rec := postDecide(t, mux, allowedRefundBody(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := ledger.Len(); got != 1 {
		t.Errorf("ledger len = %d, want 1 (one governed operation)", got)
	}
}

// A malformed proposal (unknown action → invalid) is not a governed
// operation and must not be recorded.
func TestProposals_DoesNotRecordInvalid(t *testing.T) {
	t.Parallel()
	mux, ledger, _ := newLedgerRig(t, nil)
	body := map[string]any{
		"entity_id": "order-led", "entity_type": "Order", "action_name": "NOT_A_REAL_ACTION",
		"actor_id": "agent", "parameters": map[string]any{},
	}
	_ = postDecide(t, mux, body, "")
	if got := ledger.Len(); got != 0 {
		t.Errorf("ledger len = %d, want 0 (invalid is not a governed operation)", got)
	}
}

// limit_exceeded is rejected before evaluation and must not be
// recorded as a governed operation.
func TestProposals_DoesNotRecordLimitExceeded(t *testing.T) {
	t.Parallel()
	mux, ledger, _ := newLedgerRig(t, stubLimiterAlwaysDeny{})
	rec := postDecide(t, mux, allowedRefundBody(), "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := ledger.Len(); got != 0 {
		t.Errorf("ledger len = %d, want 0 (limit_exceeded is not a governed operation)", got)
	}
}

// A deduped retry (same idempotency key) returns the recorded decision
// before re-evaluating, so it must not create a second ledger row.
func TestProposals_DedupedRetryRecordsOnce(t *testing.T) {
	t.Parallel()
	mux, ledger, _ := newLedgerRig(t, nil)
	body := allowedRefundBody()
	for i := 0; i < 2; i++ {
		rec := postDecide(t, mux, body, "idem-led-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d status = %d, body=%s", i, rec.Code, rec.Body.String())
		}
	}
	if got := ledger.Len(); got != 1 {
		t.Errorf("ledger len = %d, want 1 (deduped retry must not double-record)", got)
	}
}
