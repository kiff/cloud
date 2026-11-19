package handlers

import (
	"context"
	"log/slog"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/mandate"
)

// The reasons a halt refuses a call with.
const (
	reasonHalted          = "halted"
	reasonHaltUnavailable = "halt_unavailable"
	msgHalted             = "The owner halted this agent, or the whole account, so KIFF decides none of its calls until it is resumed. Nothing was sent."
	msgHaltedBefore       = "This call was asked before the owner's halt, and nothing from before a halt goes through, even once resumed. Nothing was sent; a new call needs a new operation."
	msgHaltUnavailable    = "KIFF couldn't check whether this agent is halted, so it refused the call. Nothing was sent."
)

// haltRefusal refuses prop while its agent (a trial's included) or its
// account is halted, and after a resume when the operation was first
// decided at or before the latest halt. Recorded as a decision, so the
// Record shows it refused and why. A halt that can't be read refuses.
func (h *ProposalsHandler) haltRefusal(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, rt *kiffruntime.Runtime) (proposalResponse, bool) {
	if h.halts == nil {
		return proposalResponse{}, false
	}
	agent := prop.ActorID
	if a, ok := mandate.TrialAgent(agent); ok {
		agent = a
	}
	refuse := func(reason, msg string) (proposalResponse, bool) {
		reasons := []string{reason}
		h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
		return proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeBlocked), Reasons: reasons, Message: msg}, true
	}
	e, err := halts.For(ctx, h.halts, id.Tenant, agent)
	if err != nil {
		h.logger.ErrorContext(ctx, "proposals: halt unreadable; refusing", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		return refuse(reasonHaltUnavailable, msgHaltUnavailable)
	}
	if e.On() {
		return refuse(reasonHalted, msgHalted)
	}
	if e.At().IsZero() || rt == nil || rt.Audit == nil {
		return proposalResponse{}, false
	}
	// Resumed: the operation's first decision tells whether it was asked
	// before the halt.
	records, err := rt.Audit.Query(ctx, kiffaudit.Filter{TraceID: prop.ID, Kind: kiffaudit.Kind("proposal_decision")})
	if err != nil {
		h.logger.ErrorContext(ctx, "proposals: decisions unreadable after a halt; refusing", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		return refuse(reasonHaltUnavailable, msgHaltUnavailable)
	}
	for _, rec := range records {
		if e.Before(rec.CreatedAt) {
			return refuse(reasonHalted, msgHaltedBefore)
		}
	}
	return proposalResponse{}, false
}
