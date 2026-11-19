package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The tool Card form sets the answers besides approve and reject.
func TestToolCard_SetsNextSteps(t *testing.T) {
	t.Parallel()
	api := &cardAPI{}
	h, c := toolCardRig(t, api)
	form := url.Values{"agent": {"refund-agent"}, "per_call": {"500"}, "total": {"2000"},
		"window": {"calendar_day"}, "on_exceed": {"owner"}, "hold": {"24h"}, "next_steps": {"Return first, proof-first, return first"}}
	if rec := postForm(h, c, "/tools/issue_refund/card", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("issue = %d\n%s", rec.Code, rec.Body.String())
	}
	g := api.issued[0]["grants"].([]any)[0].(map[string]any)
	if got := g["next_steps"]; got == nil || len(got.([]any)) != 2 || got.([]any)[0] != "return_first" || got.([]any)[1] != "proof_first" {
		t.Fatalf("next_steps = %v", got)
	}

	// A Card that refuses has no one to answer: no steps are sent.
	form.Set("on_exceed", "refuse")
	postForm(h, c, "/tools/issue_refund/card", form)
	if g := api.issued[1]["grants"].([]any)[0].(map[string]any); g["next_steps"] != nil {
		t.Fatalf("refuse card sent steps: %v", g["next_steps"])
	}
}

// Editing a Card shows its steps, and clearing the field clears them; a
// form without the field keeps them.
func TestToolCard_EditShowsAndChangesNextSteps(t *testing.T) {
	t.Parallel()
	existing := map[string]any{"id": "refund-agent-issue-refund", "subject": "refund-agent", "domain": "gateway", "on_exceed": "owner",
		"grants":     []any{map[string]any{"action": "issue_refund", "next_steps": []any{"return_first"}}},
		"aggregates": []any{map[string]any{"quantity": "count", "limit": 5, "window": "calendar_day"}}}
	api := &cardAPI{cards: []map[string]any{existing}}
	h, c := toolCardRig(t, api)
	req := httptest.NewRequest(http.MethodGet, "/tools/issue_refund/card?agent=refund-agent", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `name="next_steps" value="return_first"`) {
		t.Fatalf("edit form does not show the steps:\n%s", rec.Body.String())
	}
	base := url.Values{"agent": {"refund-agent"}, "calls": {"5"}, "window": {"calendar_day"}, "on_exceed": {"owner"}, "hold": {"24h"}}

	cleared := url.Values{}
	for k, v := range base {
		cleared[k] = v
	}
	cleared.Set("next_steps", "")
	postForm(h, c, "/tools/issue_refund/card", cleared)
	if g := api.issued[0]["grants"].([]any)[0].(map[string]any); g["next_steps"] != nil {
		t.Fatalf("cleared field kept steps: %v", g["next_steps"])
	}
	postForm(h, c, "/tools/issue_refund/card", base)
	if g := api.issued[1]["grants"].([]any)[0].(map[string]any); g["next_steps"] == nil {
		t.Fatal("a form without the field dropped the Card's steps")
	}
}

// a review: saving a Card without touching its other answers keeps their
// exact identifiers, repeated and trailing underscores included.
func TestToolCard_UnchangedStepsRoundTripExactly(t *testing.T) {
	t.Parallel()
	steps := []any{"return_first", "return__first", "proof_first_"}
	existing := map[string]any{"id": "refund-agent-issue-refund", "subject": "refund-agent", "domain": "gateway", "on_exceed": "owner",
		"grants":     []any{map[string]any{"action": "issue_refund", "next_steps": steps}},
		"aggregates": []any{map[string]any{"quantity": "count", "limit": 5, "window": "calendar_day"}}}
	api := &cardAPI{cards: []map[string]any{existing}}
	h, c := toolCardRig(t, api)
	req := httptest.NewRequest(http.MethodGet, "/tools/issue_refund/card?agent=refund-agent", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	const shown = "return_first, return__first, proof_first_"
	if !strings.Contains(rec.Body.String(), `name="next_steps" value="`+shown+`"`) {
		t.Fatalf("the form must show the identifiers as they are:\n%s", rec.Body.String())
	}
	// Only the limit changes; the field is posted back as it was shown.
	form := url.Values{"agent": {"refund-agent"}, "calls": {"9"}, "window": {"calendar_day"}, "on_exceed": {"owner"}, "hold": {"24h"},
		"next_steps": {shown}}
	postForm(h, c, "/tools/issue_refund/card", form)
	got := api.issued[0]["grants"].([]any)[0].(map[string]any)["next_steps"].([]any)
	if len(got) != 3 || got[0] != "return_first" || got[1] != "return__first" || got[2] != "proof_first_" {
		t.Fatalf("round trip changed the steps: %v", got)
	}
}
