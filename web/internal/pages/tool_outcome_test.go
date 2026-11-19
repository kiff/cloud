package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// seen in the production test on 2026-10-08: the owner approved a €25
// refund once, the tool refused it, and Needs you said "Approved once,
// done". An approval used is not a call done; the tool's own outcome is
// shown next to it, and a refusal or an unknown result never reads as done.
func TestNeedsYouSaysWhatTheToolDidWithAnApprovedCall(t *testing.T) {
	at := time.Date(2026, 10, 8, 9, 34, 0, 0, time.UTC)
	refused := &apiclient.ToolOutcome{Status: "refused_by_tool", Message: "Refund amount (€25.00) is greater than unrefunded amount on charge (€20.00)"}
	for _, c := range []struct {
		outcome *apiclient.ToolOutcome
		label   string
		warn    bool
	}{
		{nil, "Approved once, used", false},
		{&apiclient.ToolOutcome{Status: "done"}, "Approved once, the tool reported it done", false},
		{refused, "Approved once, refused by the tool", true},
		{&apiclient.ToolOutcome{Status: "unknown", Message: "Sent to the tool, but its response was lost."}, "Approved once, outcome unknown", true},
		{&apiclient.ToolOutcome{Status: "not_sent", Message: "The tool could not be reached."}, "Approved once, not sent to the tool", true},
		// a review: an outcome that was never recorded is not done.
		{&apiclient.ToolOutcome{Status: "not_recorded"}, "Approved once, outcome not recorded", true},
	} {
		e := apiclient.CardException{ID: "exc-1", Status: "consumed", Action: "refund_order", ResolvedAt: &at, ToolOutcome: c.outcome}
		if got := exceptionStatusLabel(e); got != c.label {
			t.Errorf("label = %q, want %q", got, c.label)
		}
		if strings.Contains(exceptionStatusLabel(e), "done") && c.warn {
			t.Errorf("%q reads as done", exceptionStatusLabel(e))
		}
		if warn := exceptionStateClass(e) == "dash-portfolio-state-observe"; warn != c.warn {
			t.Errorf("%s: warn = %v", c.label, warn)
		}
	}

	e := apiclient.CardException{ID: "exc-1", Status: "consumed", Action: "refund_order", ResolvedAt: &at, ToolOutcome: refused}
	var b strings.Builder
	if err := needsYouItem(e, true, nil).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "The tool refused the call. Its error: “Refund amount (€25.00) is greater than unrefunded amount on charge (€20.00)”") {
		t.Errorf("detail lacks the tool's refusal: %s", b.String())
	}
}

// Activity shows, on each allowed decision, what the tool reported, with
// the tool's message in the title. Other decisions show none.
func TestActivityShowsTheToolOutcomeNextToTheDecision(t *testing.T) {
	now := time.Now().UTC()
	e := &ControlEvidence{Decisions: ControlEvidenceDecisions{Total: 3, Rows: []apiclient.ProposalDecision{
		{ProposalID: "p1", Outcome: "allowed", CreatedAt: now,
			ToolOutcome: &apiclient.ToolOutcome{Status: "refused_by_tool", Message: "card declined"}},
		{ProposalID: "p2", Outcome: "allowed", CreatedAt: now, ToolOutcome: &apiclient.ToolOutcome{Status: "done"}},
		{ProposalID: "p3", Outcome: "owner_required", CreatedAt: now},
	}}}
	var b strings.Builder
	if err := controlRoomDecisionsTable(e).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	for _, want := range []string{
		"<th>Tool</th>",
		`class="cr-tool cr-tool-warn" title="card declined">refused by the tool</span>`,
		`class="cr-tool" title="">the tool reported it done</span>`,
		`<span class="cr-tool-none">—</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
}
