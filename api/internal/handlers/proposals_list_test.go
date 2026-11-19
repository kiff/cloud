package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/proposals"
)

// proposalListRig is the unit-test rig for GET /v1/proposals.
// It bypasses the proposal-gate runtime path entirely and seeds
// rows directly into a proposals.MemoryReader. This tests the
// wire shape and tenant-scoping without requiring the framework
// audit store. Postgres-backed integration tests live alongside
// usage_e2e_test.go and exercise the real PostgresReader.
type proposalListRig struct {
	mux      http.Handler
	reader   *proposals.MemoryReader
	tenantID string
}

func newProposalListRig(t *testing.T, tenantID string) *proposalListRig {
	t.Helper()
	reader := proposals.NewMemoryReader()
	// Use a passthrough TenantRuntimeAccessor; the list path
	// never calls into it. Build the handler with WithProposalsReader
	// only.
	h := handlers.NewProposalsHandler(nil, handlers.WithProposalsReader(reader))
	innerMux := http.NewServeMux()
	h.Register(innerMux)

	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  tenantID,
		Subject: "test-subject",
	}))

	return &proposalListRig{
		mux:      wrapped,
		reader:   reader,
		tenantID: tenantID,
	}
}

// driveListGet runs GET /v1/proposals with the optional limit
// query string and decodes the response into a typed shape so
// tests can assert on individual fields.
func driveListGet(t *testing.T, mux http.Handler, query string) (*httptest.ResponseRecorder, listResponseDecoded) {
	t.Helper()
	url := "/v1/proposals"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var decoded listResponseDecoded
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode list response: %v\nbody=%s", err, rec.Body.String())
		}
	}
	return rec, decoded
}

// listResponseDecoded mirrors the on-wire shape so tests can
// assert without depending on the handler's private response
// type. Fields use the same JSON tags as proposalsListResponse.
type listResponseDecoded struct {
	Proposals      []proposals.DecisionRow `json:"proposals"`
	LastReceivedAt *time.Time              `json:"last_received_at,omitempty"`
}

// fixed produces a UTC time at a deterministic offset; helpers
// return it sorted-friendly so DESC ordering tests are obvious.
func fixed(minute int) time.Time {
	return time.Date(2026, 5, 26, 12, minute, 0, 0, time.UTC)
}

// TestProposalsList_AllOutcomeShapesRoundTrip seeds one row per
// outcome and asserts every outcome string survives the wire.
// Acceptance criterion: "All five outcomes appear: allowed,
// approval_required, blocked, invalid, limit_exceeded."
func TestProposalsList_AllOutcomeShapesRoundTrip(t *testing.T) {
	t.Parallel()
	rig := newProposalListRig(t, "ten-1")
	outcomes := []string{"allowed", "approval_required", "blocked", "invalid", "limit_exceeded"}
	for i, oc := range outcomes {
		rig.reader.Append(rig.tenantID, proposals.DecisionRow{
			ProposalID:    "prop-" + oc,
			EntityID:      "order-" + oc,
			EntityType:    "Order",
			ActionName:    "AUTO_REFUND",
			Outcome:       oc,
			CreatedAt:     fixed(i * 5),
			ReceiptStatus: "none",
		})
	}

	rec, decoded := driveListGet(t, rig.mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(decoded.Proposals) != len(outcomes) {
		t.Fatalf("got %d rows, want %d", len(decoded.Proposals), len(outcomes))
	}
	got := make(map[string]bool, len(outcomes))
	for _, p := range decoded.Proposals {
		got[p.Outcome] = true
	}
	for _, oc := range outcomes {
		if !got[oc] {
			t.Errorf("outcome %q missing from response", oc)
		}
	}
}

// TestProposalsList_TenantScopingHidesCrossTenantRows seeds rows
// against tenant-A and tenant-B, then asserts a request from
// tenant-B sees only its own rows. Acceptance criterion: "A row
// from tenant A must never appear in tenant B's response under
// any condition."
func TestProposalsList_TenantScopingHidesCrossTenantRows(t *testing.T) {
	t.Parallel()
	rig := newProposalListRig(t, "ten-b")
	rig.reader.Append("ten-a", proposals.DecisionRow{
		ProposalID: "prop-a", Outcome: "allowed", CreatedAt: fixed(0), ReceiptStatus: "none",
	})
	rig.reader.Append("ten-b", proposals.DecisionRow{
		ProposalID: "prop-b1", Outcome: "blocked", CreatedAt: fixed(5), ReceiptStatus: "none",
	})
	rig.reader.Append("ten-b", proposals.DecisionRow{
		ProposalID: "prop-b2", Outcome: "approval_required", CreatedAt: fixed(10), ReceiptStatus: "none",
	})

	_, decoded := driveListGet(t, rig.mux, "")
	if len(decoded.Proposals) != 2 {
		t.Fatalf("got %d rows, want 2 (tenant-A leaked?)", len(decoded.Proposals))
	}
	for _, p := range decoded.Proposals {
		if p.ProposalID == "prop-a" {
			t.Fatalf("tenant-A's row leaked into tenant-B's response: %+v", p)
		}
	}
}

func TestProposalsList_DomainBoundKeyFailsClosed(t *testing.T) {
	t.Parallel()
	reader := proposals.NewMemoryReader()
	reader.Append("ten-1", proposals.DecisionRow{
		ProposalID: "prop-private",
		EntityID:   "order-1",
		EntityType: "Order",
		ActionName: "REFUND",
		Outcome:    "allowed",
		CreatedAt:  fixed(0),
	})
	h := handlers.NewProposalsHandler(nil, handlers.WithProposalsReader(reader))
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := withFakeIdentity(mux, auth.Identity{
		Tenant:   "ten-1",
		Subject:  "runtime-key",
		DomainID: "refunds",
	})

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/proposals", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "prop-private") {
		t.Fatal("domain-bound response disclosed a proposal")
	}
}

// TestProposalsList_NoParametersOnWire asserts the response body
// never contains the request `parameters` map. The MemoryReader
// surface doesn't even carry parameters; this test is a backstop
// in case a future change adds a Parameters field to DecisionRow.
// The check is a literal substring search for the sentinel value
// the test would have set, so it catches both struct-level
// regressions and accidental json.Marshal of an embedded type.
//
// Acceptance criterion: "no `parameters` leakage."
func TestProposalsList_NoParametersOnWire(t *testing.T) {
	t.Parallel()
	rig := newProposalListRig(t, "ten-1")
	// Seed a row whose action name and entity_id contain
	// reasonable values; the test then asserts no occurrence of
	// "parameters" anywhere in the JSON response.
	rig.reader.Append(rig.tenantID, proposals.DecisionRow{
		ProposalID: "prop-1", EntityID: "order-1", EntityType: "Order",
		ActionName: "AUTO_REFUND", Outcome: "allowed",
		CreatedAt: fixed(0), ReceiptStatus: "none",
	})

	rec, _ := driveListGet(t, rig.mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, banned := range []string{`"parameters"`, `"Parameters"`} {
		if strings.Contains(body, banned) {
			t.Errorf("response leaked %q (request parameters must not appear on this surface): %s", banned, body)
		}
	}
}

// TestProposalsList_NoReasoningSummaryOnWire mirrors the
// parameters guardrail. The `reasoning_summary` field is
// caller-supplied and may carry sensitive content; it must not
// appear on this surface.
//
// Acceptance criterion: "no `reasoning_summary` leakage."
func TestProposalsList_NoReasoningSummaryOnWire(t *testing.T) {
	t.Parallel()
	rig := newProposalListRig(t, "ten-1")
	rig.reader.Append(rig.tenantID, proposals.DecisionRow{
		ProposalID: "prop-1", EntityID: "order-1", EntityType: "Order",
		ActionName: "AUTO_REFUND", Outcome: "allowed",
		CreatedAt: fixed(0), ReceiptStatus: "none",
	})

	rec, _ := driveListGet(t, rig.mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, banned := range []string{`"reasoning_summary"`, `"reasoningSummary"`, `"ReasoningSummary"`} {
		if strings.Contains(body, banned) {
			t.Errorf("response leaked %q: %s", banned, body)
		}
	}
}

// TestProposalsList_LimitClamping mirrors the receipts endpoint
// posture: empty / zero / negative / non-numeric all default to
// DefaultLimit; over-cap silently truncates to MaxLimit (no
// 400). The handler does the clamping in parseLimit; the
// MemoryReader honors the limit it receives.
func TestProposalsList_LimitClamping(t *testing.T) {
	t.Parallel()
	rig := newProposalListRig(t, "ten-1")
	for i := 0; i < 150; i++ {
		rig.reader.Append(rig.tenantID, proposals.DecisionRow{
			ProposalID:    "prop-" + jitID(i),
			Outcome:       "allowed",
			CreatedAt:     fixed(i),
			ReceiptStatus: "none",
		})
	}

	cases := []struct {
		name      string
		query     string
		wantCount int
	}{
		{"default", "", proposals.DefaultLimit},
		{"explicit small", "limit=5", 5},
		{"zero defaults to 20", "limit=0", proposals.DefaultLimit},
		{"negative defaults to 20", "limit=-7", proposals.DefaultLimit},
		{"non-numeric defaults to 20", "limit=foo", proposals.DefaultLimit},
		{"over-cap clamps to 100", "limit=999", proposals.MaxLimit},
		{"explicit cap", "limit=100", proposals.MaxLimit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, decoded := driveListGet(t, rig.mux, c.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d", rec.Code)
			}
			if len(decoded.Proposals) != c.wantCount {
				t.Errorf("got %d rows, want %d", len(decoded.Proposals), c.wantCount)
			}
		})
	}
}

// TestProposalsList_LastReceivedAtPresence asserts the contract
// the dashboard's header reads:
//
//   - When at least one row matches, last_received_at is present
//     and equals proposals[0].created_at (the most recent row).
//   - When zero rows match, last_received_at is absent (not
//     null, not zero — absent — so the dashboard can branch on
//     "field missing == not connected yet" without a second
//     call).
func TestProposalsList_LastReceivedAtPresence(t *testing.T) {
	t.Parallel()

	t.Run("absent on empty", func(t *testing.T) {
		t.Parallel()
		rig := newProposalListRig(t, "ten-empty")
		rec, _ := driveListGet(t, rig.mux, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "last_received_at") {
			t.Errorf("empty result must omit last_received_at; body=%s", body)
		}
	})

	t.Run("matches latest row", func(t *testing.T) {
		t.Parallel()
		rig := newProposalListRig(t, "ten-latest")
		latest := fixed(30)
		rig.reader.Append(rig.tenantID, proposals.DecisionRow{
			ProposalID: "p-old", Outcome: "allowed", CreatedAt: fixed(0), ReceiptStatus: "none",
		})
		rig.reader.Append(rig.tenantID, proposals.DecisionRow{
			ProposalID: "p-new", Outcome: "allowed", CreatedAt: latest, ReceiptStatus: "none",
		})

		rec, decoded := driveListGet(t, rig.mux, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d", rec.Code)
		}
		if decoded.LastReceivedAt == nil {
			t.Fatalf("last_received_at missing on populated result")
		}
		if !decoded.LastReceivedAt.Equal(latest) {
			t.Errorf("last_received_at = %s, want %s (proposals[0].created_at)",
				decoded.LastReceivedAt, latest)
		}
		if !decoded.Proposals[0].CreatedAt.Equal(latest) {
			t.Errorf("proposals[0] is not the latest row; got %s", decoded.Proposals[0].CreatedAt)
		}
	})
}

// TestProposalsList_ExecutedVsNonExecutedReceipt asserts the
// receipt-correctness contract: a non-executed outcome leaves
// receipt_trace_id null and receipt_status="none". An executed
// proposal that matches a stored receipt surfaces both fields.
//
// The MemoryReader doesn't simulate the cloud_meta.receipts
// join, so this test seeds the row with the enrichment already
// applied — which is the contract the PostgresReader exposes.
// The Postgres integration test below proves the join works.
//
// Acceptance criterion: "executed-vs-non-executed
// receipt_trace_id correctness."
func TestProposalsList_ExecutedVsNonExecutedReceipt(t *testing.T) {
	t.Parallel()
	rig := newProposalListRig(t, "ten-1")

	rig.reader.Append(rig.tenantID, proposals.DecisionRow{
		ProposalID: "prop-blocked", Outcome: "blocked",
		CreatedAt: fixed(0), ReceiptStatus: "none",
	})
	rig.reader.Append(rig.tenantID, proposals.DecisionRow{
		ProposalID: "prop-allowed", Outcome: "allowed",
		CreatedAt:      fixed(5),
		ReceiptStatus:  "anchored",
		ReceiptTraceID: "prop-allowed",
		AnchorTxHash:   "0xdeadbeef",
	})

	_, decoded := driveListGet(t, rig.mux, "")
	if len(decoded.Proposals) != 2 {
		t.Fatalf("got %d rows, want 2", len(decoded.Proposals))
	}

	byID := map[string]proposals.DecisionRow{}
	for _, p := range decoded.Proposals {
		byID[p.ProposalID] = p
	}

	blocked := byID["prop-blocked"]
	if blocked.ReceiptTraceID != "" {
		t.Errorf("blocked proposal must have empty receipt_trace_id; got %q", blocked.ReceiptTraceID)
	}
	if blocked.ReceiptStatus != "none" {
		t.Errorf("blocked proposal receipt_status: got %q, want %q", blocked.ReceiptStatus, "none")
	}
	if blocked.AnchorTxHash != "" {
		t.Errorf("blocked proposal anchor_tx_hash must be empty; got %q", blocked.AnchorTxHash)
	}

	allowed := byID["prop-allowed"]
	if allowed.ReceiptTraceID != "prop-allowed" {
		t.Errorf("executed proposal receipt_trace_id: got %q, want %q", allowed.ReceiptTraceID, "prop-allowed")
	}
	if allowed.ReceiptStatus != "anchored" {
		t.Errorf("executed proposal receipt_status: got %q, want %q", allowed.ReceiptStatus, "anchored")
	}
	if allowed.AnchorTxHash != "0xdeadbeef" {
		t.Errorf("executed proposal anchor_tx_hash: got %q, want %q", allowed.AnchorTxHash, "0xdeadbeef")
	}
}

// TestProposalsList_RequiresIdentity confirms the auth gate:
// a request with no identity on the context returns 401 before
// reaching the reader.
func TestProposalsList_RequiresIdentity(t *testing.T) {
	t.Parallel()
	reader := proposals.NewMemoryReader()
	h := handlers.NewProposalsHandler(nil, handlers.WithProposalsReader(reader))
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/proposals", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", rec.Code)
	}
}

// TestProposalsList_ReaderUnconfiguredReturns503 asserts the
// in-memory smoke path's behaviour: when no Reader is wired,
// the route returns 503 instead of pretending to be empty. The
// dashboard can branch on this and fall through to receipts/
// usage instead of rendering an authoritative-looking empty
// table.
func TestProposalsList_ReaderUnconfiguredReturns503(t *testing.T) {
	t.Parallel()
	// No WithProposalsReader — handler holds nil reader.
	h := handlers.NewProposalsHandler(nil)
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  "ten-1",
		Subject: "test-subject",
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/proposals", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", rec.Code)
	}
}

// jitID returns a short stable ID for fixture rows.
func jitID(i int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 4)
	for j := range out {
		out[j] = hex[(i+j)%16]
	}
	return string(out)
}

// silence unused-import detection for context (used in helpers
// expansion if needed in a follow-up).
var _ = context.TODO
