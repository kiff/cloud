package pages

import (
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// The statement read in production (2026-10-08): each action drew twice,
// once per quantity, and the totals ran together as "2 in total.40 in
// total." One row per action, totals in words.
func TestLedgerIsOneRowPerAction(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 9, 36, 0, 0, time.UTC)
	draws := []apiclient.MandateDraw{
		{ProposalID: "tc-a", Action: "refund_order", EntityID: "tc-a", Quantity: "sum(amount)", Amount: 10, DrawnAt: t0},
		{ProposalID: "tc-a", Action: "refund_order", EntityID: "tc-a", Quantity: "count", Amount: 1, DrawnAt: t0},
	}
	rows := drawRows(draws)
	if len(rows) != 1 || rows[0].Amounts["sum(amount)"] != 10 || rows[0].Amounts["count"] != 1 {
		t.Fatalf("draw rows: %+v", rows)
	}
	limits := []apiclient.MandateLimit{{Quantity: "sum(amount)"}, {Quantity: "count"}}
	if qs := ledgerQuantities(limits, rows); len(qs) != 2 || qs[0] != "sum(amount)" || qs[1] != "count" {
		t.Fatalf("columns: %v", qs)
	}
	if quantityLabel("count") != "Actions" || quantityLabel("sum(amount_cents)") != "Amount" {
		t.Fatalf("labels: %q %q", quantityLabel("count"), quantityLabel("sum(amount_cents)"))
	}

	st := &apiclient.MandateStatement{Limits: limits, ApprovedOutside: &apiclient.ApprovedOutside{
		Entries: []apiclient.ApprovedOutsideEntry{
			{ExceptionID: "e1", ProposalID: "tc-1", Action: "refund_order", Quantity: "sum(amount)", Amount: 25, RecordedAt: t0},
			{ExceptionID: "e1", ProposalID: "tc-1", Action: "refund_order", Quantity: "count", Amount: 1, RecordedAt: t0},
			{ExceptionID: "e2", ProposalID: "tc-2", Action: "refund_order", Quantity: "sum(amount)", Amount: 15, RecordedAt: t0.Add(15 * time.Minute)},
			{ExceptionID: "e2", ProposalID: "tc-2", Action: "refund_order", Quantity: "count", Amount: 1, RecordedAt: t0.Add(15 * time.Minute)},
		},
		Totals: map[string]int64{"count": 2, "sum(amount)": 40},
	}}
	if n := approvedCount(st); n != 2 {
		t.Fatalf("approved count %d, want 2", n)
	}
	if rows := approvedRows(st.ApprovedOutside.Entries); rows[0].ExceptionID != "e2" {
		t.Fatalf("most recent first: %+v", rows)
	}
	if got := approvedTotals(st); got != "40 amount, 2 actions" {
		t.Fatalf("totals: %q", got)
	}
}

func TestApprovedOutcomeNeverReadsRefusalAsDone(t *testing.T) {
	answers := map[string]apiclient.CardException{
		"e1": {ID: "e1", Status: "consumed", ToolOutcome: &apiclient.ToolOutcome{Status: "refused_by_tool"}},
		"e2": {ID: "e2", Status: "consumed", ToolOutcome: &apiclient.ToolOutcome{Status: "done"}},
	}
	o := ledgerOutcomes{byID: answers}
	if l, warn := approvedOutcome(o, "e1"); l != "Refused by the tool" || !warn {
		t.Errorf("refused: %q %v", l, warn)
	}
	if l, warn := approvedOutcome(o, "e2"); l != "Tool reported it done" || warn {
		t.Errorf("done: %q %v", l, warn)
	}
	// A read that lacks the action, or no read at all, is said, never a
	// blank cell.
	if l, warn := approvedOutcome(o, "missing"); l != "Answer record unavailable" || !warn {
		t.Errorf("missing: %q %v", l, warn)
	}
	if l, warn := approvedOutcome(ledgerOutcomes{unreadable: true}, "e2"); l != "Could not be read" || !warn {
		t.Errorf("unreadable: %q %v", l, warn)
	}
}

// A Card change does not authorize the retry: what the tool recorded is
// what happened, and without a record the retry is decided again.
func TestChangedAnswerShowsTheToolOutcome(t *testing.T) {
	for status, want := range map[string]string{
		"refused_by_tool": "Refused by the tool",
		"unknown":         "Outcome unknown",
		"":                "Card changed; a retry is decided again",
	} {
		e := apiclient.CardException{ID: "e", Status: "changed"}
		if status != "" {
			e.ToolOutcome = &apiclient.ToolOutcome{Status: status}
		}
		if got := answeredOutcomeLabel(e); got != want {
			t.Errorf("changed, outcome %q: got %q, want %q", status, got, want)
		}
	}
}
