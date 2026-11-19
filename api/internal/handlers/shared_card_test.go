package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// the mandates design amendment, "a Card can be shared", at the API: issue with
// subjects, change holders, and a statement whose lines name the agent.
func TestSharedCardAPI(t *testing.T) {
	store := mandate.NewMemoryStore()
	issued := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return issued })
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time { return now }))
	mux := http.NewServeMux()
	h.Register(mux)
	card := func(subject string, subjects ...string) map[string]any {
		b := map[string]any{
			"id": "team", "domain": "refund-control",
			"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
			"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 1000, "window": "calendar_day"}},
		}
		if subject != "" {
			b["subject"] = subject
		}
		if len(subjects) > 0 {
			b["subjects"] = subjects
		}
		return b
	}
	subjectsOf := func(rr string) string {
		var v struct {
			Subjects []string `json:"subjects"`
			Mandate  struct {
				Subjects []string `json:"subjects"`
			} `json:"mandate"`
		}
		_ = json.Unmarshal([]byte(rr), &v)
		if len(v.Subjects) == 0 {
			v.Subjects = v.Mandate.Subjects
		}
		return strings.Join(v.Subjects, ",")
	}

	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card("", "agent-a", "agent-b"))
	if rr.Code != http.StatusCreated || subjectsOf(rr.Body.String()) != "agent-a,agent-b" || !strings.Contains(rr.Body.String(), `"subject":"agent-a"`) {
		t.Fatalf("issue shared: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card("agent-b", "agent-a", "agent-b")); rr.Code != http.StatusBadRequest {
		t.Errorf("subject not first: %d", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card("", "agent-a", "agent\u0001b")); rr.Code != http.StatusBadRequest {
		t.Errorf("bad holder id: %d", rr.Code)
	}
	// A reissue naming only the subject keeps the Card shared.
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card("agent-a")); subjectsOf(rr.Body.String()) != "agent-a,agent-b" {
		t.Fatalf("subject-only reissue: %s", rr.Body.String())
	}

	current := []string{"agent-a", "agent-b"}
	put := func(as string, subjects ...string) *httptest.ResponseRecorder {
		id := owner()
		if as == "runtime" {
			id = runtimeKey()
		}
		return do(t, mux, id, http.MethodPut, "/v1/me/mandates/team/holders", map[string]any{"subjects": subjects, "expected": current})
	}
	// a review: the list the change was made from is required, and
	// a stale one is refused.
	if rr := do(t, mux, owner(), http.MethodPut, "/v1/me/mandates/team/holders", map[string]any{"subjects": []string{"agent-a"}}); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "expected is required") {
		t.Errorf("no expected: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPut, "/v1/me/mandates/team/holders", map[string]any{"subjects": []string{"agent-a"}, "expected": []string{"agent-a"}}); rr.Code != http.StatusPreconditionFailed {
		t.Errorf("stale expected: %d %s", rr.Code, rr.Body.String())
	}
	if rr := put("runtime", "agent-a"); rr.Code != http.StatusForbidden {
		t.Errorf("runtime key: %d", rr.Code)
	}
	if rr := put("owner", "agent-a", "agent-b"); rr.Code != http.StatusConflict {
		t.Errorf("unchanged: %d", rr.Code)
	}
	if rr := put("owner", "agent-a", "agent-a"); rr.Code != http.StatusBadRequest {
		t.Errorf("duplicate: %d", rr.Code)
	}
	if rr := put("owner"); rr.Code != http.StatusBadRequest {
		t.Errorf("empty: %d", rr.Code)
	}
	if rr := put("owner", "agent-a", "agent-b", "agent-c"); rr.Code != http.StatusOK || subjectsOf(rr.Body.String()) != "agent-a,agent-b,agent-c" {
		t.Fatalf("add a holder: %d %s", rr.Code, rr.Body.String())
	}

	// Two holders draw; the statement names each line's agent and
	// subtotals by holder.
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	for i, agent := range []string{"agent-a", "agent-b", "agent-a"} {
		d, err := a.Authorize(context.Background(), "t1", "p-"+string(rune('1'+i)), mandate.Request{
			Subject: agent, Domain: "refund-control", DomainID: "refund-control", Action: "AUTO_REFUND", EntityState: "PAID", Parameters: map[string]int64{"amount": 100}})
		if err != nil || d.Outcome != mandate.OutcomeWithinLimit {
			t.Fatalf("draw by %s: %+v %v", agent, d, err)
		}
	}
	st := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/team/statement", nil)
	var out struct {
		Draws    []map[string]any            `json:"draws"`
		ByHolder map[string]map[string]int64 `json:"by_holder"`
	}
	if err := json.Unmarshal(st.Body.Bytes(), &out); err != nil {
		t.Fatalf("statement: %s", st.Body.String())
	}
	if len(out.Draws) != 3 || out.Draws[0]["subject"] == nil || out.ByHolder["agent-a"]["sum(amount)"] != 200 || out.ByHolder["agent-b"]["sum(amount)"] != 100 {
		t.Fatalf("statement by holder: %+v %+v", out.ByHolder, out.Draws)
	}
}

// Invariant: an agent's own key cannot issue, share or answer for a
// Card, even when it holds the admin role.
// Category: authority
// Attack: mint an admin-role key bound to the agent, then issue itself a Card, add itself to another agent's Card, or approve its own held action
// Refused: managing Cards and answering for them require the owner's or an admin's own session or key, never one bound to an agent
func TestAgentKeyCannotManageCards(t *testing.T) {
	store := mandate.NewMemoryStore()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time { return now }))
	mux := http.NewServeMux()
	h.Register(mux)
	agentAdmin := owner()
	agentAdmin.Roles = []string{"admin"}
	agentAdmin.AgentID = "agent-a"
	card := map[string]any{"id": "c", "subject": "agent-a", "domain": "refund-control",
		"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
		"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 1000, "window": "calendar_day"}}}
	if rr := do(t, mux, agentAdmin, http.MethodPost, "/v1/me/mandates", card); rr.Code != http.StatusForbidden {
		t.Errorf("agent key issued itself a Card: %d", rr.Code)
	}
	card["subject"] = "agent-b"
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", card); rr.Code != http.StatusCreated {
		t.Fatalf("owner issue: %d", rr.Code)
	}
	if rr := do(t, mux, agentAdmin, http.MethodPut, "/v1/me/mandates/c/holders", map[string]any{"subjects": []string{"agent-b", "agent-a"}, "expected": []string{"agent-b"}}); rr.Code != http.StatusForbidden {
		t.Errorf("agent key added itself to a Card: %d", rr.Code)
	}
	if rr := do(t, mux, agentAdmin, http.MethodPost, "/v1/me/card-exceptions/x/approve-once", nil); rr.Code != http.StatusForbidden {
		t.Errorf("agent key answered for a Card: %d", rr.Code)
	}
}
