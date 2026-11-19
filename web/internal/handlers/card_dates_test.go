package handlers_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// Changing a Card in a form never changes when it starts, and the tool
// Card form, which shows no dates, never changes when it ends. Omitted,
// the API records no start (a Card dated to start later is in force at
// once) and no end (a dated Card never ends).
func TestCardFormsKeepTheCardsDates(t *testing.T) {
	t.Parallel()
	from := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	until := time.Now().Add(240 * time.Hour).UTC().Truncate(time.Second)
	api := &cardAPI{cards: []map[string]any{
		{"id": "refund-agent-issue-refund", "subject": "refund-agent", "domain": "gateway", "active": true,
			"grants":     []any{map[string]any{"action": "issue_refund"}},
			"aggregates": []any{map[string]any{"quantity": "count", "limit": 5, "window": "calendar_day"}},
			"valid_from": from, "valid_until": until},
		{"id": "limit-refund-agent", "subject": "refund-agent", "domain": "refunds", "active": true,
			"grants":     []any{map[string]any{"action": "ISSUE_REFUND"}},
			"aggregates": []any{map[string]any{"quantity": "count", "limit": 3, "window": "calendar_day"}},
			"valid_from": from},
	}}
	h, c := toolCardRig(t, api)

	tool := url.Values{"agent": {"refund-agent"}, "calls": {"9"}, "window": {"calendar_day"}, "on_exceed": {"refuse"}}
	followFlashTo(t, h, c, postForm(h, c, "/tools/issue_refund/card", tool), "/cards/refund-agent-issue-refund")
	api.mu.Lock()
	got := api.issued[len(api.issued)-1]
	api.mu.Unlock()
	if got["id"] != "refund-agent-issue-refund" || got["valid_from"] != from.Format(time.RFC3339) || got["valid_until"] != until.Format(time.RFC3339) {
		t.Fatalf("tool Card form changed the dates: %v", got)
	}

	general := url.Values{"id": {"limit-refund-agent"}, "subject": {"refund-agent"}, "domain": {"refunds"}, "actions": {"ISSUE_REFUND"},
		"quantity_kind": {"count"}, "limit": {"4"}, "window": {"calendar_day"}}
	if rec := postForm(h, c, "/cards", general); rec.Code != http.StatusSeeOther {
		t.Fatalf("general form: %d %s", rec.Code, rec.Body.String())
	}
	api.mu.Lock()
	got = api.issued[len(api.issued)-1]
	api.mu.Unlock()
	if got["id"] != "limit-refund-agent" || got["valid_from"] != from.Format(time.RFC3339) {
		t.Fatalf("general form changed the start: %v", got)
	}
}
