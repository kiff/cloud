package handlers_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A tool Card's "Only for" row opens a sheet that limits it to named values
// of its tool's resource argument, or lifts the limit. Saving reissues the
// Card with everything else as it was: the expiry, what happens past it and
// its hold time.
func TestCardOnlyFor(t *testing.T) {
	t.Parallel()
	until := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	from := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	api := &cardAPI{cards: []map[string]any{{
		"id": "refund-agent-issue-refund", "subject": "refund-agent", "domain": "gateway", "active": true,
		"grants": []any{map[string]any{"action": "issue_refund", "next_steps": []string{"return_first"},
			"resources": map[string]any{"parameter": "order", "values": []string{"1043", "1044"}}}},
		"aggregates": []any{map[string]any{"quantity": "sum(amount_eur)", "limit": 30000, "window": "calendar_day", "measure": "effects"}},
		"valid_from": from, "valid_until": until, "on_exceed": "owner", "hold_expiry_seconds": 86400,
	}, {
		// Scoped on an argument the tool no longer names (it names order).
		"id": "refund-agent-issue-refund-old", "subject": "refund-agent", "domain": "gateway", "active": true,
		"grants": []any{map[string]any{"action": "issue_refund",
			"resources": map[string]any{"parameter": "order_id", "values": []string{"ord_1"}}}},
		"aggregates": []any{map[string]any{"quantity": "count", "limit": 5, "window": "calendar_day"}},
	}}}
	h, c := toolCardRig(t, api)

	settings := send(h, c, http.MethodGet, "/cards/refund-agent-issue-refund/settings").Body.String()
	if !strings.Contains(settings, `href="/cards/refund-agent-issue-refund/only-for"`) || !strings.Contains(settings, `href="/tools/issue_refund"`) {
		t.Fatalf("settings rows: %s", settings)
	}
	sheet := send(h, c, http.MethodGet, "/cards/refund-agent-issue-refund/only-for").Body.String()
	for _, want := range []string{"Any order", `value="only" checked`, `name="keep" value="1043" checked`, `name="keep" value="1044" checked`,
		"comes to you, like one over the limit"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("sheet missing %q", want)
		}
	}

	back := "/cards/refund-agent-issue-refund/settings"
	followFlashTo(t, h, c, postForm(h, c, "/cards/refund-agent-issue-refund/only-for",
		url.Values{"scope": {"only"}, "keep": {"1043", "9999"}, "add": {"1051, 1043 ,\n1060"}}), back)
	api.mu.Lock()
	got := api.issued[len(api.issued)-1]
	api.mu.Unlock()
	grant := got["grants"].([]any)[0].(map[string]any)
	res := grant["resources"].(map[string]any)
	if res["parameter"] != "order" || strings.Join(toStrings(res["values"]), ",") != "1043,1051,1060" {
		t.Fatalf("scope: %v", res)
	}
	if got["id"] != "refund-agent-issue-refund" || got["on_exceed"] != "owner" || got["hold_expiry"] != "86400s" ||
		got["valid_from"] != from.Format(time.RFC3339) ||
		got["valid_until"] != until.Format(time.RFC3339) || strings.Join(toStrings(grant["next_steps"]), ",") != "return_first" {
		t.Fatalf("other terms not kept: %v", got)
	}
	if aggs := got["aggregates"].([]any); len(aggs) != 1 || aggs[0].(map[string]any)["limit"] != float64(30000) {
		t.Fatalf("aggregates: %v", got["aggregates"])
	}

	followFlashTo(t, h, c, postForm(h, c, "/cards/refund-agent-issue-refund/only-for", url.Values{"scope": {"any"}}), back)
	api.mu.Lock()
	got = api.issued[len(api.issued)-1]
	api.mu.Unlock()
	if _, ok := got["grants"].([]any)[0].(map[string]any)["resources"]; ok {
		t.Fatalf("any order kept a scope: %v", got)
	}

	sheetURL := "/cards/refund-agent-issue-refund/only-for"
	for name, form := range map[string]url.Values{
		"nothing listed": {"scope": {"only"}},
		"too long":       {"scope": {"only"}, "add": {strings.Repeat("x", 257)}},
		"no choice":      {},
	} {
		if rec := postForm(h, c, sheetURL, form); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != sheetURL {
			t.Errorf("%s: %d → %q", name, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// A scope on an argument the tool no longer names is shown as it is, with
// neither choice made, so Save cannot quietly lift it.
func TestCardOnlyForKeepsAMismatchedScopeVisible(t *testing.T) {
	t.Parallel()
	api := &cardAPI{cards: []map[string]any{{
		"id": "refund-agent-issue-refund", "subject": "refund-agent", "domain": "gateway", "active": true,
		"grants": []any{map[string]any{"action": "issue_refund",
			"resources": map[string]any{"parameter": "order_id", "values": []string{"ord_1"}}}},
		"aggregates": []any{map[string]any{"quantity": "count", "limit": 5, "window": "calendar_day"}},
	}}}
	h, c := toolCardRig(t, api)
	sheetURL := "/cards/refund-agent-issue-refund/only-for"
	sheet := send(h, c, http.MethodGet, sheetURL).Body.String()
	if !strings.Contains(sheet, "limited to order_id ord_1, which calls to issue_refund no longer carry") {
		t.Fatalf("old scope not shown: %s", sheet)
	}
	if strings.Contains(sheet, `value="any" checked`) || strings.Contains(sheet, `value="only" checked`) {
		t.Fatal("a choice is preselected for a mismatched scope")
	}
	if rec := postForm(h, c, sheetURL, url.Values{}); rec.Header().Get("Location") != sheetURL {
		t.Fatalf("saved without a choice: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	api.mu.Lock()
	n := len(api.issued)
	api.mu.Unlock()
	if n != 0 {
		t.Fatal("the Card was reissued without a choice")
	}
	followFlashTo(t, h, c, postForm(h, c, sheetURL, url.Values{"scope": {"only"}, "keep": {"ord_1"}, "add": {"1043"}}),
		"/cards/refund-agent-issue-refund/settings")
	api.mu.Lock()
	res := api.issued[0]["grants"].([]any)[0].(map[string]any)["resources"].(map[string]any)
	api.mu.Unlock()
	if res["parameter"] != "order" || strings.Join(toStrings(res["values"]), ",") != "1043" {
		t.Fatalf("an old value was carried to the new argument: %v", res)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
