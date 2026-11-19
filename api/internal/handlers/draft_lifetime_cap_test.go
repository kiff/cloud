package handlers

import (
	"net/http"
	"strings"
	"testing"
)

// A runtime drafts "one reply per ticket, ever" as one count, lifetime, on
// the tool: signed so, its taf stops after its first ticket. Every draft is
// made here, the product's and the Eval's alike, so it's refused here, at the
// submit and the preview, with the words that send Coach back: a total per
// the action's resource argument. A count per item, a lifetime budget and a
// day's count are drafted as asked.
func TestADraftNeverCountsAWriteOnceInItsWholeLife(t *testing.T) {
	r := newDraftRigOn(t, stubDomainSource{yaml: replyYAML})
	daily := map[string]any{"quantity": "count", "limit": 20, "window": "calendar_day"}
	reply := func(aggs ...map[string]any) map[string]any {
		c := replyCard()
		c["subject"] = "rae"
		c["aggregates"] = aggs
		return c
	}
	once := map[string]any{"quantity": "count", "limit": 1, "window": "lifetime"}
	want := `card 0: it counts post_reply 1 in its whole life, across every item, so the taf would stop after its first; a limit for each item is a total per ticket_id: {"quantity": "count", "window": "lifetime", "limit": 1, "per": "ticket_id"}, beside the day's total`
	for _, path := range []string{"/v1/me/agent-drafts", "/v1/me/agent-drafts/preview"} {
		rr := do(t, r.mux, admin(), http.MethodPost, path, map[string]any{"agent_id": "rae", "cards": []any{reply(daily, once)}})
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), strings.ReplaceAll(want, `"`, `\"`)) {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
	}
	// A write with no item to count per: the limit goes in the instructions.
	refund := card("rae-refund", "rae")
	refund["aggregates"] = []map[string]any{{"quantity": "sum(amount_cents)", "limit": 200000, "window": "calendar_day"}, once}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/preview", map[string]any{"agent_id": "rae", "cards": []any{refund}}); rr.Code != http.StatusBadRequest ||
		!strings.Contains(rr.Body.String(), "refund names no single item to count per, so a limit for each item goes in the taf's instructions") {
		t.Fatalf("a write with no item: %d %s", rr.Code, rr.Body.String())
	}
	// Two string arguments: nothing says which is the item, so none is named.
	two := newDraftRigOn(t, stubDomainSource{yaml: strings.Replace(replyYAML, "      - name: ticket_id\n", "      - name: status\n        type: string\n      - name: ticket_id\n", 1)})
	if rr := do(t, two.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/preview", map[string]any{"agent_id": "rae", "cards": []any{reply(daily, once)}}); rr.Code != http.StatusBadRequest ||
		!strings.Contains(rr.Body.String(), "post_reply names no single item to count per") || strings.Contains(rr.Body.String(), `per\": \"status`) {
		t.Fatalf("two string arguments: %d %s", rr.Code, rr.Body.String())
	}
	// Drafted as asked: a count per item, a lifetime budget, a day's count.
	perTicket := map[string]any{"quantity": "count", "limit": 1, "window": "lifetime", "per": "ticket_id"}
	budget := card("rae-refund", "rae")
	budget["aggregates"] = []map[string]any{{"quantity": "sum(amount_cents)", "limit": 200000, "window": "lifetime"}}
	for name, c := range map[string]map[string]any{"per ticket": reply(daily, perTicket), "a lifetime budget": budget, "a day's count": reply(daily)} {
		if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/preview", map[string]any{"agent_id": "rae", "cards": []any{c}}); rr.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
	}
}
