package receipt

import (
	"encoding/base64"
	"errors"
	"sort"
	"time"
)

// build_handoff.go: the receipts of a step a taf handed to its owner ("Your
// step"). A hand-off gets two, as a held call does:
//   - the raised receipt, when the taf hands the step over: outcome
//     handed_to_owner;
//   - the settled receipt, when it closes: done, skipped or no_answer, with
//     who answered and the phone's proof, and, after "Done", the call the
//     taf made to read the vendor and what the tool reported, or nothing.
//
// The words the owner reads (what, why, about) never enter a receipt: it
// carries their hash, so they can be erased and the receipt still
// verifies. The link the owner opens enters only as its host. A hand-off
// grants nothing and sends nothing: the step stays refused at the gateway,
// and the receipt says only what the owner answered.

// HandoffAction is a hand-off receipt's action name.
const HandoffAction = "hand_off"

// HandoffOutcome is the raised receipt's outcome, beside a decision's
// allowed, held and refused.
const HandoffOutcome = "handed_to_owner"

// ErrHandoffInvalid is a hand-off without its id, agent or tenant.
var ErrHandoffInvalid = errors.New("receipt: a hand-off needs its id, agent and tenant")

// ErrHandoffOpen is a settled receipt asked for while the hand-off waits.
var ErrHandoffOpen = errors.New("receipt: the hand-off has not closed")

// Handoff is what a hand-off's receipts are built from.
type Handoff struct {
	ID, TenantID, Agent string
	// Status is waiting, done, skipped or no_answer.
	Status     string
	LinkHost   string
	WordsHash  string
	PreparedBy []string
	Version    int
	CreatedAt  time.Time
	ExpiresAt  time.Time
	// AnsweredBy and AnsweredByID say who answered; AnsweredAt when.
	AnsweredBy, AnsweredByID string
	AnsweredAt               *time.Time
	Device                   *SettlementDevice
	// CheckedBy is the call that read the vendor after "Done", and Check
	// what the gateway recorded on it; nil when there was none.
	CheckedBy string
	Check     *SettlementTool
}

// HandoffReceiptTraceID is the raised receipt's id; HandoffSettledTraceID
// the settled one's. Both are deterministic, so re-minting upserts.
func HandoffReceiptTraceID(id string) string { return "handoff-" + id }
func HandoffSettledTraceID(id string) string { return "handoff-settled-" + id }

func handedStep(h Handoff) (ChainStep, error) {
	prepared := append([]string{}, h.PreparedBy...)
	sort.Strings(prepared)
	data := map[string]any{"handoff_id": h.ID, "agent": h.Agent, "words_sha256": h.WordsHash,
		"prepared_by": prepared, "version": h.Version, "expires_at": h.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	if h.LinkHost != "" {
		data["link_host"] = h.LinkHost
	}
	hash, err := hashFields(data)
	if err != nil {
		return ChainStep{}, err
	}
	return ChainStep{Kind: "handed_to_owner", ActorID: h.Agent, OccurredAt: h.CreatedAt.UTC(),
		Message: "the taf handed this step to its owner; KIFF sends nothing for it", DataHash: hash, Data: data}, nil
}

func handoffReceipt(h Handoff, traceID, outcome string, steps []ChainStep) Receipt {
	for i := range steps {
		steps[i].Sequence = i + 1
	}
	return Receipt{
		SchemaVersion: SchemaVersion,
		TraceID:       traceID,
		ProposalID:    h.ID,
		TenantID:      h.TenantID,
		EntityID:      h.Agent,
		EntityType:    "agent",
		ActionName:    HandoffAction,
		Outcome:       outcome,
		IssuedAt:      steps[len(steps)-1].OccurredAt,
		CausalChain:   steps,
	}
}

func validHandoff(h Handoff) error {
	if h.ID == "" || h.TenantID == "" || h.Agent == "" {
		return ErrHandoffInvalid
	}
	return nil
}

// BuildHandoffRaised is the receipt of handing the step over.
func BuildHandoffRaised(h Handoff) (Receipt, error) {
	if err := validHandoff(h); err != nil {
		return Receipt{}, err
	}
	step, err := handedStep(h)
	if err != nil {
		return Receipt{}, err
	}
	return handoffReceipt(h, HandoffReceiptTraceID(h.ID), HandoffOutcome, []ChainStep{step}), nil
}

// BuildHandoffSettled is the receipt of how the hand-off closed: the owner's
// answer, or none, and, after "Done", the taf's read of the vendor.
func BuildHandoffSettled(h Handoff) (Receipt, error) {
	if err := validHandoff(h); err != nil {
		return Receipt{}, err
	}
	if h.Status != "done" && h.Status != "skipped" && h.Status != "no_answer" {
		return Receipt{}, ErrHandoffOpen
	}
	handed, err := handedStep(h)
	if err != nil {
		return Receipt{}, err
	}
	steps := []ChainStep{handed}
	answer := map[string]any{"handoff_id": h.ID, "outcome": h.Status}
	at, actor, message := h.ExpiresAt.UTC(), "", "nobody answered before it expired: never read as done"
	if h.Status != "no_answer" {
		if h.AnsweredAt != nil {
			at = h.AnsweredAt.UTC()
		}
		actor = h.AnsweredBy
		message = map[string]string{"done": "the owner says they did it", "skipped": "the owner chose not to"}[h.Status]
		for k, v := range map[string]string{"answered_by": h.AnsweredBy, "answered_by_id": h.AnsweredByID} {
			if v != "" {
				answer[k] = v
			}
		}
		if d := h.Device; d != nil {
			device := map[string]any{"key_id": d.KeyID, "public_key": base64.StdEncoding.EncodeToString(d.PublicKey),
				"signed_message": d.Message, "signature": d.Signature, "algorithm": "ecdsa-p256-sha256"}
			if d.Name != "" {
				device["name"] = d.Name
			}
			answer["device"] = device
		}
	}
	hash, err := hashFields(answer)
	if err != nil {
		return Receipt{}, err
	}
	steps = append(steps, ChainStep{Kind: "handoff_settled", ActorID: actor, OccurredAt: at, Message: message, DataHash: hash, Data: answer})
	if h.Status == "done" && h.CheckedBy != "" && h.Check != nil {
		data := map[string]any{"proposal_id": h.CheckedBy, "status": h.Check.Status, "at": h.Check.At.UTC().Format(time.RFC3339Nano)}
		hash, err := hashFields(data)
		if err != nil {
			return Receipt{}, err
		}
		when := h.Check.At.UTC()
		if when.Before(at) {
			when = at
		}
		steps = append(steps, ChainStep{Kind: "handoff_checked", ActorID: h.Agent, OccurredAt: when,
			Message: "the taf read the vendor after the owner's answer; the tool reported: " + h.Check.Status, DataHash: hash, Data: data})
	}
	return handoffReceipt(h, HandoffSettledTraceID(h.ID), h.Status, steps), nil
}
