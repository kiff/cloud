package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/receipt"
)

// fixtureReceipt builds a minimal receipt for handler tests.
// Tenant + trace are the knobs the test cases vary; everything
// else is filled in.
func fixtureReceipt(tenantID, traceID string) receipt.Receipt {
	return receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion,
		TenantID:      tenantID,
		TraceID:       traceID,
		EntityID:      "order-001",
		EntityType:    "Order",
		ActionName:    "MARK_PAID",
		IssuedAt:      time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC),
		CausalChain: []receipt.ChainStep{
			{Sequence: 1, Kind: "event_ingested", DataHash: "deadbeef", OccurredAt: time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)},
			{Sequence: 2, Kind: "action_executed", DataHash: "cafebabe", OccurredAt: time.Date(2026, time.May, 24, 12, 0, 0, 1, time.UTC)},
		},
		ComplianceMap: refundDomainCompliance(),
	}
}

// authedRequest builds a request with an Identity attached to the
// context, mirroring what the auth middleware attaches in
// production. Useful for handler unit tests that don't need to go
// through the full middleware stack.
func authedRequest(t *testing.T, method, target, tenantID string) *http.Request {
	return authedDomainRequest(t, method, target, tenantID, "")
}

func authedDomainRequest(t *testing.T, method, target, tenantID, domainID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	id := auth.Identity{
		Subject:  "test-subject",
		Method:   auth.MethodAPIKey,
		Tenant:   tenantID,
		DomainID: domainID,
	}
	ctx := auth.WithIdentity(req.Context(), id)
	return req.WithContext(ctx)
}

func TestReceiptsHandler_DomainBoundReadsAreScoped(t *testing.T) {
	t.Parallel()
	store := receipt.NewMemoryStore()
	refund := fixtureReceipt("ten-A", "trace-refund")
	refund.DomainID = "refunds"
	payout := fixtureReceipt("ten-A", "trace-payout")
	payout.DomainID = "payouts"
	for _, row := range []receipt.Receipt{refund, payout} {
		if err := store.Save(context.Background(), row); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, authedDomainRequest(t, http.MethodGet, "/v1/receipts", "ten-A", "refunds"))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d; body=%q", list.Code, list.Body.String())
	}
	if !containsExact(list.Body.String(), "trace-refund") || containsExact(list.Body.String(), "trace-payout") {
		t.Fatalf("domain-scoped list leaked or omitted rows: %s", list.Body.String())
	}

	get := httptest.NewRecorder()
	mux.ServeHTTP(get, authedDomainRequest(t, http.MethodGet, "/v1/receipts/trace-payout", "ten-A", "refunds"))
	if get.Code != http.StatusNotFound {
		t.Fatalf("cross-domain get status = %d, want 404", get.Code)
	}
}

func TestReceiptsHandler_GetReturnsReceiptForOwningTenant(t *testing.T) {
	t.Parallel()

	store := receipt.NewMemoryStore()
	want := fixtureReceipt("ten-A", "trace-001")
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(t, "GET", "/v1/receipts/trace-001", "ten-A"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var got receipt.Receipt
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.TenantID != "ten-A" {
		t.Errorf("TenantID: got %q, want %q", got.TenantID, "ten-A")
	}
	if got.TraceID != "trace-001" {
		t.Errorf("TraceID: got %q, want %q", got.TraceID, "trace-001")
	}
	if got.ActionName != "MARK_PAID" {
		t.Errorf("ActionName: got %q", got.ActionName)
	}
}

func TestReceiptsHandler_GetReturnsNotFoundForMissingTrace(t *testing.T) {
	t.Parallel()

	store := receipt.NewMemoryStore()
	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedRequest(t, "GET", "/v1/receipts/trace-missing", "ten-A"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestReceiptsHandler_CrossTenantReadsReturn404NotForbidden(t *testing.T) {
	t.Parallel()

	// Tenant A saves a receipt; tenant B tries to read it by
	// guessing the trace id. The handler must return 404, not 403
	// — a 403 would leak the receipt's existence to a tenant that
	// has no business knowing about it.
	store := receipt.NewMemoryStore()
	r := fixtureReceipt("ten-A", "trace-private")
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)

	// Confirm the receipt exists under ten-A first.
	recA := httptest.NewRecorder()
	mux.ServeHTTP(recA, authedRequest(t, "GET", "/v1/receipts/trace-private", "ten-A"))
	if recA.Code != http.StatusOK {
		t.Fatalf("ten-A read: got %d, want 200", recA.Code)
	}

	// Now ten-B tries to read it. 404, not 200 and not 403.
	recB := httptest.NewRecorder()
	mux.ServeHTTP(recB, authedRequest(t, "GET", "/v1/receipts/trace-private", "ten-B"))
	if recB.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read: got %d, want 404 (cross-tenant existence must not leak)", recB.Code)
	}
}

func TestReceiptsHandler_GetWithoutIdentityReturns401(t *testing.T) {
	t.Parallel()

	store := receipt.NewMemoryStore()
	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)

	// No auth.WithIdentity on the context — the handler must
	// refuse rather than serving an empty-tenant query.
	req := httptest.NewRequest("GET", "/v1/receipts/trace-001", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestReceiptsHandler_GetWithEmptyTenantReturns401(t *testing.T) {
	t.Parallel()

	// Identity present but no tenant resolved. The middleware
	// would normally have rejected such a request; the handler
	// guards anyway as defense in depth.
	store := receipt.NewMemoryStore()
	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)

	req := httptest.NewRequest("GET", "/v1/receipts/trace-001", nil)
	id := auth.Identity{Subject: "test", Method: auth.MethodAPIKey} // Tenant is ""
	ctx := auth.WithIdentity(req.Context(), id)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestReceiptsHandler_ListReturnsTenantsReceipts confirms the
// list endpoint returns receipts in descending issued_at order
// and is scoped strictly to the caller's tenant.
func TestReceiptsHandler_ListReturnsTenantsReceipts(t *testing.T) {
	t.Parallel()
	store := receipt.NewMemoryStore()
	t1 := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

	mustSave := func(tenantID, traceID string, when time.Time) {
		t.Helper()
		if err := store.Save(context.Background(), receipt.Receipt{
			SchemaVersion: receipt.SchemaVersion,
			TenantID:      tenantID,
			TraceID:       traceID,
			EntityID:      "ent-1",
			IssuedAt:      when,
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	mustSave("ten-1", "trace-old", t1)
	mustSave("ten-1", "trace-new", t1.Add(time.Hour))
	mustSave("ten-1", "trace-mid", t1.Add(30*time.Minute))
	mustSave("ten-2", "trace-other", t1.Add(2*time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/v1/receipts", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "subj-1", Tenant: "ten-1", Method: auth.MethodAPIKey, APIKey: true,
	}))
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Receipts []struct {
			TraceID string `json:"trace_id"`
		} `json:"receipts"`
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Limit != 50 {
		t.Errorf("limit = %d, want 50", got.Limit)
	}
	if len(got.Receipts) != 3 {
		t.Fatalf("receipts = %d, want 3 (cross-tenant leak?)", len(got.Receipts))
	}
	wantOrder := []string{"trace-new", "trace-mid", "trace-old"}
	for i, r := range got.Receipts {
		if r.TraceID != wantOrder[i] {
			t.Errorf("receipts[%d].trace_id = %q, want %q (DESC by issued_at)", i, r.TraceID, wantOrder[i])
		}
	}
}

func TestReceiptsHandler_ListRequiresIdentity(t *testing.T) {
	t.Parallel()
	store := receipt.NewMemoryStore()
	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/receipts", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestReceiptsHandler_ListLimitClamping(t *testing.T) {
	t.Parallel()
	store := receipt.NewMemoryStore()
	t1 := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_ = store.Save(context.Background(), receipt.Receipt{
			SchemaVersion: receipt.SchemaVersion,
			TenantID:      "ten-1",
			TraceID:       fmt.Sprintf("trace-%02d", i),
			EntityID:      "ent-1",
			IssuedAt:      t1.Add(time.Duration(i) * time.Minute),
		})
	}
	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)

	tests := []struct {
		name      string
		query     string
		wantLimit int
		wantCount int
	}{
		{"explicit limit", "?limit=2", 2, 2},
		{"default", "", 50, 5},
		{"zero clamps to default", "?limit=0", 50, 5},
		{"negative clamps to default", "?limit=-7", 50, 5},
		{"over-cap clamps", "?limit=999999", 200, 5},
		{"unparsable clamps to default", "?limit=foo", 50, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/receipts"+tt.query, nil)
			req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
				Subject: "s", Tenant: "ten-1", Method: auth.MethodAPIKey, APIKey: true,
			}))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			var got struct {
				Receipts []json.RawMessage `json:"receipts"`
				Limit    int               `json:"limit"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Limit != tt.wantLimit {
				t.Errorf("limit = %d, want %d", got.Limit, tt.wantLimit)
			}
			if len(got.Receipts) != tt.wantCount {
				t.Errorf("receipts = %d, want %d", len(got.Receipts), tt.wantCount)
			}
		})
	}
}

// A receipt's steps come with their words in the reader's language, beside
// the receipt: its causal_chain stays byte for byte what KIFF signed, so a
// phone that hashes the chain from the response still verifies it.
func TestReceiptsHandler_StepWordsAreInTheReadersLanguageAndLeaveTheChainAlone(t *testing.T) {
	t.Parallel()
	store := receipt.NewMemoryStore()
	r := fixtureReceipt("ten-A", "trace-words")
	r.CausalChain = []receipt.ChainStep{
		{Sequence: 1, Kind: "proposal_decision", Message: "allowed", DataHash: "aa", OccurredAt: r.IssuedAt},
		{Sequence: 2, Kind: "action_executed", Message: "refund of 500 cents issued", DataHash: "bb", OccurredAt: r.IssuedAt},
	}
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handlers.NewReceiptsHandler(store).Register(mux)
	for _, path := range []string{"/v1/receipts/trace-words", "/v1/receipts"} {
		req := authedRequest(t, "GET", path, "ten-A")
		req.Header.Set("Accept-Language", "de-DE,de;q=0.9")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		var one struct {
			Chain     json.RawMessage    `json:"causal_chain"`
			StepWords []receipt.StepWord `json:"step_words"`
		}
		body := rec.Body.Bytes()
		if path == "/v1/receipts" {
			var list struct {
				Receipts []json.RawMessage `json:"receipts"`
			}
			if err := json.Unmarshal(body, &list); err != nil || len(list.Receipts) != 1 {
				t.Fatalf("list: %v %s", err, body)
			}
			body = list.Receipts[0]
		}
		if err := json.Unmarshal(body, &one); err != nil {
			t.Fatal(err)
		}
		signed, _ := json.Marshal(r.CausalChain)
		if string(one.Chain) != string(signed) {
			t.Errorf("%s: the chain isn't what was signed:\n%s\n%s", path, one.Chain, signed)
		}
		if len(one.StepWords) != 1 || one.StepWords[0].Sequence != 1 || one.StepWords[0].Code != receipt.CodeDecisionAllowed || one.StepWords[0].Words != "Erlaubt" {
			t.Errorf("%s: step words: %#v", path, one.StepWords)
		}
	}
}
