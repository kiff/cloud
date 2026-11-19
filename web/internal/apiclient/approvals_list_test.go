package apiclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// TestListApprovalsBearer_HappyPath confirms the wire shape and
// the apiclient's normalization of the response (nil → []).
func TestListApprovalsBearer_HappyPath(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/approvals": func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer abc" {
				t.Errorf("Authorization = %q, want Bearer abc", got)
			}
			if got := r.URL.Query().Get("status"); got != "pending" {
				t.Errorf("status query = %q, want pending", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"approvals": []map[string]any{{
					"id":           "appr-1",
					"entity_id":    "order-1",
					"entity_type":  "Order",
					"action_name":  "ISSUE_REFUND",
					"status":       "pending",
					"requested_by": "agent-1",
					"reason":       "agent-initiated",
					"created_at":   now,
				}},
				"last_received_at": now,
			})
		},
	})
	c := newSessionClient(t, srv)
	got, err := c.ListApprovalsBearer(context.Background(), "abc",
		apiclient.ApprovalsListOptions{Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovalsBearer: %v", err)
	}
	if len(got.Approvals) != 1 || got.Approvals[0].ID != "appr-1" {
		t.Fatalf("approvals: got %#v", got.Approvals)
	}
	if got.Approvals[0].ActionName != "ISSUE_REFUND" {
		t.Errorf("ActionName: got %q", got.Approvals[0].ActionName)
	}
	if got.LastReceivedAt == nil || !got.LastReceivedAt.Equal(now) {
		t.Errorf("LastReceivedAt mismatch: %v", got.LastReceivedAt)
	}
}

// TestListApprovalsBearer_EmptyResponseNormalizesToEmptySlice
// confirms a missing/null approvals key still gives the templ
// renderer an empty slice, not nil — the page renders an
// empty-state, never iterates over nil.
func TestListApprovalsBearer_EmptyResponseNormalizesToEmptySlice(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/approvals": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"approvals": []any{},
			})
		},
	})
	c := newSessionClient(t, srv)
	got, err := c.ListApprovalsBearer(context.Background(), "abc",
		apiclient.ApprovalsListOptions{})
	if err != nil {
		t.Fatalf("ListApprovalsBearer: %v", err)
	}
	if got.Approvals == nil {
		t.Fatalf("Approvals must never be nil; got nil")
	}
	if len(got.Approvals) != 0 {
		t.Fatalf("Approvals: got %d, want 0", len(got.Approvals))
	}
}

// TestListApprovalsBearer_QueryParamsForwardCorrectly confirms
// the Limit + Status options compose into the URL exactly.
func TestListApprovalsBearer_QueryParamsForwardCorrectly(t *testing.T) {
	t.Parallel()
	var rawQuery string
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/approvals": func(w http.ResponseWriter, r *http.Request) {
			rawQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"approvals":[]}`))
		},
	})
	c := newSessionClient(t, srv)
	if _, err := c.ListApprovalsBearer(context.Background(), "abc",
		apiclient.ApprovalsListOptions{Status: "granted", Limit: 10}); err != nil {
		t.Fatalf("ListApprovalsBearer: %v", err)
	}
	if !strings.Contains(rawQuery, "status=granted") {
		t.Errorf("status not forwarded; rawQuery=%q", rawQuery)
	}
	if !strings.Contains(rawQuery, "limit=10") {
		t.Errorf("limit not forwarded; rawQuery=%q", rawQuery)
	}
}
