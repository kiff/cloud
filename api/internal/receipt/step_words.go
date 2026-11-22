package receipt

import (
	"strings"

	"github.com/kiff/cloud/api/internal/words"
)

// StepWord is a step's words in the reader's language, beside the receipt
// and outside its signature: the signed message stays as KIFF wrote it, in
// English. Code is the step's stable label (README "Step codes"); Detail is
// the free part a label carries (a reason, a next step, a tool's status),
// as it was signed: KIFF doesn't translate what a person or a vendor wrote.
type StepWord struct {
	Sequence int    `json:"sequence"`
	Code     string `json:"code"`
	// Again: the call was asked again and decided the same.
	Again  bool   `json:"again,omitempty"`
	Detail string `json:"detail,omitempty"`
	Words  string `json:"words"`
}

// The step codes. A new label is a new code; a code never changes meaning.
const (
	CodeDecisionAllowed         = "decision.allowed"
	CodeDecisionRefused         = "decision.refused"
	CodeDecisionHeldForOwner    = "decision.held_for_owner"
	CodeDecisionHeldForApproval = "decision.held_for_approval"
	CodeDecisionApprovedNoRoom  = "decision.approved_no_room"
	CodeDecisionInvalid         = "decision.invalid"
	CodeSettledApprovedOnce     = "settled.approved_once"
	CodeSettledRejected         = "settled.rejected"
	CodeSettledRejectedNext     = "settled.rejected_next_step"
	CodeSettledCardChanged      = "settled.card_changed"
	CodeSettledFitOnRetry       = "settled.fit_on_retry"
	CodeSettledExpired          = "settled.expired"
	CodeSettledWithdrawn        = "settled.withdrawn"
	CodeSettledWithdrawnReason  = "settled.withdrawn_reason"
	CodeToolReported            = "tool.reported"
	CodeHandoffHanded           = "handoff.handed"
	CodeHandoffDone             = "handoff.done"
	CodeHandoffSkipped          = "handoff.skipped"
	CodeHandoffNoAnswer         = "handoff.no_answer"
	CodeHandoffChecked          = "handoff.checked"
	CodeOwnerHalted             = "owner.halted"
	CodeOwnerResumed            = "owner.resumed"
	CodeStateNotAllowed         = "state.not_allowed"
)

// Codes are every step code, in the README's order.
var Codes = []string{
	CodeDecisionAllowed, CodeDecisionRefused, CodeDecisionHeldForOwner, CodeDecisionHeldForApproval,
	CodeDecisionApprovedNoRoom, CodeDecisionInvalid,
	CodeSettledApprovedOnce, CodeSettledRejected, CodeSettledRejectedNext, CodeSettledCardChanged,
	CodeSettledFitOnRetry, CodeSettledExpired, CodeSettledWithdrawn, CodeSettledWithdrawnReason,
	CodeToolReported,
	CodeHandoffHanded, CodeHandoffDone, CodeHandoffSkipped, CodeHandoffNoAnswer, CodeHandoffChecked,
	CodeOwnerHalted, CodeOwnerResumed, CodeStateNotAllowed,
}

// The signed messages each code is read from. Each is what the builders
// write (decisionOutcomeWord, settlementMessage, build_handoff.go,
// build_halt.go); a test builds each and reads its code back.
var (
	decisionWords = map[string]string{
		"allowed": CodeDecisionAllowed, "refused": CodeDecisionRefused, "held for its owner": CodeDecisionHeldForOwner,
		"held for approval": CodeDecisionHeldForApproval, "approved, waiting for room": CodeDecisionApprovedNoRoom,
		"invalid": CodeDecisionInvalid,
	}
	settledWords = map[string]string{
		"approved once": CodeSettledApprovedOnce, "rejected": CodeSettledRejected,
		"the card was changed instead":                                 CodeSettledCardChanged,
		"nobody answered: on a retry it fit the card as it then stood": CodeSettledFitOnRetry,
		"nobody answered before the hold expired":                      CodeSettledExpired, "withdrawn": CodeSettledWithdrawn,
	}
	handoffWords = map[string]string{
		"the owner says they did it": CodeHandoffDone, "the owner chose not to": CodeHandoffSkipped,
		"nobody answered before it expired: never read as done": CodeHandoffNoAnswer,
	}
)

const (
	askedAgain       = "asked again: "
	rejectedNext     = "rejected, next step: "
	withdrawnReason  = "withdrawn: "
	toolReported     = "the tool reported: "
	handoffChecked   = "the taf read the vendor after the owner's answer; the tool reported: "
	handoffHanded    = "the taf handed this step to its owner; KIFF sends nothing for it"
	stateNotAllowed  = "state_not_allowed: "
	ownerHaltedWords = "the owner halted it: KIFF decides none of its calls until it is resumed"
	ownerResumedWord = "the owner resumed it: new calls are decided again, and nothing asked before the halt"
)

// StepCode reads a step's code from its kind and signed message, and the
// free part the message carries. A step KIFF didn't word (a business's own
// audit record) has none: "".
func StepCode(s ChainStep) (code string, again bool, detail string) {
	m := s.Message
	switch s.Kind {
	case string(decisionKind):
		if rest, ok := strings.CutPrefix(m, askedAgain); ok {
			m, again = rest, true
		}
		return decisionWords[m], again && decisionWords[m] != "", ""
	case "hold_settled":
		if c, ok := settledWords[m]; ok {
			return c, false, ""
		}
		if rest, ok := strings.CutPrefix(m, rejectedNext); ok && rest != "" {
			return CodeSettledRejectedNext, false, rest
		}
		if rest, ok := strings.CutPrefix(m, withdrawnReason); ok && rest != "" {
			return CodeSettledWithdrawnReason, false, rest
		}
	case "tool_outcome":
		if rest, ok := strings.CutPrefix(m, toolReported); ok && rest != "" {
			return CodeToolReported, false, rest
		}
	case "handed_to_owner":
		if m == handoffHanded {
			return CodeHandoffHanded, false, ""
		}
	case "handoff_settled":
		return handoffWords[m], false, ""
	case "handoff_checked":
		if rest, ok := strings.CutPrefix(m, handoffChecked); ok && rest != "" {
			return CodeHandoffChecked, false, rest
		}
	case "owner_halt":
		if m == ownerHaltedWords {
			return CodeOwnerHalted, false, ""
		}
	case "owner_resume":
		if m == ownerResumedWord {
			return CodeOwnerResumed, false, ""
		}
	case "state_not_allowed":
		if rest, ok := strings.CutPrefix(m, stateNotAllowed); ok && rest != "" {
			return CodeStateNotAllowed, false, rest
		}
	}
	return "", false, ""
}

// StepWords are the chain's steps KIFF worded, in w's language.
func StepWords(chain []ChainStep, w *words.Words) []StepWord {
	out := []StepWord{}
	for i, s := range chain {
		code, again, detail := StepCode(s)
		line := w.ReceiptStep(code, detail)
		if line == "" {
			continue
		}
		if again {
			line = w.ReceiptAskedAgain(line)
		}
		seq := s.Sequence
		if seq == 0 {
			seq = i + 1
		}
		out = append(out, StepWord{Sequence: seq, Code: code, Again: again, Detail: detail, Words: line})
	}
	return out
}
