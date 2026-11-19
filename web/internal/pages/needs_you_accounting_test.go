package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestNeedsYouAccountingStatesPreserveActionAndApproval(t *testing.T) {
	t.Parallel()
	approved := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, status := range []string{"held", "approved_no_room", "unavailable"} {
		t.Run(status, func(t *testing.T) {
			no := false
			e := apiclient.CardException{ID: "exception-room", Subject: "refund-agent", Action: "issue_refund", Status: status, CanApproveOnce: &no, ResolvedBy: "owner@example.com", ResolvedAt: &approved,
				Arguments:      map[string]json.RawMessage{"customer_id": json.RawMessage(`"customer-42"`), "amount_cents": json.RawMessage(`480000`)},
				Breaches:       []apiclient.CardBreach{{MandateID: "current-budget", Outcome: "limit_reached", Quantity: "sum(amount_cents)", Limit: 50000, Used: 50000, Draw: 480000}},
				PriorApprovals: []apiclient.CardApprovalRecord{{By: "earlier-owner@example.com", At: &approved}},
			}
			var b bytes.Buffer
			if err := NeedsYou(NeedsYouView{Detail: &e}).Render(context.Background(), &b); err != nil {
				t.Fatal(err)
			}
			html := b.String()
			for _, want := range []string{"issue_refund", "customer-42", "480000", "earlier-owner@example.com"} {
				if !strings.Contains(html, want) {
					t.Errorf("missing action/provenance %q", want)
				}
			}
			if strings.Contains(html, "/approve-once") || strings.Contains(html, "/change-card") {
				t.Fatal("cumulative/unreadable state offered approval or implicit budget change")
			}
			if status == "unavailable" {
				if strings.Contains(html, "/extra-room") || strings.Contains(html, "/reject") {
					t.Fatal("unreadable state offered write controls")
				}
			} else if !strings.Contains(html, "/cards/current-budget/extra-room") {
				t.Fatal("current budget recovery link missing")
			}
			if status == "approved_no_room" && !strings.Contains(html, "owner@example.com") {
				t.Fatal("original approver lost")
			}
		})
	}
}
