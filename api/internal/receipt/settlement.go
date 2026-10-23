package receipt

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"
)

// A call held for a person gets its decision receipt within a sweep of
// the hold, so that receipt says "held" for good: reconcile never
// rebuilds a receipt that exists, and an anchored one must never change.
// When the hold is settled, a second receipt, the settled
// receipt, records how: every decision, the person's answer, and what
// the tool reported. It is linked to the first by proposal id and is the
// newer of the two, so GetByProposalID returns it.

// Settlement is how a held call ended.
type Settlement struct {
	ProposalID string
	HoldID     string
	// Outcome is the settled receipt's outcome: approved_once, rejected,
	// card_changed, fit_on_retry, expired or withdrawn.
	Outcome string
	// Answer is the person's answer (approve-once, reject, change-card),
	// "" when nobody answered (expired, withdrawn) or it is not recorded.
	Answer string
	// AnsweredBy, Role and SeparationOfDuties say who answered; empty
	// when not recorded. ExternalApprover names an external approver.
	AnsweredBy string
	// AnsweredByID is the sign-in id when AnsweredBy was resolved to an
	// email; empty otherwise.
	AnsweredByID       string
	Role               string
	SeparationOfDuties *bool
	ExternalApprover   string
	// At is when the hold was settled: answered, expired or withdrawn.
	At time.Time
	// NextStep and Note are what a rejection told the agent; Reason is
	// why a hold was withdrawn.
	NextStep, Note, Reason string
	// Tool is what the tool reported, nil when nothing ran or nothing is
	// recorded.
	Tool *SettlementTool
	// Device is the phone's signed proof of the answer, nil when
	// the answer was given without one.
	Device *SettlementDevice
	// AwaitRetry: the hold settles only once the agent has asked again
	// (a decision recorded at or after At), and, without a tool report,
	// SettlementGrace after that retry. A Card changed instead of an
	// answer is re-decided on the next call, never at the change.
	AwaitRetry bool
}

// SettlementGrace is how long a hold whose call ran (or may run) waits for
// the tool's report before it settles without one: a call outside the
// gateway never has one, and a lost report must not hold the receipt back
// for good.
const SettlementGrace = 15 * time.Minute

// ErrNotSettledYet is a hold whose settlement is still waiting for the
// agent's retry or the tool's report.
var ErrNotSettledYet = errors.New("receipt: the hold is not settled yet")

// SettlementDevice is what a phone signed to answer: anyone holding the
// receipt can check Signature over Message with PublicKey (SPKI DER,
// ECDSA P-256 over SHA-256), and KIFF's signature on the receipt binds
// that key to the person who approved the phone's sign-in.
type SettlementDevice struct {
	Name, KeyID, Message, Signature string
	PublicKey                       []byte
}

// SettlementTool is the tool's report on the call, as the gateway
// recorded it: its status, its own message, and when.
type SettlementTool struct {
	Status  string    `json:"status"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}

// SettlementSource lists a tenant's settled holds. Only settled ones:
// a hold still waiting, or approved and not yet used, is not settled.
type SettlementSource interface {
	Settled(ctx context.Context, tenantID string) ([]Settlement, error)
}

// SettledReceiptTraceID is the settled receipt's trace for a proposal.
func SettledReceiptTraceID(proposalID string) string { return "settled-" + proposalID }

// ErrSettlementInvalid is a settlement without a proposal id or outcome.
var ErrSettlementInvalid = errors.New("receipt: settlement needs a proposal id and an outcome")

// BuildSettlement assembles the settled receipt: the proposal's decision
// records and two more steps, how the hold was settled and what the tool
// reported, in the order they happened. The added steps carry their own
// values in Data, and their DataHash covers exactly that map, so the
// signature covers them.
func BuildSettlement(ctx context.Context, audit AuditReader, tenantID string, s Settlement, now time.Time) (Receipt, error) {
	if tenantID == "" {
		return Receipt{}, ErrTenantIDRequired
	}
	if s.ProposalID == "" || s.Outcome == "" {
		return Receipt{}, ErrSettlementInvalid
	}
	records, err := audit.Query(ctx, kiffaudit.Filter{TraceID: s.ProposalID, Kind: decisionKind})
	if err != nil {
		return Receipt{}, fmt.Errorf("query proposal_decision: %w", err)
	}
	orderRecords(records)
	if len(records) == 0 {
		return Receipt{}, ErrNoDecision
	}
	if s.AwaitRetry {
		var retry *time.Time
		// The retry is a decision at or after the change that is not
		// itself a hold: the hold being changed may share its instant,
		// and a retry held again has not settled anything.
		for _, rec := range records {
			outcome, _ := rec.Data["outcome"].(string)
			if !rec.CreatedAt.Before(s.At) && !heldOutcome(outcome) {
				t := rec.CreatedAt
				retry = &t
				break
			}
		}
		if retry == nil || (s.Tool == nil && now.Sub(*retry) < SettlementGrace) {
			return Receipt{}, ErrNotSettledYet
		}
	}
	type ranked struct {
		step ChainStep
		rank int // at the same instant: the settlement, then a decision, then the tool
	}
	var all []ranked
	for i, rec := range records {
		h, err := hashRecordData(rec)
		if err != nil {
			return Receipt{}, fmt.Errorf("hash audit record %s: %w", rec.ID, err)
		}
		all = append(all, ranked{ChainStep{Kind: string(rec.Kind), ActorID: rec.ActorID,
			OccurredAt: rec.CreatedAt.UTC(), Message: decisionStepMessage(records, i), DataHash: h, Data: decisionStepData(rec)}, 1})
	}
	answer := map[string]any{"hold_id": s.HoldID, "outcome": s.Outcome}
	for k, v := range map[string]string{"answer": s.Answer, "answered_by": s.AnsweredBy, "answered_by_id": s.AnsweredByID, "role": s.Role,
		"external_approver": s.ExternalApprover, "next_step": s.NextStep, "note": s.Note, "reason": s.Reason} {
		if v != "" {
			answer[k] = v
		}
	}
	if s.SeparationOfDuties != nil {
		answer["separation_of_duties"] = *s.SeparationOfDuties
	}
	if d := s.Device; d != nil {
		device := map[string]any{"key_id": d.KeyID, "public_key": base64.StdEncoding.EncodeToString(d.PublicKey),
			"signed_message": d.Message, "signature": d.Signature, "algorithm": "ecdsa-p256-sha256"}
		if d.Name != "" {
			device["name"] = d.Name
		}
		answer["device"] = device
	}
	h, err := hashFields(answer)
	if err != nil {
		return Receipt{}, err
	}
	all = append(all, ranked{ChainStep{Kind: "hold_settled", ActorID: s.AnsweredBy,
		OccurredAt: s.At.UTC(), Message: settlementMessage(s), DataHash: h, Data: answer}, 0})
	if t := s.Tool; t != nil {
		data := map[string]any{"status": t.Status, "at": t.At.UTC().Format(time.RFC3339Nano)}
		if t.Message != "" {
			data["message"] = t.Message
		}
		h, err := hashFields(data)
		if err != nil {
			return Receipt{}, err
		}
		all = append(all, ranked{ChainStep{Kind: "tool_outcome",
			OccurredAt: t.At.UTC(), Message: "the tool reported: " + t.Status, DataHash: h, Data: data}, 2})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].step.OccurredAt.Equal(all[j].step.OccurredAt) {
			return all[i].step.OccurredAt.Before(all[j].step.OccurredAt)
		}
		return all[i].rank < all[j].rank
	})
	steps := make([]ChainStep, len(all))
	for i, r := range all {
		steps[i] = r.step
		steps[i].Sequence = i + 1
	}
	decision := records[len(records)-1]
	actionName, _ := decision.Data["action"].(string)
	domainID, _ := decision.Data["domain_id"].(string)
	return Receipt{
		SchemaVersion: SchemaVersion,
		TraceID:       SettledReceiptTraceID(s.ProposalID),
		ProposalID:    s.ProposalID,
		TenantID:      tenantID,
		EntityID:      decision.EntityID,
		EntityType:    decision.EntityType,
		DomainID:      domainID,
		ActionName:    actionName,
		Outcome:       s.Outcome,
		// The last thing that happened.
		IssuedAt:    steps[len(steps)-1].OccurredAt,
		CausalChain: steps,
	}, nil
}

func settlementMessage(s Settlement) string {
	switch s.Outcome {
	case "approved_once":
		return "approved once"
	case "rejected":
		if s.NextStep != "" {
			return "rejected, next step: " + s.NextStep
		}
		return "rejected"
	case "card_changed":
		return "the card was changed instead"
	case "fit_on_retry":
		return "nobody answered: on a retry it fit the card as it then stood"
	case "expired":
		return "nobody answered before the hold expired"
	case "withdrawn":
		if s.Reason != "" {
			return "withdrawn: " + s.Reason
		}
		return "withdrawn"
	}
	return s.Outcome
}

func hashFields(v map[string]any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("hash settlement: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// decisionStepMessage says what decision i decided. The agent asking
// again with nothing changed writes an identical decision; it reads
// "asked again", so it is not mistaken for a duplicate record.
func decisionStepMessage(records []kiffaudit.Record, i int) string {
	outcome, _ := records[i].Data["outcome"].(string)
	word := decisionOutcomeWord(outcome)
	if word == "" {
		return records[i].Message
	}
	if i > 0 {
		if prev, _ := records[i-1].Data["outcome"].(string); prev == outcome {
			return "asked again: " + word
		}
	}
	return word
}

func decisionOutcomeWord(outcome string) string {
	switch outcome {
	case "allowed":
		return "allowed"
	case "blocked":
		return "refused"
	case "owner_required":
		return "held for its owner"
	case "approved_no_room":
		return "approved, waiting for room"
	case "approval_required":
		return "held for approval"
	case "invalid":
		return "invalid"
	}
	return ""
}

// AnswererEmails resolves a person's sign-in id to their email, for the
// settled receipt's answerer (Clerk in production).
type AnswererEmails interface {
	EmailForSubject(ctx context.Context, subject string) (string, error)
}

// WithAnswererEmails names a settled receipt's answerer by email instead
// of the sign-in id; a failed lookup keeps the id. Looked up once, when
// the receipt is minted.
func WithAnswererEmails(e AnswererEmails) PipelineOption {
	return func(p *Pipeline) { p.emails = e }
}

// WithSettlements turns on settled receipts.
func WithSettlements(s SettlementSource) PipelineOption {
	return func(p *Pipeline) { p.settlements = s }
}

// ReconcileSettlements mints a settled receipt for each settled hold that
// has none. Idempotent: an existing settled receipt is never rebuilt.
func (p *Pipeline) ReconcileSettlements(ctx context.Context, tenantID string, rt *kiffruntime.Runtime) (int, error) {
	if p.settlements == nil {
		return 0, nil
	}
	if rt == nil {
		return 0, ErrRuntimeMissing
	}
	if rt.Audit == nil {
		return 0, ErrAuditStoreMissing
	}
	settled, err := p.settlements.Settled(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("list settlements: %w", err)
	}
	built := 0
	for _, s := range settled {
		trace := SettledReceiptTraceID(s.ProposalID)
		if _, err := p.store.Get(ctx, tenantID, trace); err == nil {
			continue
		} else if !errors.Is(err, ErrReceiptNotFound) {
			return built, fmt.Errorf("store get: %w", err)
		}
		key := traceKey{TenantID: tenantID, TraceID: trace}
		if !p.acquire(key) {
			continue
		}
		// Ready first: a hold still waiting for its retry or the tool's
		// report is skipped before anything is looked up.
		r, err := BuildSettlement(ctx, rt.Audit, tenantID, s, p.now())
		// The person, not the sign-in id, looked up once, for the receipt
		// about to be saved; an external approver is already named.
		if err == nil && p.emails != nil && s.AnsweredBy != "" && s.ExternalApprover == "" {
			if email, lerr := p.emails.EmailForSubject(ctx, s.AnsweredBy); lerr == nil && email != "" {
				s.AnsweredByID, s.AnsweredBy = s.AnsweredBy, email
				r, err = BuildSettlement(ctx, rt.Audit, tenantID, s, p.now())
			}
		}
		if err == nil {
			err = p.store.Save(ctx, r)
		}
		p.release(key)
		switch {
		case errors.Is(err, ErrNotSettledYet):
			// Not yet: the next sweep looks again.
		case errors.Is(err, ErrNoDecision), errors.Is(err, ErrSettlementInvalid):
			p.logger.DebugContext(ctx, "receipt: settlement skipped", slog.String("tenant", tenantID), slog.String("proposal", s.ProposalID), slog.String("err", err.Error()))
		case err != nil:
			return built, fmt.Errorf("settle %s: %w", s.ProposalID, err)
		default:
			built++
		}
	}
	return built, nil
}

// heldOutcome is a decision that waits for a person.
func heldOutcome(outcome string) bool {
	switch outcome {
	case "owner_required", "approved_no_room", "approval_required":
		return true
	}
	return false
}
