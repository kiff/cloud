package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// A call a Card held is joined to the owner's answer and opens its
// story on Needs you; any other call opens its own page.
func TestActivityRows(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 12, 0, 0, time.UTC)
	call := func(pid, outcome string, held bool) apiclient.ActivityCall {
		return apiclient.ActivityCall{ProposalDecision: apiclient.ProposalDecision{ProposalID: pid, ActorID: "luna-desk", ActionName: "shop_refund_order", Outcome: outcome, CreatedAt: at}, Held: held}
	}
	rejected := apiclient.CardException{ID: "exc-1", ProposalID: "p-rej", Status: "rejected", CreatedAt: at, Refusal: &apiclient.Refusal{NextStep: "return_first"},
		Parameters: map[string]int64{"amount": 24000}, ParameterUnits: map[string]apiclient.AmountUnit{"amount": {Unit: "EUR", Scale: 2}}}
	waiting := apiclient.CardException{ID: "exc-2", ProposalID: "p-wait", Status: "held", CreatedAt: at}
	rows := JoinActivity([]apiclient.ActivityCall{call("p-rej", "owner_required", true), call("p-wait", "owner_required", true), call("p-ok", "allowed", false), call("p-no", "blocked", false)},
		[]apiclient.CardException{rejected, waiting})

	for i, want := range []struct{ label, tone, answer, href, amount string }{
		{"Held", "warn", "Rejected: Return first", "/needs-you/exc-1", "240.00 EUR"},
		{"Waiting for you", "wait", "", "/needs-you/exc-2", ""},
		{"Allowed", "ok", "", "/activity/p-ok", ""},
		{"Refused", "off", "", "/activity/p-no", ""},
	} {
		r := rows[i]
		label, tone := r.decided()
		if label != want.label || tone != want.tone || r.answer() != want.answer || r.href() != want.href || r.amount() != want.amount {
			t.Errorf("row %d = %q %q %q %q %q, want %+v", i, label, tone, r.answer(), r.href(), r.amount(), want)
		}
	}
	// a review: a call still waiting with no held call to join (a domain
	// approval, or the held calls unreadable) is waiting, never refused.
	pending := JoinActivity([]apiclient.ActivityCall{call("p-appr", "approval_required", true), call("p-own", "owner_required", true), call("p-room", "approved_no_room", true)}, nil)
	for i, want := range []string{"Waiting for approval", "Waiting for you", "Approved, waiting for room"} {
		if label, tone := pending[i].decided(); label != want || tone != "wait" {
			t.Errorf("pending %d = %q %q, want %q wait", i, label, tone, want)
		}
	}
	if lead := activityCallLead(pending[0].Call); !strings.Contains(lead, "held it for an approver") || strings.Contains(lead, "refused") {
		t.Errorf("approval lead = %q", lead)
	}
	if lead := activityCallLead(pending[1].Call); !strings.Contains(lead, "A Card held it for you") || strings.Contains(lead, "refused") {
		t.Errorf("owner lead = %q", lead)
	}
	// a review: an approval kept for lack of room asks nobody to answer
	// again.
	if lead := activityCallLead(pending[2].Call); !strings.Contains(lead, "It was approved, but the Card has no room for it yet") || strings.Contains(lead, "until you answer") {
		t.Errorf("approved, no room lead = %q", lead)
	}
	if got := ActivityAgents([]string{"guard-bot", "luna-desk"}, nil, ""); strings.Join(got, ",") != "guard-bot,luna-desk" {
		t.Errorf("agents with no Cards = %v", got)
	}
	if f := NormalizeActivityFilter("", "", "201"); f.Page != 201 || f.Query().Offset != 10050 {
		t.Errorf("page 201 = %+v", f)
	}

	// A missing tool outcome may be a failed read: never "not sent".
	if rows[2].toolLine() != "—" {
		t.Errorf("allowed without an outcome = %q", rows[2].toolLine())
	}
	rows[2].Call.ToolOutcome = &apiclient.ToolOutcome{Status: "refused_by_tool", At: at}
	if rows[2].toolLine() != "Refused by the tool" || !rows[2].toolWarns() {
		t.Errorf("tool refused = %q", rows[2].toolLine())
	}
}

// A held call gets a receipt too; one rejected before a decision does not.
func TestActivityReceiptLabel(t *testing.T) {
	for _, tc := range []struct {
		d    apiclient.ProposalDecision
		want string
	}{
		{apiclient.ProposalDecision{Outcome: "allowed", ReceiptTraceID: "decision-p"}, "Signed"},
		{apiclient.ProposalDecision{Outcome: "owner_required"}, "Being signed"},
		{apiclient.ProposalDecision{Outcome: "approved_no_room"}, "Being signed"},
		{apiclient.ProposalDecision{Outcome: "blocked", Reasons: []string{"actor_not_key_holder"}}, "No receipt"},
		{apiclient.ProposalDecision{Outcome: "invalid"}, "No receipt"},
	} {
		if got := receiptLabel(tc.d); got != tc.want {
			t.Errorf("%+v = %q, want %q", tc.d, got, tc.want)
		}
	}
	if decisionReceiptLabel(apiclient.ProposalDecision{Outcome: "owner_required"}) != "Receipt being signed" {
		t.Error("a held call still reads as rejected before a decision on the Card")
	}
}

// Days are the account's: Today, Yesterday, then the date.
func TestActivityDays(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, loc)
	row := func(t time.Time) ActivityRow {
		return ActivityRow{Call: apiclient.ActivityCall{ProposalDecision: apiclient.ProposalDecision{CreatedAt: t}}}
	}
	days := activityDays([]ActivityRow{
		row(time.Date(2026, 10, 14, 7, 0, 0, 0, time.UTC)),
		row(time.Date(2026, 10, 13, 23, 30, 0, 0, time.UTC)), // 14 Oct, 01:30 local
		row(time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)),
		row(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)),
	}, loc, now)
	if len(days) != 3 || days[0].Label != "Today, 14 Oct" || len(days[0].Rows) != 2 || days[1].Label != "Yesterday, 13 Oct" || days[2].Label != "10 Oct 2026" {
		t.Errorf("days = %+v", days)
	}
}

func TestActivityFilter(t *testing.T) {
	f := NormalizeActivityFilter("held", " luna-desk ", "2")
	if f != (ActivityFilter{Show: "held", Agent: "luna-desk", Page: 2}) {
		t.Errorf("filter = %+v", f)
	}
	if q := f.Query(); q.Offset != 100 || q.Limit != ActivityPerPage || q.Actor != "luna-desk" || q.Show != "held" {
		t.Errorf("query = %+v", q)
	}
	if f := NormalizeActivityFilter("bogus", "", "-1"); f != (ActivityFilter{}) {
		t.Errorf("bad filter = %+v", f)
	}
	if got := activityHref("refused", "luna-desk", 1); got != "/activity?agent=luna-desk&page=1&show=refused" {
		t.Errorf("href = %q", got)
	}
	if ActivityAgentHref("") != "/activity" {
		t.Error("no agent is every call")
	}
}

// A gateway call's entity is its own call id, so it is not repeated; the
// tool's own error is quoted as the tool's; an anchored receipt says so.
func TestActivityCallPolish(t *testing.T) {
	c := apiclient.ActivityCall{ProposalDecision: apiclient.ProposalDecision{ProposalID: "tc-c0fc", EntityID: "tc-c0fc", ActorID: "luna-desk", ActionName: "shop_refund_order", Outcome: "allowed"}}
	if lead := activityCallLead(c); strings.Contains(lead, " on tc-c0fc") {
		t.Errorf("lead repeats the call id: %q", lead)
	}
	c.EntityID = "order-1043"
	if lead := activityCallLead(c); !strings.Contains(lead, "shop_refund_order on order-1043") {
		t.Errorf("lead drops a real entity: %q", lead)
	}
	refused := &apiclient.ToolOutcome{Status: "refused_by_tool", Message: "Not sent: the call could not be prepared"}
	if got := toolOutcomeLine(refused); got != "The tool refused the call. Its error: “Not sent: the call could not be prepared”" {
		t.Errorf("tool line = %q", got)
	}
	if got := toolOutcomeDetail(refused); got != "The tool said: “Not sent: the call could not be prepared”" {
		t.Errorf("tool detail = %q", got)
	}
	// The gateway's and the connection's own text is never quoted: the label
	// says what happened, in KIFF's words.
	for _, o := range []*apiclient.ToolOutcome{
		{Status: "unknown", Message: "the connection dropped"},
		{Status: "not_sent", Message: "the connection dropped"},
		{Status: "refused_by_tool", RefusedBy: "connection", Code: "app_closed", Message: "the connection dropped"},
	} {
		if got := toolOutcomeDetail(o); got != "" {
			t.Errorf("%s: the detail quotes %q", o.Status, got)
		}
		if got := toolOutcomeLine(o); strings.Contains(got, "dropped") || got == "" {
			t.Errorf("%s: the line reads %q", o.Status, got)
		}
	}
	signed := apiclient.ProposalDecision{Outcome: "allowed", ReceiptTraceID: "decision-tc-c0fc", ReceiptStatus: "pending"}
	anchored := signed
	anchored.ReceiptStatus = "anchored"
	if receiptLabel(signed) != "Signed" || receiptLabel(anchored) != "Signed and anchored" || receiptHeading(anchored) != "Receipt signed and anchored" || receiptTone(anchored) != "ok" {
		t.Errorf("labels: %q %q", receiptLabel(signed), receiptLabel(anchored))
	}
}

// A settled receipt's outcome reads in words, not as a code.
func TestReceiptSettledOutcomeLabels(t *testing.T) {
	for outcome, want := range map[string]string{"approved_once": "approved once", "rejected": "rejected", "expired": "not answered in time",
		"withdrawn": "withdrawn", "card_changed": "Card changed instead", "fit_on_retry": "fit the Card on a retry", "owner_required": "held for you"} {
		if got := receiptOutcomeLabel(outcome); got != want {
			t.Errorf("%s = %q, want %q", outcome, got, want)
		}
	}
	if receiptOutcomePillClass("rejected") != "receipt-outcome-blocked" || receiptOutcomePillClass("approved_once") != "receipt-outcome-allowed" {
		t.Error("settled outcomes have no tone")
	}
}
