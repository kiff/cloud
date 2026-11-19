package handlers

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A draft says each Card's lines in the owner's words, KIFF's own, so no app
// builds them: the tool's title, the cap in its unit, the
// totals; and its title by the name it was submitted with.
func TestAgentDraftSaysItsCardsLines(t *testing.T) {
	r := newDraftRig(t, WithMandatesToolTitles(fakeTitles{"refund": "Refund a payment"}))
	c := card("rae-refund", "rae")
	c["aggregates"] = append(c["aggregates"].([]map[string]any), map[string]any{"quantity": "count", "limit": 20, "window": "calendar_day"})
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Stripe refunds", "cards": []any{c}})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %v", code, d)
	}
	// A new Card holds for the owner past its limits: each line says it asks.
	want := []string{"Refund a payment: up to €150 each; more asks you first", "€2,000 in total a day, then it asks you", "20 calls a day, then it asks you"}
	check := func(where string, v map[string]any) {
		t.Helper()
		if v["card_title"] != "Stripe refunds' Card" {
			t.Errorf("%s: card_title = %v", where, v["card_title"])
		}
		shown := v["cards"].([]any)[0].(map[string]any)
		if shown["on_exceed"] != "owner" {
			t.Errorf("%s: on_exceed = %v", where, shown["on_exceed"])
		}
		var got []string
		for _, l := range shown["lines"].([]any) {
			got = append(got, l.(string))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: lines = %q, want %q", where, got, want)
		}
	}
	check("submit", d)
	rr := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts/"+d["id"].(string), nil)
	var got map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	check("read", got)
}

// Two grants that read alike, on two Cards the owner confirms together, each
// add their action in words; the terms a Card has now read on their own.
func TestCardLinesTellAlikeToolsApart(t *testing.T) {
	a := map[string]any{"grants": []any{map[string]any{"action": "refund", "title": "Write Stripe API data",
		"per_action_max": []any{map[string]any{"quantity": "sum(amount)", "max": float64(4000), "unit": "USD", "scale": 2}}}},
		"current": map[string]any{"grants": []any{map[string]any{"action": "refund", "title": "Write Stripe API data"}}}}
	b := map[string]any{"grants": []any{map[string]any{"action": "x9k2ab_capture", "title": "Write Stripe API data"}},
		"aggregates": []any{map[string]any{"quantity": "count", "limit": float64(1), "window": "calendar_day"}}}
	withLines([]map[string]any{a, b}, wordsIn(langEN))
	if got := a["lines"].([]string); !slices.Equal(got, []string{"Write Stripe API data · refund: up to $40 each"}) {
		t.Errorf("first: %q", got)
	}
	if got := b["lines"].([]string); !slices.Equal(got, []string{"Write Stripe API data · capture", "1 call a day"}) {
		t.Errorf("second: %q", got)
	}
	if got := a["current"].(map[string]any)["lines"].([]string); !slices.Equal(got, []string{"Write Stripe API data"}) {
		t.Errorf("now: %q", got)
	}
	if cardTitle("", wordsIn(langEN)) != "The taf's Card" || cardTitle("Rae", wordsIn(langEN)) != "Rae's Card" {
		t.Errorf("titles: %q, %q", cardTitle("", wordsIn(langEN)), cardTitle("Rae", wordsIn(langEN)))
	}
}

// A total in a window KIFF has no words for is refused at submit: the owner
// never signs a guessed line.
func TestAgentDraftRefusesAWindowWithoutWords(t *testing.T) {
	r := newDraftRig(t)
	c := card("rae-refund", "rae")
	c["aggregates"] = []map[string]any{{"quantity": "count", "limit": 20, "window": "calendar_week"}}
	if code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{c}}); code != http.StatusBadRequest {
		t.Fatalf("submit: %d %v, want 400", code, d)
	}
}

// A preview reads as the submitted draft would: the same
// card_title and lines, from the person's session, and nothing is stored,
// confirmed or given a claim secret.
func TestAgentDraftPreviewReadsAsTheDraftWould(t *testing.T) {
	r := newDraftRig(t, WithMandatesToolTitles(fakeTitles{"refund": "Refund a payment"}))
	body := map[string]any{"agent_id": "rae", "label": "Stripe refunds", "cards": []any{card("rae-refund", "rae")}}
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/preview", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got["card_title"] != "Stripe refunds' Card" {
		t.Errorf("card_title = %v", got["card_title"])
	}
	for _, k := range []string{"id", "claim_secret", "status", "version"} {
		if _, ok := got[k]; ok {
			t.Errorf("a preview has no %s: %v", k, got[k])
		}
	}
	lines := got["cards"].([]any)[0].(map[string]any)["lines"].([]any)
	if len(lines) == 0 || lines[0] != "Refund a payment: up to €150 each; more asks you first" {
		t.Errorf("lines = %v", lines)
	}
	list := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts", nil)
	if strings.Contains(list.Body.String(), "drf_") {
		t.Errorf("a preview stored a draft: %s", list.Body.String())
	}
	// Checked as a submit is: a window without words is refused.
	bad := card("rae-refund", "rae")
	bad["aggregates"] = []map[string]any{{"quantity": "count", "limit": 20, "window": "calendar_week"}}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/preview", map[string]any{"agent_id": "rae", "cards": []any{bad}}); rr.Code != http.StatusBadRequest {
		t.Errorf("bad window: %d, want 400", rr.Code)
	}
}

// Each draft Card says what happens past its limits, as it would be issued,
// and its lines say it: the draft's own policy; else, on a revision, the
// Card in force's, since leaving it out keeps it; else the owner's.
func TestAgentDraftSaysWhatHappensPastTheLimits(t *testing.T) {
	r := newDraftRig(t)
	c := card("rae-refund", "rae")
	c["on_exceed"] = "refuse"
	c["aggregates"] = []map[string]any{{"quantity": "count", "limit": 0, "window": "calendar_day"}}
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{c}})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %v", code, d)
	}
	shown := d["cards"].([]any)[0].(map[string]any)
	if shown["on_exceed"] != "refuse" || !slices.Contains(shown["lines"].([]any), any("0 calls a day")) {
		t.Errorf("refuse: %v %v", shown["on_exceed"], shown["lines"])
	}

	c["on_exceed"] = "owner"
	_, d = r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{c}})
	shown = d["cards"].([]any)[0].(map[string]any)
	if shown["on_exceed"] != "owner" || !slices.Contains(shown["lines"].([]any), any("Each call asks you first")) {
		t.Errorf("owner: %v %v", shown["on_exceed"], shown["lines"])
	}
}

// A revision that leaves on_exceed out keeps the Card in force's, and reads so.
func TestAgentDraftRevisionKeepsTheCardsPolicy(t *testing.T) {
	r := newDraftRig(t)
	c := card("rae-refund", "rae")
	c["on_exceed"] = "refuse"
	if code, d := r.submit(admin(), map[string]any{"agent_id": "rae", "cards": []any{c}}); code != http.StatusCreated || d["status"] != "confirmed" {
		t.Fatalf("start rae: %d %v", code, d)
	}
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "cards": []any{cardMax("rae-refund", "rae", 30000)}})
	if code != http.StatusCreated || d["revision"] != true {
		t.Fatalf("revision: %d %v", code, d)
	}
	shown := d["cards"].([]any)[0].(map[string]any)
	if shown["on_exceed"] != "refuse" {
		t.Errorf("revision: on_exceed = %v, want the Card in force's", shown["on_exceed"])
	}
	for _, l := range shown["lines"].([]any) {
		if strings.Contains(l.(string), "asks you") {
			t.Errorf("a refusing Card says it asks: %q", l)
		}
	}
	if cur := shown["current"].(map[string]any); cur["on_exceed"] != "refuse" {
		t.Errorf("current: on_exceed = %v", cur["on_exceed"])
	}
}
