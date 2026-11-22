package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/words"
	"time"
)

func heldForTest() HeldAction {
	return HeldAction{
		Agent: "refund-agent", Action: "issue_refund", Amount: "4700 amount_eur",
		Outside:   []string{"card-refunds allows up to 500 amount_eur per action"},
		ExpiresAt: time.Date(2026, 10, 1, 12, 10, 0, 0, time.UTC),
		ReviewURL: "https://app.kiff.dev/needs-you/exc-1",
	}
}

// The owner is told what the agent wanted, which Card it is
// outside of, when the hold expires, and where to answer; every button
// opens Needs you, and the email itself cannot answer.
func TestResend_ActionHeld(t *testing.T) {
	t.Parallel()
	var got resendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"id":"email_1"}`))
	}))
	defer srv.Close()
	n, err := NewResendNotifier(ResendConfig{APIKey: testKey, From: "KIFF <ops@kiff.dev>", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.ActionHeld(context.Background(), "owner@example.com", heldForTest()); err != nil {
		t.Fatal(err)
	}
	if len(got.To) != 1 || got.To[0] != "owner@example.com" || got.Subject != "Your agent is waiting for your answer" {
		t.Fatalf("to %v, subject %q", got.To, got.Subject)
	}
	for _, s := range []string{"refund-agent wants to issue_refund 4700 amount_eur", "card-refunds allows up to 500 amount_eur per action",
		"https://app.kiff.dev/needs-you/exc-1", "1 Oct 2026, 12:10 (UTC)", "Nothing can be approved from this email."} {
		if !strings.Contains(got.Text, s) {
			t.Errorf("text lacks %q:\n%s", s, got.Text)
		}
	}
	if strings.Count(got.HTML, `href="https://app.kiff.dev/needs-you/exc-1"`) != 3 {
		t.Errorf("html should link each of the three answers to Needs you:\n%s", got.HTML)
	}
	// Nothing in the email answers: no answer endpoints, no tokens.
	for _, s := range []string{"approve-once", "change-card", "/reject", "token"} {
		if strings.Contains(got.HTML, s) || strings.Contains(got.Text, s) {
			t.Errorf("email contains %q", s)
		}
	}
}

// The agent picks the action name and arguments, so they are escaped.
func TestHeldEmailEscapesWhatTheAgentChose(t *testing.T) {
	h := heldForTest()
	h.Action = `<script>alert(1)</script>`
	h.Agent = `"><img src=x>`
	_, _, html, err := renderHeld(h)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>") || strings.Contains(html, "<img src=x") {
		t.Fatalf("unescaped agent input:\n%s", html)
	}
	h.ReviewURL = ""
	if _, _, _, err := renderHeld(h); err == nil {
		t.Fatal("an email without its review link was rendered")
	}
}

func TestNoopActionHeld(t *testing.T) {
	if err := NewNoopNotifier().ActionHeld(context.Background(), "x@example.com", heldForTest()); err != nil {
		t.Fatal(err)
	}
}

// The Card's next steps are offered too, between "Change the Card"
// and "Reject", each opening Needs you like every other answer.
func TestRenderHeld_NextSteps(t *testing.T) {
	t.Parallel()
	h := heldForTest()
	h.NextSteps = []string{"return_first", "proof_first"}
	_, text, html, err := renderHeld(h)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "reject it with a next step for the agent: Return first, Proof first.") {
		t.Errorf("text lacks the next steps:\n%s", text)
	}
	if strings.Count(html, `href="https://app.kiff.dev/needs-you/exc-1"`) != 5 {
		t.Errorf("html should link all five answers to Needs you:\n%s", html)
	}
	order := []string{"Approve once", "Change the Card to allow it", "Return first", "Proof first", "Reject"}
	at := 0
	for _, s := range order {
		i := strings.Index(html[at:], ">"+s+"<")
		if i < 0 {
			t.Fatalf("html lacks %q after the previous answer:\n%s", s, html)
		}
		at += i
	}
	_, text, _, _ = renderHeld(heldForTest())
	if strings.Contains(text, "next step") {
		t.Errorf("a Card without next steps mentions them:\n%s", text)
	}
}

// The email is in the recipient's language: its subject, sentences and
// buttons, with the agent's own names escaped in the HTML.
func TestHeldEmailInTheRecipientsLanguage(t *testing.T) {
	h := heldForTest()
	h.Lang = words.DE
	h.Agent = "<b>x</b>"
	subject, text, html, err := renderHeld(h)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Dein Agent wartet auf deine Antwort" {
		t.Errorf("subject %q", subject)
	}
	for _, s := range []string{"möchte issue_refund 4700 amount_eur ausführen", "Es wurde nichts ausgeführt.", "1. Okt. 2026, 12:10 (UTC)",
		"Über diese E-Mail kann nichts genehmigt werden."} {
		if !strings.Contains(text, s) {
			t.Errorf("text lacks %q:\n%s", s, text)
		}
	}
	for _, s := range []string{"Einmal genehmigen", "Ablehnen", "&lt;b&gt;x&lt;/b&gt;"} {
		if !strings.Contains(html, s) {
			t.Errorf("html lacks %q", s)
		}
	}
	if strings.Contains(html, "<b>x</b>") {
		t.Error("the agent's name wasn't escaped")
	}
}

// With the call in people's words the email says who wants to do something outside its
// Card, then a table: the action in words, the amount as the person reads money, why it is
// held. No identifier, no unit name, and the hold's end in the
// account's zone, named.
func TestHeldEmailInPeoplesWords(t *testing.T) {
	h := heldForTest()
	h.Agent = "Refund bot"
	h.ActionWords, h.AmountText = "Refund a payment", "€47.00"
	h.Reason = "€47.00 is over the €40.00 limit for one call"
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	h.Zone = paris
	_, text, html, err := renderHeld(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Refund bot wants to do something outside its Card:",
		"Action: Refund a payment", "Amount: €47.00", "Why it's held: €47.00 is over the €40.00 limit for one call",
		"1 Oct 2026, 14:10 (Paris time)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	for _, raw := range []string{"issue_refund", "amount_eur", "card-refunds allows"} {
		if strings.Contains(text, raw) || strings.Contains(html, raw) {
			t.Errorf("the email says %q, an identifier or a unit:\n%s", raw, text)
		}
	}
	for _, want := range []string{"<strong>Refund bot</strong>", "<td width=", "€47.00", "1 Oct 2026, 14:10 (Paris time)"} {
		if !strings.Contains(html, want) {
			t.Errorf("html lacks %q:\n%s", want, html)
		}
	}
	// The words KIFF built from the agent's arguments are escaped.
	h.Reason = `<img src=x onerror=alert(1)>`
	h.ActionWords = `<script>x</script>`
	_, _, html, _ = renderHeld(h)
	if strings.Contains(html, "<img src=x") || strings.Contains(html, "<script>x") {
		t.Errorf("unescaped: %s", html)
	}
	// In French, with its own labels and the French amount.
	h = heldForTest()
	h.Lang, h.ActionWords, h.AmountText = words.FR, "Rembourser un paiement", "47,00 €"
	_, text, _, _ = renderHeld(h)
	for _, want := range []string{"veut faire quelque chose qui sort de sa Card :", "Action demandée : Rembourser un paiement", "Montant : 47,00 €"} {
		if !strings.Contains(text, want) {
			t.Errorf("french text lacks %q:\n%s", want, text)
		}
	}
}
