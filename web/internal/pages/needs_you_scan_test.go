package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// D: an answered call reads as one headline (action and amount in its
// unit), why it was held named as the Card's limit, and who decided
// kept apart from what the tool reported.
func TestNeedsYouAnsweredIsEasyToScan(t *testing.T) {
	at := time.Date(2026, 10, 10, 14, 5, 0, 0, time.UTC)
	e := apiclient.CardException{ID: "exc-1", Subject: "onboard-agent", Action: "refund_order", Status: "approved", ResolvedAt: &at, ResolvedBy: "approver@kiff",
		Parameters:     map[string]int64{"amount_eur": 7500},
		ParameterUnits: map[string]apiclient.AmountUnit{"amount_eur": {Unit: "EUR", Scale: 2}},
		Breaches:       []apiclient.CardBreach{{MandateID: "onboard-agent-refunds", Outcome: "per_action_exceeded", Quantity: "sum(amount_eur)", Limit: 5000, Draw: 7500, Unit: "EUR", Scale: 2}},
		ExternalAnswer: &apiclient.ExternalAnswer{Name: "Refund review service", Decision: "approve", Reason: "Within the damaged-item policy", At: at},
		ToolOutcome:    &apiclient.ToolOutcome{Status: "done", At: at}}
	var b bytes.Buffer
	if err := needsYouItem(e, true, nil).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{`<h3 class="ny-headline"><code>refund_order</code> <span class="ny-amount">75.00 EUR</span>`, "25.00 EUR over this Card’s 50.00 EUR per-call limit", "<h4>Who decided</h4>", "Refund review service</strong>, an external approver · approved once", "<h4>What the tool reported</h4>"} {
		if !strings.Contains(body, want) {
			t.Errorf("answered call lacks %q", want)
		}
	}
	who, tool := strings.Index(body, "Who decided"), strings.Index(body, "What the tool reported")
	if who < 0 || tool < who || strings.Contains(body[who:tool], "tool reported the call done") {
		t.Error("who decided and what the tool reported are not separate blocks")
	}
	if strings.Contains(body, "outside your delegation") {
		t.Error("the old wording is back")
	}
}

// A call whose current Card cannot be read keeps its recorded external
// answer on screen, in the list and on its page.
func TestNeedsYouUnavailableKeepsWhoDecided(t *testing.T) {
	at := time.Date(2026, 10, 10, 14, 5, 0, 0, time.UTC)
	e := apiclient.CardException{ID: "exc-2", Subject: "onboard-agent", Action: "refund_order", Status: "unavailable", ResolvedAt: &at, ResolvedBy: "apr_principal",
		ExternalAnswer: &apiclient.ExternalAnswer{Name: "Refund review service", Decision: "approve", Reason: "Within the damaged-item policy", At: at}}
	for _, detail := range []bool{false, true} {
		var b bytes.Buffer
		if err := needsYouItem(e, detail, nil).Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"The Card could not be read", "<h4>Who decided</h4>", "Refund review service</strong>, an external approver · approved once", "Within the damaged-item policy"} {
			if !strings.Contains(b.String(), want) {
				t.Errorf("detail=%v: unavailable call lacks %q", detail, want)
			}
		}
	}
}
