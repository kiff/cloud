package pages

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// The receipt page tells one call in words: what happened, the proof,
// and how to check it. Everything here reads the signed receipt
// itself; nothing is added that the receipt does not say.

// ReceiptView is a receipt and its neighbours: Related is the call's
// other receipt (the one signed at the hold, or the settled one), and
// Signed where the signed JSON can be read; either may be empty.
type ReceiptView struct {
	R       *apiclient.Receipt
	Related string
	Signed  string
}

// SignedReceiptView is the signed JSON on its own page: JSON is the
// receipt as the API holds it, indented to read; Download serves the
// exact bytes.
type SignedReceiptView struct {
	TraceID, JSON, Back, Download string
}

// receiptCall is the call a receipt is about: its proposal id, else the
// trace without its decision-/settled- prefix.
func receiptCall(r *apiclient.Receipt) string {
	if r.ProposalID != "" {
		return r.ProposalID
	}
	return strings.TrimPrefix(strings.TrimPrefix(r.TraceID, "decision-"), "settled-")
}

// receiptAgent is the agent that asked: the first decision's actor, else
// the first actor in the chain.
func receiptAgent(r *apiclient.Receipt) string {
	for _, s := range r.CausalChain {
		if s.Kind == "proposal_decision" && s.ActorID != "" {
			return s.ActorID
		}
	}
	for _, s := range r.CausalChain {
		if s.ActorID != "" {
			return s.ActorID
		}
	}
	return ""
}

func receiptStepOf(r *apiclient.Receipt, kind string) *apiclient.ChainStep {
	for i := range r.CausalChain {
		if r.CausalChain[i].Kind == kind {
			return &r.CausalChain[i]
		}
	}
	return nil
}

func stepString(s *apiclient.ChainStep, key string) string {
	if s == nil {
		return ""
	}
	v, _ := s.Data[key].(string)
	return v
}

// receiptAnswerer names who answered a settled hold: an external
// approver by name, else the person (an email once resolved).
func receiptAnswerer(s *apiclient.ChainStep) string {
	if ext := stepString(s, "external_approver"); ext != "" {
		return ext + ", an external approver,"
	}
	if who := stepString(s, "answered_by"); who != "" {
		return who
	}
	return "Someone"
}

// receiptLead is the call in a sentence or two.
func receiptLead(r *apiclient.Receipt) string {
	agent := receiptAgent(r)
	if agent == "" {
		agent = "An agent"
	}
	asked := agent + " asked to run " + r.ActionName
	if receiptEntityShown(r) {
		asked += " on " + r.EntityID
	}
	asked += "."
	var what string
	if s := receiptStepOf(r, "hold_settled"); s != nil {
		who := receiptAnswerer(s)
		switch stepString(s, "outcome") {
		case "rejected":
			what = "Its Card held it for you; " + who + " rejected it"
			if ns := stepString(s, "next_step"); ns != "" {
				what += " and told the agent: " + NextStepLabel(ns)
			}
			what += ". Nothing ran."
		case "approved_once":
			what = "Its Card held it for you; " + who + " approved it once."
		case "card_changed":
			what = "Its Card held it for you; " + who + " changed the Card instead, and when the agent asked again KIFF decided again."
		case "fit_on_retry":
			what = "Its Card held it; nobody answered, and when the agent asked again the call fit the Card as it then stood."
		case "expired":
			what = "Its Card held it for you; nobody answered before the hold ran out, so nothing ran."
		case "withdrawn":
			what = "Its Card held it, and it was withdrawn before it was used"
			if reason := stepString(s, "reason"); reason != "" {
				what += ": " + storyWithdrawn(reason)
			}
			what += ". Nothing ran."
		}
	} else {
		switch r.Outcome {
		case "allowed":
			what = "It fit its Card, so KIFF allowed it."
		case "blocked":
			what = "KIFF refused it, so nothing was authorized."
		case "owner_required":
			what = "Its Card held it for its owner. This receipt was signed at the hold; nothing was authorized then."
		case "approved_no_room":
			what = "It was approved, but its Card had no room for it yet, so nothing was authorized."
		case "approval_required":
			what = "KIFF held it for an approver. This receipt was signed at the hold; nothing was authorized then."
		case "invalid":
			what = "The request was not valid, so KIFF decided nothing."
		}
	}
	if t := receiptStepOf(r, "tool_outcome"); t != nil {
		o, unrecorded := receiptToolOutcome(r, t)
		l := toolOutcomeLabel(o)
		if unrecorded {
			l = "refused before it was done"
		}
		if l != "" {
			what = strings.TrimSpace(what + " Then " + l + ".")
		}
	}
	return strings.TrimSpace(asked + " " + what)
}

// receiptStep is one line of the receipt's timeline.
type receiptStep struct {
	Title, Detail string
	At            time.Time
	// Kind colours the dot: asked, yes, no or done.
	Kind string
}

// receiptToolOutcome is a receipt's tool step as an outcome, and whether it's
// a refusal from before receipts said who refused a call (unrecorded): such a
// receipt can't tell a connection's refusal from an app's own, so its message
// is never quoted and the page says only that it was refused.
func receiptToolOutcome(r *apiclient.Receipt, s *apiclient.ChainStep) (*apiclient.ToolOutcome, bool) {
	o := &apiclient.ToolOutcome{Status: stepString(s, "status"), Message: stepString(s, "message"),
		RefusedBy: stepString(s, "refused_by"), Code: stepString(s, "code")}
	unrecorded := o.Status == "refused_by_tool" && o.RefusedBy == "" && r.IssuedAt.Before(refusedByRecordedSince)
	if unrecorded {
		o.Message = ""
	}
	return o, unrecorded
}

// refusedByRecordedSince is when receipts began to say who refused a call
// (`refused_by` on the tool step): went live at 00:24 UTC on 19 Nov, and
// this is a few minutes after, so no receipt built by the code before it
// counts as recording it. A receipt issued before it quotes no refusal's
// message, an app's own included.
var refusedByRecordedSince = time.Date(2026, time.November, 19, 0, 30, 0, 0, time.UTC)

// receiptSteps is the causal chain in words, oldest first.
func receiptSteps(r *apiclient.Receipt) []receiptStep {
	var out []receiptStep
	agent := receiptAgent(r)
	firstDecision := true
	// "Nothing ran" ends the timeline only when something refused the
	// call and nothing ever authorized it: a later allowed decision, or
	// an approval, rules it out, and a missing tool report proves
	// nothing.
	refused, authorized := false, false
	lastDecision := -1
	settled := receiptStepOf(r, "hold_settled") != nil
	for i, s := range r.CausalChain {
		if s.Kind == "proposal_decision" {
			lastDecision = i
		}
	}
	for i := range r.CausalChain {
		s := &r.CausalChain[i]
		step := receiptStep{At: s.OccurredAt, Kind: "asked"}
		switch s.Kind {
		case "proposal_decision":
			msg := s.Message
			if msg == "" || msg == "proposal decision recorded" {
				// An older receipt names no outcome per step; its last
				// decision is the receipt's own.
				msg = "decided"
				// Only a decision receipt's outcome is its last decision;
				// a settled receipt's outcome is how the hold was
				// settled.
				if i == lastDecision && !settled && receiptOutcomeLabel(r.Outcome) != "" {
					msg = receiptOutcomeLabel(r.Outcome)
				}
			}
			if firstDecision && agent != "" && !strings.HasPrefix(msg, "asked again") {
				step.Title = agent + " asked: " + msg
			} else {
				step.Title = sentenceCase(msg)
			}
			firstDecision = false
			switch {
			case strings.Contains(msg, "allowed"):
				step.Kind, authorized = "yes", true
			case strings.Contains(msg, "refused"):
				step.Kind, refused = "no", true
			}
		case "hold_settled":
			step.Title, step.Kind = settledStepTitle(s), "yes"
			switch stepString(s, "outcome") {
			case "rejected", "expired", "withdrawn":
				step.Kind, refused = "no", true
			default:
				authorized = true
			}
			step.Detail = settledStepDetail(s)
		case "tool_outcome":
			o, unrecorded := receiptToolOutcome(r, s)
			step.Title = sentenceCase(toolOutcomeLabel(o))
			if step.Title == "" {
				step.Title = "The tool reported"
			}
			step.Kind = storyOutcomeKind(o.Status)
			// Only an app's own error is quoted; the receipt keeps the
			// connection's and the gateway's text as the record, unquoted.
			step.Detail = toolOutcomeDetail(o)
			if unrecorded {
				// Who refused it isn't known, so the page says only what is.
				step.Title = "Refused before it was done"
				step.Detail = "This older receipt doesn't record whether the tool or its connection refused it."
			}
			authorized = true
		default:
			step.Title = sentenceCase(strings.ReplaceAll(s.Kind, "_", " "))
			step.Detail = s.Message
			if s.ActorID != "" {
				step.Detail = strings.TrimSpace(s.ActorID + " " + step.Detail)
			}
		}
		out = append(out, step)
	}
	if refused && !authorized {
		out = append(out, receiptStep{Title: "Nothing ran", Detail: "KIFF never authorized the call.", Kind: "done"})
	}
	return out
}

func settledStepTitle(s *apiclient.ChainStep) string {
	switch stepString(s, "outcome") {
	case "rejected":
		if ns := stepString(s, "next_step"); ns != "" {
			return "Rejected: " + NextStepLabel(ns)
		}
		return "Rejected"
	case "approved_once":
		return "Approved once"
	case "card_changed":
		return "Card changed instead"
	case "fit_on_retry":
		return "Fit the Card on a retry"
	case "expired":
		return "Not answered in time"
	case "withdrawn":
		return "Withdrawn"
	}
	return "Settled"
}

// receiptDevice is the phone that signed a settled answer: its name and
// its key's fingerprint, the one its sign-in showed. ok is false when the
// answer was not signed by a phone.
func receiptDevice(s *apiclient.ChainStep) (name, fingerprint string, ok bool) {
	if s == nil {
		return "", "", false
	}
	d, _ := s.Data["device"].(map[string]any)
	key, _ := d["public_key"].(string)
	der, err := base64.StdEncoding.DecodeString(key)
	if d == nil || err != nil || len(der) == 0 {
		return "", "", false
	}
	sum := sha256.Sum256(der)
	h := hex.EncodeToString(sum[:8])
	name, _ = d["name"].(string)
	if name == "" {
		name = "a phone"
	}
	return name, h[0:4] + " " + h[4:8] + " " + h[8:12] + " " + h[12:16], true
}

// settledStepDetail is who answered, their role and separation of
// duties, the phone that signed it, and the note.
func settledStepDetail(s *apiclient.ChainStep) string {
	var parts []string
	if stepString(s, "answer") != "" || stepString(s, "external_approver") != "" {
		who := strings.TrimSuffix(receiptAnswerer(s), ",")
		if role := stepString(s, "role"); role != "" {
			who += ", " + articleFor(role) + " " + role
		}
		parts = append(parts, who)
	}
	if sod, ok := s.Data["separation_of_duties"].(bool); ok {
		parts = append(parts, "separation of duties "+map[bool]string{true: "on", false: "off"}[sod])
	}
	if name, fp, ok := receiptDevice(s); ok {
		parts = append(parts, "signed on "+name+" with key "+fp)
	}
	if note := stepString(s, "note"); note != "" {
		parts = append(parts, "note: “"+note+"”")
	}
	return strings.Join(parts, " · ")
}

func articleFor(word string) string {
	if word != "" && strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

// receiptProof says how far the receipt's proof goes: not signed yet,
// signed, or signed and anchored in a public record.
func receiptProof(r *apiclient.Receipt) (title, text string) {
	a := r.Attestation
	switch {
	case a == nil:
		return "Not signed yet", "KIFF signs every receipt shortly after the decision. If this stays unsigned for more than a few minutes, tell us with the receipt id below."
	case a.AnchorTxHash != "":
		return "Signed and anchored", "KIFF signed this receipt and put its fingerprint in a public record on " + formatTime(a.AnchoredAt) + ". Anyone you give it to can check it has not been changed since."
	}
	return "Signed", "KIFF signed this receipt. Anyone you give it to can check it has not been changed. It is not in a public record yet."
}

// receiptCovers are the facts the receipt is about, label and value.
func receiptCovers(r *apiclient.Receipt) [][2]string {
	var out [][2]string
	if a := receiptAgent(r); a != "" {
		out = append(out, [2]string{"Agent", a})
	}
	out = append(out, [2]string{"Call", receiptCall(r)})
	if receiptEntityShown(r) {
		out = append(out, [2]string{"On", r.EntityID})
	}
	if r.DomainID != "" {
		out = append(out, [2]string{"Domain", r.DomainID})
	}
	if r.TenantID != "" {
		out = append(out, [2]string{"Account", r.TenantID})
	}
	return out
}

// receiptRelatedLabel names the call's other receipt.
func receiptRelatedLabel(v ReceiptView) string {
	if strings.HasPrefix(v.R.TraceID, "settled-") {
		return "This call also has its first receipt, signed when it was held."
	}
	return "This call also has a settled receipt, signed once it was answered."
}

// receiptRelatedLink is the other receipt's link text.
func receiptRelatedLink(v ReceiptView) string {
	if strings.HasPrefix(v.R.TraceID, "settled-") {
		return "The receipt at the hold"
	}
	return "The settled receipt"
}

// receiptChip is the head's outcome chip: label and tone.
func receiptChip(r *apiclient.Receipt) (string, string) {
	label := sentenceCase(receiptOutcomeLabel(r.Outcome))
	switch receiptOutcomePillClass(r.Outcome) {
	case "receipt-outcome-allowed":
		return label, "ok"
	case "receipt-outcome-blocked":
		return label, "no"
	case "receipt-outcome-held":
		return label, "warn"
	}
	return label, "off"
}

func shortSignature(sig string) string {
	if len(sig) <= 20 {
		return sig
	}
	return fmt.Sprintf("%s…%s", sig[:10], sig[len(sig)-8:])
}
