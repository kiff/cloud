package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
)

// kiff/tafs#75 and the eight languages: the phone sends its app's
// languages, and KIFF writes a person's lines in the best one it writes.
func TestReplyLanguage(t *testing.T) {
	for header, want := range map[string]string{
		"":                              langEN,
		"es-CO, fr-FR;q=0.9, en;q=0.8":  langES,
		"fr-FR, en;q=0.9, es;q=0.8":     "fr",
		"de":                            "de",
		"ja, zh":                        langEN,
		"en-US, es;q=0.5":               langEN,
		"es;q=0, en;q=0.4":              langEN,
		"ES-mx":                         langES,
		"es;q=oops, en":                 langEN,
		"en;q=0.7, es;q=0.7, fr;q=0.9":  "fr",
		"pt-BR, es-419;q=0.6, en;q=0.5": "pt",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Accept-Language", header)
		}
		if got := replyLanguage(r); got != want {
			t.Errorf("%q: %s, want %s", header, got, want)
		}
	}
}

// Every reason code the Record names has its words in every language, so
// a refused call never reads blank in one of them.
func TestEveryCallReasonHasWordsInEveryLanguage(t *testing.T) {
	for _, l := range words.All {
		for _, code := range callReasons {
			if strings.TrimSpace(words.Of(l).Reasons[code]) == "" {
				t.Errorf("%s: no words for %s", l, code)
			}
		}
	}
	if callReasonIn("refused", []string{"unknown", reasonHalted}, "de") != "Du hattest es angehalten." {
		t.Error("the first reason with words, in German")
	}
	// K4's closed category reads as closed until the owner opens it, never as a Card that doesn't cover it.
	if got := callReasonIn("blocked", []string{"mandate_category_closed"}, "en"); got != "It's closed on your Card until you open it." {
		t.Errorf("a closed category: %q", got)
	}
}

func TestAHoldsReasonInSpanish(t *testing.T) {
	per := map[string]any{"outcome": string(mandate.OutcomePerActionExceeded), "draw": int64(15000), "limit": int64(10000)}
	if got := reasonInUnit(per, "EUR", 2, langES); got != "150,00 EUR supera el límite de 100,00 EUR por llamada" {
		t.Errorf("per call: %q", got)
	}
	total := map[string]any{"outcome": string(mandate.OutcomeLimitReached), "draw": int64(15000), "limit": int64(100000), "used": int64(90000)}
	if got := reasonInUnit(total, "EUR", 2, langES); got != "150,00 EUR superaría el límite de 1000,00 EUR: ya se usaron 900,00 EUR" {
		t.Errorf("total: %q", got)
	}
	named := map[string]any{"outcome": string(mandate.OutcomeResourceOutside), "resource": "charge", "value": "ch_9"}
	if got := resourceReasonIn(named, langES); got != `charge "ch_9" no es uno de los que cubre esta Card` {
		t.Errorf("resource: %q", got)
	}
	none := map[string]any{"outcome": string(mandate.OutcomeResourceOutside), "resource": "charge", "value": ""}
	if got := resourceReasonIn(none, langES); got != "la llamada no indica ningún charge, y esta Card solo se aplica a los charge que lista" {
		t.Errorf("no resource: %q", got)
	}
	// English keeps KIFF's own resource reason; other breaches keep theirs in any language.
	if resourceReasonIn(named, langEN) != "" || resourceReasonIn(per, langES) != "" {
		t.Error("a reason KIFF doesn't compose was rewritten")
	}
}

// Through the handler: the list and the detail say the reason in the
// request's language, and say which in Content-Language.
func TestHeldReasonsFollowAcceptLanguage(t *testing.T) {
	at := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	sum := mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return at })
	if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-eu", TenantID: "t1", Subject: "claude",
		Grants: []mandate.Grant{{Action: "refund"}}}, "tester"); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	over := mandate.Breach{MandateID: "mdt-eu", Outcome: mandate.OutcomePerActionExceeded, Quantity: sum, Limit: 10000, Draw: 15000,
		Reason: "amount_cents of 15000 is above the per-action limit of 10000"}
	if err := store.SaveException(context.Background(), mandate.Exception{
		ID: "exc-1", TenantID: "t1", Subject: "claude", ProposalID: "tc-1", Action: "refund", CreatedAt: at,
		Request: mandate.Request{Domain: "shop-eu", Parameters: map[string]int64{"amount_cents": 15000}}, Status: mandate.ExceptionHeld,
		Breaches: []mandate.Breach{over},
	}); err != nil {
		t.Fatalf("seed hold: %v", err)
	}
	h := NewMandatesHandler(store, unitDomains{yaml: map[string]string{"shop-eu": unitDomainYAML("shop-eu", "EUR", 2)}},
		WithMandatesClock(func() time.Time { return at }))
	mux := http.NewServeMux()
	h.Register(mux)

	get := func(path, lang string) (string, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(auth.WithIdentity(req.Context(), owner()))
		if lang != "" {
			req.Header.Set("Accept-Language", lang)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		var one struct {
			Breaches []struct {
				Reason string `json:"reason"`
			} `json:"breaches"`
		}
		var list struct {
			Exceptions []json.RawMessage `json:"exceptions"`
		}
		body := rr.Body.Bytes()
		if json.Unmarshal(body, &list) == nil && len(list.Exceptions) == 1 {
			body = list.Exceptions[0]
		}
		if err := json.Unmarshal(body, &one); err != nil || len(one.Breaches) != 1 {
			t.Fatalf("%s: %v %s", path, err, rr.Body.String())
		}
		return one.Breaches[0].Reason, rr.Header().Get("Content-Language")
	}
	for _, path := range []string{"/v1/me/card-exceptions", "/v1/me/card-exceptions/exc-1"} {
		if reason, lang := get(path, "es-CO, en;q=0.8"); reason != "150,00 EUR supera el límite de 100,00 EUR por llamada" || lang != "es" {
			t.Errorf("%s in Spanish: %q (%s)", path, reason, lang)
		}
		if reason, lang := get(path, ""); reason != "150.00 EUR is above the 100.00 EUR limit for one call" || lang != "en" {
			t.Errorf("%s with no header: %q (%s)", path, reason, lang)
		}
	}
}
