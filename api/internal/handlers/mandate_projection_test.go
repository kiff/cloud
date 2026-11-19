package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// A person answers after list/detail reads its first snapshot. The view
// must use the refreshed exception, not just its refreshed status.
type answerDuringRead struct {
	*mandate.MemoryStore
	transition func()
}

func (s *answerDuringRead) Exception(ctx context.Context, tenant, id string) (mandate.Exception, error) {
	e, err := s.MemoryStore.Exception(ctx, tenant, id)
	if s.transition != nil {
		f := s.transition
		s.transition = nil
		f()
	}
	return e, err
}
func (s *answerDuringRead) Exceptions(ctx context.Context, tenant string, status mandate.ExceptionStatus) ([]mandate.Exception, error) {
	es, err := s.MemoryStore.Exceptions(ctx, tenant, status)
	if s.transition != nil {
		f := s.transition
		s.transition = nil
		f()
	}
	return es, err
}
func TestExceptionViewsRefreshAnswerAndMetadataTogether(t *testing.T) {
	for _, endpoint := range []string{"list", "detail"} {
		for _, answer := range []mandate.ExceptionStatus{mandate.ExceptionRejected, mandate.ExceptionConsumed} {
			t.Run(endpoint+"/"+string(answer), func(t *testing.T) {
				ctx := context.Background()
				now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
				earlier := now.Add(-time.Hour)
				store := &answerDuringRead{MemoryStore: mandate.NewMemoryStore()}
				e := mandate.Exception{ID: "e", TenantID: "t1", ProposalID: "p", Subject: "claude", Action: "refund", Status: mandate.ExceptionApproved, ResolvedBy: "alice", ResolvedAt: &earlier, AccountingVersion: 1}
				if err := store.SaveException(ctx, e); err != nil {
					t.Fatal(err)
				}
				store.transition = func() {
					e.Status, e.ResolvedBy, e.ResolvedAt = answer, "bob", &now
					e.Breaches = []mandate.Breach{{MandateID: "new-card", Limit: 123}}
					if answer == mandate.ExceptionConsumed {
						e.ConsumedAt = &now
					}
					if err := store.SaveException(ctx, e); err != nil {
						t.Fatal(err)
					}
				}
				h := NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time { return now }))
				mux := http.NewServeMux()
				h.Register(mux)
				path := "/v1/me/card-exceptions/e"
				if endpoint == "list" {
					path = "/v1/me/card-exceptions"
				}
				rr := do(t, mux, owner(), http.MethodGet, path, nil)
				if rr.Code != 200 {
					t.Fatal(rr.Code, rr.Body.String())
				}
				var v map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
					t.Fatal(err)
				}
				if endpoint == "list" {
					v = v["exceptions"].([]any)[0].(map[string]any)
				}
				if v["status"] != string(answer) || v["owner_answer"] != string(answer) || v["resolved_by"] != "bob" || v["resolved_at"] != now.Format(time.RFC3339) || v["breaches"].([]any)[0].(map[string]any)["mandate_id"] != "new-card" {
					t.Fatalf("mixed snapshot: %#v", v)
				}
				if answer == mandate.ExceptionConsumed && v["consumed_at"] != now.Format(time.RFC3339) {
					t.Fatalf("stale consumption: %#v", v)
				}
			})
		}
	}
}

type oneProjectionUnavailable struct{ *mandate.MemoryStore }

func (s *oneProjectionUnavailable) ForSubject(ctx context.Context, tenant, subject string) ([]mandate.Mandate, error) {
	if subject == "broken" {
		return nil, errors.New("one Card read failed")
	}
	return s.MemoryStore.ForSubject(ctx, tenant, subject)
}
func TestOneFailedProjectionDoesNotHideOtherHolds(t *testing.T) {
	store := &oneProjectionUnavailable{mandate.NewMemoryStore()}
	ctx := context.Background()
	for _, e := range []mandate.Exception{
		{ID: "good", TenantID: "t1", ProposalID: "p-good", Subject: "claude", Status: mandate.ExceptionHeld},
		{ID: "bad", TenantID: "t1", ProposalID: "p-bad", Subject: "broken", Status: mandate.ExceptionApproved},
	} {
		if err := store.SaveException(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	h := NewMandatesHandler(store, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions?status=held", nil)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	var got struct {
		Exceptions []struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			CanApprove  bool   `json:"can_approve_once"`
			Unavailable bool   `json:"room_unavailable"`
		}
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Exceptions) != 2 {
		t.Fatalf("hidden holds: %s", rr.Body.String())
	}
	for _, e := range got.Exceptions {
		if e.ID == "good" && e.Status != "held" {
			t.Fatal(e)
		}
		if e.ID == "bad" && (e.Status != "unavailable" || !e.Unavailable || e.CanApprove) {
			t.Fatal(e)
		}
	}
	rr = do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions/bad", nil)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
}

func TestAttributionCannotMixApprovalRevisions(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	e := mandate.Exception{Status: mandate.ExceptionConsumed, ResolvedAt: &now, AccountingVersion: 1}
	matching := membership.HoldAnswer{Answer: "approve-once", At: now, Answerer: "person-id"}
	if !matchingAttribution(e, matching, true) {
		t.Fatal("exact decision attribution omitted")
	}
	older := matching
	older.At = now.Add(-time.Second)
	later := matching
	later.At = now.Add(time.Second)
	rejected := matching
	rejected.Answer = "reject"
	for _, a := range []membership.HoldAnswer{older, later, rejected} {
		if matchingAttribution(e, a, true) {
			t.Fatalf("mixed attribution: %+v", a)
		}
	}
}
