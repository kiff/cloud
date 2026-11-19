package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// fakeAPI returns an httptest server that mimics apps/api's
// receipt endpoint. Tests configure it with a static response
// (status + body) and assert the handler's behavior.
func fakeAPI(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity: the dashboard handler must always present an
		// API key. If it ever skips the header, every test
		// fails and we know the regression early.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
}

// silentLogger discards all output so the test runner stays clean.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestReceipt_NotConfiguredRenders200WithExplanation(t *testing.T) {
	t.Parallel()

	// Nil client = the binary started without KIFF_API_BASE_URL /
	// KIFF_DEMO_API_KEY. The dashboard should render an
	// explanatory page rather than 503 — visiting /dashboard/* on
	// an unconfigured deploy must not look like an outage.
	mux := http.NewServeMux()
	mux.Handle("GET /dashboard/receipts/{trace_id}", Receipt(nil, silentLogger()))

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard/receipts/trace-x", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Dashboard not configured") {
		t.Errorf("body missing the not-configured message")
	}
	if !strings.Contains(body, "KIFF_API_BASE_URL") {
		t.Errorf("body missing the env-var hint")
	}
}

func TestReceipt_HappyPathRendersTheReceipt(t *testing.T) {
	t.Parallel()

	receipt := apiclient.Receipt{
		SchemaVersion: "v0.1",
		TenantID:      "ten-demo",
		TraceID:       "trace-001",
		EntityID:      "order-001",
		EntityType:    "Order",
		ActionName:    "MARK_PAID",
		IssuedAt:      time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC),
		CausalChain: []apiclient.ChainStep{
			{
				Sequence:   1,
				Kind:       "event_ingested",
				ActorID:    "agent-cs-bot",
				OccurredAt: time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC),
				DataHash:   strings.Repeat("ab", 32),
			},
			{
				Sequence:   2,
				Kind:       "action_executed",
				ActorID:    "agent-cs-bot",
				OccurredAt: time.Date(2026, time.May, 24, 12, 0, 0, 1, time.UTC),
				DataHash:   strings.Repeat("cd", 32),
			},
		},
		ComplianceMap: []apiclient.ComplianceMapping{
			{Framework: "SOC2", ControlID: "CC7.2", EvidenceKind: "system_monitoring"},
		},
	}
	api := fakeAPI(t, http.StatusOK, receipt)
	defer api.Close()

	client, err := apiclient.New(apiclient.Config{BaseURL: api.URL, APIKey: "kiff_live_demo_test"})
	if err != nil {
		t.Fatalf("apiclient.New: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /dashboard/receipts/{trace_id}", Receipt(client, silentLogger()))

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard/receipts/trace-001", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	mustContain := []string{
		// Layout shows up.
		`class="site-nav"`,
		`class="site-footer"`,
		// The receipt in words: the call, its steps, the proof.
		"MARK_PAID",
		"order-001",
		"trace-001",
		"Event ingested",
		"Action executed",
		"agent-cs-bot",
		"Not signed yet",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// The old sections are gone: compliance map, "not yet" roadmap
	// fields, raw data hashes.
	for _, never := range []string{"SOC2", "Risk score", "Insurer envelope", "Replay link"} {
		if strings.Contains(body, never) {
			t.Errorf("body still shows %q", never)
		}
	}

	// Hash truncation: the full 64-char hash should be replaced
	// with first 8 + ellipsis + last 8.
	if strings.Contains(body, strings.Repeat("ab", 32)) {
		t.Errorf("data hash not truncated; long hex string is unreadable in HTML")
	}
}

func TestReceipt_404FromAPIRendersNotFoundPage(t *testing.T) {
	t.Parallel()

	api := fakeAPI(t, http.StatusNotFound, map[string]string{"error": "receipt: not found"})
	defer api.Close()

	client, err := apiclient.New(apiclient.Config{BaseURL: api.URL, APIKey: "kiff_live_demo_test"})
	if err != nil {
		t.Fatalf("apiclient.New: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /dashboard/receipts/{trace_id}", Receipt(client, silentLogger()))

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard/receipts/trace-missing", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusNotFound)
	}
	if !strings.Contains(rr.Body.String(), "No receipt for this trace") {
		t.Errorf("body missing the not-found message")
	}
}

func TestReceipt_401FromAPIRenders502NotLeaking(t *testing.T) {
	t.Parallel()

	// 401 from the API means the operator's key is bad. The
	// dashboard returns 502 (the upstream is misconfigured from
	// the visitor's perspective) and renders the generic error
	// page — never surfacing "your API key is wrong" to a
	// random visitor.
	api := fakeAPI(t, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	defer api.Close()

	client, err := apiclient.New(apiclient.Config{BaseURL: api.URL, APIKey: "kiff_live_demo_bad"})
	if err != nil {
		t.Fatalf("apiclient.New: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /dashboard/receipts/{trace_id}", Receipt(client, silentLogger()))

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard/receipts/trace-x", nil))

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusBadGateway)
	}
	body := rr.Body.String()
	if strings.Contains(strings.ToLower(body), "unauthorized") {
		t.Errorf("body leaked upstream auth-failure detail to visitor")
	}
	if strings.Contains(strings.ToLower(body), "api key") {
		t.Errorf("body leaked the configured-key concept to visitor")
	}
	if !strings.Contains(body, "Could not load this receipt") {
		t.Errorf("body missing the generic error message")
	}
}

func TestReceipt_TransportErrorRenders502(t *testing.T) {
	t.Parallel()

	// Point the client at a server we close immediately so the
	// next request fails at transport.
	api := fakeAPI(t, http.StatusOK, nil)
	api.Close()

	client, err := apiclient.New(apiclient.Config{BaseURL: api.URL, APIKey: "kiff_live_demo_test"})
	if err != nil {
		t.Fatalf("apiclient.New: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /dashboard/receipts/{trace_id}", Receipt(client, silentLogger()))

	// Use a tight context so the test doesn't wait for a full
	// dial timeout in case the closed listener still accepts.
	req := httptest.NewRequest(http.MethodGet, "/dashboard/receipts/trace-x", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want %d (body=%q)", rr.Code, http.StatusBadGateway, rr.Body.String())
	}
}
