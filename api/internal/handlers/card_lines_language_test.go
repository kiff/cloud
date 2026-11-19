package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
)

// getIn reads path as id does from an app showing lang.
func getIn(t *testing.T, mux *http.ServeMux, id auth.Identity, path, lang string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept-Language", lang)
	req = req.WithContext(auth.WithIdentity(req.Context(), id))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var v map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("%s: %d %v %s", path, rr.Code, err, rr.Body.String())
	}
	return v
}

// A draft's Card lines and title come in the language of the app that reads
// them, the amounts written its way; a tool's title stays as it is.
func TestADraftsCardLinesInTheReadersLanguage(t *testing.T) {
	r := newDraftRig(t, WithMandatesToolTitles(fakeTitles{"refund": "Refund a payment"}))
	c := card("rae-refund", "rae")
	c["aggregates"] = append(c["aggregates"].([]map[string]any), map[string]any{"quantity": "count", "limit": 20, "window": "calendar_day"})
	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Stripe refunds", "cards": []any{c}})
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %v", code, d)
	}
	got := getIn(t, r.mux, admin(), "/v1/me/agent-drafts/"+d["id"].(string), "de-DE, en;q=0.5")
	if got["card_title"] != "Card von Stripe refunds" {
		t.Errorf("card_title = %v", got["card_title"])
	}
	var lines []string
	for _, l := range got["cards"].([]any)[0].(map[string]any)["lines"].([]any) {
		lines = append(lines, l.(string))
	}
	want := []string{"Refund a payment: bis zu 150\u00a0€ pro Aufruf; bei mehr wirst du zuerst gefragt",
		"2.000\u00a0€ insgesamt pro Tag, danach wirst du gefragt", "20 Aufrufe pro Tag, danach wirst du gefragt"}
	if !slices.Equal(lines, want) {
		t.Errorf("lines = %q, want %q", lines, want)
	}
}

// A held call's reason comes in the reader's language: which call it would
// be, and an amount over a limit, written the language's way.
func TestAHeldCallsReasonInTheReadersLanguage(t *testing.T) {
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return at })
	if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-c", TenantID: "t1", Subject: "claude",
		Grants: []mandate.Grant{{Action: "refund"}}}, "tester"); err != nil {
		t.Fatal(err)
	}
	b := mandate.Breach{MandateID: "mdt-c", Outcome: mandate.OutcomeLimitReached, Quantity: mandate.Quantity{Kind: mandate.KindCount},
		Limit: 20, Used: 20, Draw: 1, Window: mandate.WindowCalendarDay}
	if err := store.SaveException(context.Background(), mandate.Exception{ID: "exc-c", TenantID: "t1", Subject: "claude", ProposalID: "tc-c",
		Action: "refund", CreatedAt: at, Request: mandate.Request{Domain: "shop"}, Status: mandate.ExceptionHeld, Breaches: []mandate.Breach{b}}); err != nil {
		t.Fatal(err)
	}
	h := NewMandatesHandler(store, unitDomains{yaml: map[string]string{}}, WithMandatesClock(func() time.Time { return at }))
	mux := http.NewServeMux()
	h.Register(mux)
	if got := getIn(t, mux, owner(), "/v1/me/card-exceptions/exc-c", "es")["reason_text"]; got != "20 llamadas al día: esta sería la 21.ª" {
		t.Errorf("es reason_text = %q", got)
	}
	list := getIn(t, mux, owner(), "/v1/me/card-exceptions", "pl")["exceptions"].([]any)
	if got := list[0].(map[string]any)["reason_text"]; got != "20 wywołań dziennie: to byłoby 21. wywołanie" {
		t.Errorf("pl list reason_text = %q", got)
	}

	per := map[string]any{"outcome": string(mandate.OutcomePerActionExceeded), "limit": int64(4000), "unit": "EUR", "scale": 2}
	if got := heldReason(mandate.Exception{}, []map[string]any{per}, heldMoney{4500, "EUR", 2}, true, wordsIn("de")); got != "45\u00a0€ liegt über dem Limit von 40\u00a0€ pro Aufruf" {
		t.Errorf("de amount reason = %q", got)
	}
}

// A limit's pending use, in French: its amount written the French way.
func TestPendingUseInALanguage(t *testing.T) {
	if got := pendingText(4900, 2, false, "EUR", 2, wordsIn("fr")); got != "49 € en viennent de 2 appels que l'outil a refusés. Ce montant compte jusqu'à ce que KIFF puisse confirmer que rien n'a bougé." {
		t.Errorf("fr = %q", got)
	}
	if got := pendingText(0, 1, true, "", 0, wordsIn("nl")); got != "1 daarvan is een aanroep die de tool weigerde. Die telt mee tot KIFF kan bevestigen dat er niets is gebeurd." {
		t.Errorf("nl = %q", got)
	}
}
