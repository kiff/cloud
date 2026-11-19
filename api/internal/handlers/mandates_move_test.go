package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

func seedCard(t *testing.T, store *mandate.MemoryStore, revoked *time.Time) {
	t.Helper()
	if err := store.Issue(context.Background(), mandate.Mandate{
		ID: "card-1", TenantID: "t1", Subject: "agent://a",
		Grants:     []mandate.Grant{{Action: "REFUND_INVOICE"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 5, Window: mandate.WindowCalendarDay}},
		RevokedAt:  revoked,
	}, "owner@x"); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// Moving a Card keeps its id and terms and records the new holder on a
// new revision (the mandates design amendment, "Moving a Card").
func TestMoveGivesTheCardToANewHolder(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	seedCard(t, store, nil)

	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates/card-1/move", map[string]string{"subject": "agent://b"})
	if rr.Code != http.StatusOK {
		t.Fatalf("move = %d %s", rr.Code, rr.Body.String())
	}
	ms, _ := store.List(context.Background(), "t1")
	if len(ms) != 1 || ms[0].ID != "card-1" || ms[0].Subject != "agent://b" || ms[0].Aggregates[0].Limit != 5 {
		t.Fatalf("after move: %+v", ms)
	}
	revs, _ := store.Revisions(context.Background(), "t1", "card-1")
	if len(revs) != 2 || revs[0].Subject != "agent://a" || revs[1].Subject != "agent://b" || revs[1].By != "alice" {
		t.Fatalf("revisions: %+v", revs)
	}
	st := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/card-1/statement", nil)
	if st.Code != http.StatusOK || !strings.Contains(st.Body.String(), `"subject":"agent://a"`) || !strings.Contains(st.Body.String(), `"subject":"agent://b"`) {
		t.Fatalf("statement revisions should name each holder: %s", st.Body.String())
	}
}

func TestMoveRefusals(t *testing.T) {
	revoked := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		revoked *time.Time
		path    string
		body    any
		asKey   bool
		want    int
	}{
		{"runtime key", nil, "/v1/me/mandates/card-1/move", map[string]string{"subject": "agent://b"}, true, http.StatusForbidden},
		{"no subject", nil, "/v1/me/mandates/card-1/move", map[string]string{"subject": " "}, false, http.StatusBadRequest},
		{"unknown card", nil, "/v1/me/mandates/nope/move", map[string]string{"subject": "agent://b"}, false, http.StatusNotFound},
		{"same holder", nil, "/v1/me/mandates/card-1/move", map[string]string{"subject": "agent://a"}, false, http.StatusConflict},
		{"revoked card", &revoked, "/v1/me/mandates/card-1/move", map[string]string{"subject": "agent://b"}, false, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, store := mandatesServer(t, owner())
			seedCard(t, store, tc.revoked)
			who := owner()
			if tc.asKey {
				who = runtimeKey()
			}
			if rr := do(t, mux, who, http.MethodPost, tc.path, tc.body); rr.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
			ms, _ := store.List(context.Background(), "t1")
			if ms[0].Subject != "agent://a" {
				t.Fatalf("a refused move changed the holder to %s", ms[0].Subject)
			}
		})
	}
}

// Per-action limits go in through the issue API and come back out of
// the Card view.
func TestIssueWithPerActionLimit(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)

	body := map[string]any{
		"id": "card-pa", "subject": "agent://a", "domain": "refund-control",
		"grants": []map[string]any{{"action": "AUTO_REFUND",
			"per_action_max": []map[string]any{{"quantity": "sum(amount)", "max": 50000}}}},
		"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 500000, "window": "calendar_day"}},
	}
	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("issue = %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"per_action_max":[{"max":50000,"quantity":"sum(amount)","quantity_of":{"kind":"sum","parameter":"amount"}}]`) {
		t.Fatalf("view should carry the per-action limit: %s", rr.Body.String())
	}
	ms, _ := store.List(context.Background(), "t1")
	if len(ms) != 1 || len(ms[0].Grants[0].PerAction) != 1 || ms[0].Grants[0].PerAction[0].Max != 50000 {
		t.Fatalf("stored: %+v", ms)
	}

	bad := body
	bad["grants"] = []map[string]any{{"action": "AUTO_REFUND",
		"per_action_max": []map[string]any{{"quantity": "count", "max": 1}}}}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", bad); rr.Code != http.StatusBadRequest {
		t.Fatalf("a per-action count must be refused, got %d %s", rr.Code, rr.Body.String())
	}
}

// a review: a change to a per-action limit is visible in the
// statement's revision history, old value and new.
func TestHistoryShowsPerActionChanges(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	cap := func(max int64) []mandate.Grant {
		return []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: max}}}}
	}
	m := mandate.Mandate{ID: "card-history", TenantID: "t1", Subject: "agent://a", Grants: cap(50000),
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 500000, Window: mandate.WindowCalendarDay}}}
	if err := store.Issue(context.Background(), m, "owner"); err != nil {
		t.Fatal(err)
	}
	m.Grants = cap(10000)
	if err := store.Issue(context.Background(), m, "owner"); err != nil {
		t.Fatal(err)
	}
	m.Grants = []mandate.Grant{{Action: "REFUND_INVOICE"}}
	if err := store.Issue(context.Background(), m, "owner"); err != nil {
		t.Fatal(err)
	}
	rec := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/card-history/statement", nil)
	var body struct {
		Revisions []struct {
			Grants []struct {
				PerActionMax []struct {
					Max int64 `json:"max"`
				} `json:"per_action_max"`
			} `json:"grants"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Revisions) != 3 {
		t.Fatalf("status %d, %v: %s", rec.Code, err, rec.Body.String())
	}
	want := []int64{50000, 10000, 0}
	for i, rev := range body.Revisions {
		var got int64
		if len(rev.Grants) == 1 && len(rev.Grants[0].PerActionMax) == 1 {
			got = rev.Grants[0].PerActionMax[0].Max
		}
		if got != want[i] {
			t.Errorf("revision %d per-action = %d, want %d (0 = none)", i+1, got, want[i])
		}
	}
}
