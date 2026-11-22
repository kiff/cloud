package receipt

import (
	"strings"
	"testing"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"

	"github.com/kiff/cloud/api/internal/words"
)

// Every step KIFF words is read back to its code from the message the
// builders sign: a builder whose words change without its code fails here.
func TestEveryStepKIFFWordsHasItsCode(t *testing.T) {
	type want struct {
		step   ChainStep
		code   string
		again  bool
		detail string
	}
	var cases []want
	decision := func(outcomes ...string) ChainStep {
		var recs []kiffaudit.Record
		for _, o := range outcomes {
			recs = append(recs, kiffaudit.Record{Kind: decisionKind, Data: map[string]any{"outcome": o}})
		}
		return ChainStep{Kind: string(decisionKind), Message: decisionStepMessage(recs, len(recs)-1)}
	}
	for outcome, code := range map[string]string{"allowed": CodeDecisionAllowed, "blocked": CodeDecisionRefused,
		"owner_required": CodeDecisionHeldForOwner, "approval_required": CodeDecisionHeldForApproval,
		"approved_no_room": CodeDecisionApprovedNoRoom, "invalid": CodeDecisionInvalid} {
		cases = append(cases, want{decision(outcome), code, false, ""}, want{decision(outcome, outcome), code, true, ""})
	}
	for _, s := range []struct {
		Settlement
		code, detail string
	}{
		{Settlement{Outcome: "approved_once"}, CodeSettledApprovedOnce, ""},
		{Settlement{Outcome: "rejected"}, CodeSettledRejected, ""},
		{Settlement{Outcome: "rejected", NextStep: "ask the customer"}, CodeSettledRejectedNext, "ask the customer"},
		{Settlement{Outcome: "card_changed"}, CodeSettledCardChanged, ""},
		{Settlement{Outcome: "fit_on_retry"}, CodeSettledFitOnRetry, ""},
		{Settlement{Outcome: "expired"}, CodeSettledExpired, ""},
		{Settlement{Outcome: "withdrawn"}, CodeSettledWithdrawn, ""},
		{Settlement{Outcome: "withdrawn", Reason: "duplicate"}, CodeSettledWithdrawnReason, "duplicate"},
	} {
		cases = append(cases, want{ChainStep{Kind: "hold_settled", Message: settlementMessage(s.Settlement)}, s.code, false, s.detail})
	}
	cases = append(cases, want{ChainStep{Kind: "tool_outcome", Message: toolReported + "succeeded"}, CodeToolReported, false, "succeeded"})

	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	for kind, code := range map[string]string{"halt": CodeOwnerHalted, "resume": CodeOwnerResumed} {
		r, err := BuildHalt("t1", HaltStep{ID: "h1", Kind: kind, At: at, By: "user_1"})
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, want{r.CausalChain[0], code, false, ""})
	}
	h := Handoff{ID: "ho1", TenantID: "t1", Agent: "ag", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	raised, err := BuildHandoffRaised(h)
	if err != nil {
		t.Fatal(err)
	}
	cases = append(cases, want{raised.CausalChain[0], CodeHandoffHanded, false, ""})
	for status, code := range map[string]string{"done": CodeHandoffDone, "skipped": CodeHandoffSkipped, "no_answer": CodeHandoffNoAnswer} {
		h := h
		h.Status = status
		if status == "done" {
			h.CheckedBy, h.Check = "p1", &SettlementTool{Status: "succeeded", At: at}
		}
		r, err := BuildHandoffSettled(h)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, want{r.CausalChain[1], code, false, ""})
		if status == "done" {
			cases = append(cases, want{r.CausalChain[2], CodeHandoffChecked, false, "succeeded"})
		}
	}
	cases = append(cases, want{ChainStep{Kind: "state_not_allowed", Message: stateNotAllowed + "action refund not allowed in state closed"},
		CodeStateNotAllowed, false, "action refund not allowed in state closed"})

	seen := map[string]bool{}
	for _, c := range cases {
		code, again, detail := StepCode(c.step)
		if code != c.code || again != c.again || detail != c.detail {
			t.Errorf("%s %q: got %q again=%v %q, want %q again=%v %q", c.step.Kind, c.step.Message, code, again, detail, c.code, c.again, c.detail)
		}
		seen[code] = true
	}
	for _, code := range Codes {
		if !seen[code] {
			t.Errorf("no builder writes %s", code)
		}
	}
}

// A step KIFF didn't word, or a message that isn't the builder's, has no
// code: its signed message is all there is.
func TestAStepKIFFDidntWordHasNoCode(t *testing.T) {
	for _, s := range []ChainStep{
		{Kind: "action_executed", Message: "refund of 500 cents issued: damaged"},
		{Kind: string(decisionKind), Message: "allowed, mostly"},
		{Kind: string(decisionKind), Message: "asked again: maybe"},
		{Kind: "hold_settled", Message: "rejected, next step: "},
		{Kind: "tool_outcome", Message: "allowed"},
		{Kind: "owner_halt", Message: "the owner resumed it: new calls are decided again, and nothing asked before the halt"},
	} {
		if code, again, _ := StepCode(s); code != "" || again {
			t.Errorf("%s %q: got %q", s.Kind, s.Message, code)
		}
	}
	got := StepWords([]ChainStep{{Sequence: 1, Kind: "action_executed", Message: "ticket closed"}}, words.Of(words.EN))
	if got == nil || len(got) != 0 {
		t.Errorf("a chain with nothing KIFF worded: %#v", got)
	}
}

// Every code has words in every language, and the words carry the step's
// free part as it was signed.
func TestEveryCodeHasWordsInEveryLanguage(t *testing.T) {
	for _, l := range words.All {
		w := words.Of(l)
		for _, code := range Codes {
			line := w.ReceiptStep(code, "«x»")
			if strings.TrimSpace(line) == "" {
				t.Errorf("%s: no words for %s", l, code)
			}
			if strings.Contains(w.ReceiptStepLines[code], "%s") != strings.Contains(words.Of(words.EN).ReceiptStepLines[code], "%s") {
				t.Errorf("%s %s: the free part is placed differently from English", l, code)
			}
		}
		if len(w.ReceiptStepLines) != len(Codes) {
			t.Errorf("%s: %d lines, %d codes", l, len(w.ReceiptStepLines), len(Codes))
		}
	}
	chain := []ChainStep{
		{Sequence: 1, Kind: string(decisionKind), Message: "held for approval"},
		{Sequence: 2, Kind: "hold_settled", Message: "rejected, next step: ask the customer"},
		{Sequence: 3, Kind: string(decisionKind), Message: "asked again: allowed"},
	}
	got := StepWords(chain, words.Of(words.DE))
	if len(got) != 3 || got[0].Words != "Zur Genehmigung zurückgehalten" || got[1].Words != "Zurückgewiesen. Nächster Schritt: ask the customer" ||
		got[1].Detail != "ask the customer" || !got[2].Again || got[2].Words != "Erneut angefragt: Erlaubt" || got[2].Sequence != 3 {
		t.Errorf("German: %#v", got)
	}
}
