package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// HaltStep is one halt or resume of an agent, or of the whole account:
// what a halt receipt signs.
type HaltStep struct {
	ID string // the event's id
	// AgentID is the agent, or "" for the whole account.
	AgentID string
	// Kind is "halt" or "resume".
	Kind string
	At   time.Time
	By   string
}

// HaltReceiptTraceID is a halt receipt's id: apart from decision and
// execution receipts, deterministic for its event.
func HaltReceiptTraceID(eventID string) string { return "halt-" + eventID }

// BuildHalt is the receipt for a halt or a resume: one step, its values in
// Data and DataHash over exactly them, as a settled receipt's answer is.
// The anchor loop signs it like any receipt.
func BuildHalt(tenantID string, s HaltStep) (Receipt, error) {
	if tenantID == "" {
		return Receipt{}, ErrTenantIDRequired
	}
	if s.ID == "" || (s.Kind != "halt" && s.Kind != "resume") {
		return Receipt{}, errors.New("receipt: a halt step needs its id and a kind of halt or resume")
	}
	entity, entityType := s.AgentID, "agent"
	if s.AgentID == "" {
		entity, entityType = tenantID, "account"
	}
	data := map[string]any{"kind": s.Kind, "agent": s.AgentID, "by": s.By, "at": s.At.UTC().Format(time.RFC3339Nano)}
	encoded, err := json.Marshal(data)
	if err != nil {
		return Receipt{}, err
	}
	sum := sha256.Sum256(encoded)
	outcome, message := "halted", "the owner halted it: KIFF decides none of its calls until it is resumed"
	if s.Kind == "resume" {
		outcome, message = "resumed", "the owner resumed it: new calls are decided again, and nothing asked before the halt"
	}
	return Receipt{
		SchemaVersion: SchemaVersion,
		TraceID:       HaltReceiptTraceID(s.ID),
		TenantID:      tenantID,
		EntityID:      entity,
		EntityType:    entityType,
		ActionName:    s.Kind,
		Outcome:       outcome,
		IssuedAt:      s.At.UTC(),
		CausalChain: []ChainStep{{Sequence: 1, Kind: "owner_" + s.Kind, ActorID: s.By, OccurredAt: s.At.UTC(),
			Message: message, DataHash: hex.EncodeToString(sum[:]), Data: data}},
	}, nil
}
