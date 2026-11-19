package handlers_test

import (
	"html"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/web/internal/words"
)

// heldAPI is the held call as KIFF answers it, in the language asked
// for: its own lines in German when asked in German.
func heldAPI(t *testing.T, langs *[]string, mu *sync.Mutex) *http.ServeMux {
	t.Helper()
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/me/card-exceptions/exc-1", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*langs = append(*langs, r.Header.Get("Accept-Language"))
		mu.Unlock()
		summary, reason := "Refund a payment", "€1,500 is over the €500 limit for one call"
		if r.Header.Get("Accept-Language") == "de" {
			summary, reason = "Eine Zahlung erstatten", "1.500 € liegt über dem Limit von 500 € pro Aufruf"
		}
		_, _ = w.Write([]byte(`{"id":"exc-1","proposal_id":"p-1","subject":"refund-agent","action":"REFUND_ORDER","status":"held","can_approve_once":true,
"breaches":[{"mandate_id":"card-refunds","outcome":"per_action_exceeded","quantity":"sum(amount_cents)","limit":50000,"used":0,"draw":150000}],
"arguments":{"order":"\"ord-9\""},"summary":"` + summary + `","amount_text":"1.500 €","reason_text":"` + reason + `","details":["order: ord-9"],
"created_at":"2026-09-30T10:00:00Z"}`))
	})
	api.HandleFunc("POST /v1/me/card-exceptions/exc-1/approve-once", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"exc-1","status":"approved"}`))
	})
	return api
}

// Opened by the tafs app (?lang=de), a held call's page is its screen in
// German: KIFF's own lines asked in German, the screen's words around them,
// "tafs by KIFF", and an answer that comes back to the screen in German.
func TestTheHeldCallsScreenSpeaksTheAppsLanguage(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var langs []string
	do := needsYouServer(t, heldAPI(t, &langs, &mu))
	de := words.HeldIn("de")

	res, body := do(http.MethodGet, "/needs-you/exc-1?lang=de")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	for _, want := range []string{`<html lang="de"`, "Eine Zahlung erstatten", "1.500 €", "liegt über dem Limit", "order: ord-9", words.TafsByKIFF,
		">" + de.Heading + "<", ">" + de.ApproveOnce + "<", ">" + de.Refuse + "<", ">" + de.ReviseCard + "<", html.EscapeString(de.HowURL),
		`action="/needs-you/exc-1/approve-once?lang=de"`, `action="/needs-you/exc-1/reject?lang=de"`, `href="/needs-you/exc-1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the German screen has no %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Approve once") || strings.Contains(body, "Refund a payment") {
		t.Errorf("English is left on the German screen:\n%s", body)
	}
	mu.Lock()
	if len(langs) == 0 || langs[len(langs)-1] != "de" {
		t.Errorf("KIFF wasn't asked in German: %v", langs)
	}
	mu.Unlock()

	// Approve once: back on the screen, in German, saying what it did.
	_, after := do(http.MethodPost, "/needs-you/exc-1/approve-once?lang=de")
	if !strings.Contains(after, html.EscapeString(de.DoneApproveOnce)) || !strings.Contains(after, `<html lang="de"`) {
		t.Errorf("after approving, in German: %s", after)
	}
}

// Without ?lang=, the page is the dashboard's, in English, as before.
func TestTheDashboardsHeldCallStaysAsItWas(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var langs []string
	do := needsYouServer(t, heldAPI(t, &langs, &mu))
	_, body := do(http.MethodGet, "/needs-you/exc-1")
	if strings.Contains(body, words.TafsByKIFF) || !strings.Contains(body, "Approve once") || !strings.Contains(body, "Needs you") || !strings.Contains(body, `<html lang="en"`) {
		t.Errorf("the dashboard's view: %s", body)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range langs {
		if l != "" {
			t.Errorf("the dashboard asked KIFF in %q", l)
		}
	}
}
