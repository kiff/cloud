package handlers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/permit"
)

// Execution permits. A decide request for a call whose
// connection is in verify or relay mode carries a permit request; an
// allowed decision then returns the call's one permit, which the tool or
// relay verifies itself.

// Refusal reasons for permit requests.
const (
	reasonPermitsUnavailable  = "permits_unavailable"
	reasonPermitNeedsID       = "permit_needs_proposal_id"
	reasonPermitInvalid       = "invalid_permit_request"
	reasonPermitAlreadyIssued = "permit_already_issued"
	reasonPermitNotIssued     = "permit_not_issued"
	reasonPermitNoTarget      = "permit_not_configured"
	reasonPermitSubstituted   = "permit_target_mismatch"
	reasonPermitParameters    = "permit_parameters_mismatch"
	reasonPermitNotConsumed   = "permit_not_consumed"
	reasonPermitRequired      = "permit_required"
	reasonPermitBindingLate   = "permit_binding_late"
)

// decidedChecker is the part of the mandate authorizer that says whether a
// proposal id was already decided on a Card (mandate.Authorizer.Decided).
type decidedChecker interface {
	Decided(ctx context.Context, tenantID, proposalID string) (bool, error)
}

// WithProposalsPermits installs the permit issuer. Nil leaves permits
// off: a request that asks for one is refused, so nothing runs without.
func WithProposalsPermits(i *permit.Issuer) ProposalsOption {
	return func(h *ProposalsHandler) { h.permits = i }
}

// WithProposalsPermitTargets installs where each action's permit goes. It
// is installed whether or not permits are on: an action with a target is
// decided only with a permit request, so every decision of it records its
// binding at the first decision.
func WithProposalsPermitTargets(t permit.Targets) ProposalsOption {
	return func(h *ProposalsHandler) { h.permitTargets = t }
}

// permitRequired refuses a decide request without a permit request for an
// action connected in verify or relay mode. Otherwise an operation could
// be held, approved and released with no binding, and a later request
// could bind it to another call.
func (h *ProposalsHandler) permitRequired(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, spec domain.Spec) (proposalResponse, bool) {
	if h.permitTargets == nil {
		return proposalResponse{}, false
	}
	// A gateway call whose amount couldn't be read is only ever recorded as
	// blocked (evaluate, amountRefusal): it can't be held or allowed, so
	// nothing needs binding, and it has no amount to ask a permit with.
	if unverified, _ := takeAmountUnverified(spec, prop); unverified {
		return proposalResponse{}, false
	}
	_, ok, err := h.permitTargets.Get(ctx, id.Tenant, cardDomainFor(ctx, spec), prop.ActionName)
	if err != nil {
		h.logger.ErrorContext(ctx, "proposals: permit target unavailable; refusing",
			slog.String("tenant", id.Tenant), slog.String("proposal_id", prop.ID), slog.String("err", err.Error()))
		h.recordRejection(ctx, id, prop, outcomeBlocked, []string{reasonPermitsUnavailable})
		return proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeBlocked), Reasons: []string{reasonPermitsUnavailable},
			Message: "KIFF could not read whether this action needs an execution permit, so it was not authorized; retry later"}, true
	}
	if !ok {
		return proposalResponse{}, false
	}
	h.recordRejection(ctx, id, prop, outcomeInvalid, []string{reasonPermitRequired})
	return proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeInvalid), Reasons: []string{reasonPermitRequired},
		Message: "this action's tool checks KIFF's execution permit, so it is decided only with a permit request (the call's arguments); call it through the KIFF gateway"}, true
}

// coveringKey carries, through one decide request, what the Card check
// found for an allowed call: the Cards that covered it, so the permit can
// name its Card, and whether this request itself consumed the authority.
type coveringKey struct{}

type covering struct {
	ids      []string
	consumed bool
	// approval is the held call whose owner's approval this request
	// redeemed; "" when the Cards' room allowed it.
	approval string
}

func withCoveringSlot(ctx context.Context) context.Context {
	return context.WithValue(ctx, coveringKey{}, &covering{})
}

func recordCovering(ctx context.Context, ids []string, consumed bool, approval string) {
	if slot, ok := ctx.Value(coveringKey{}).(*covering); ok {
		slot.ids, slot.consumed, slot.approval = ids, consumed, approval
	}
}

func coveringOf(ctx context.Context) covering {
	if slot, ok := ctx.Value(coveringKey{}).(*covering); ok {
		return *slot
	}
	return covering{}
}

// permitGate establishes that the permit will sign the call the Card
// evaluates, and binds the operation to it:
//
//   - the tool and audience are the action's target, which the account's
//     owner or an admin configured; a request naming others is refused, and
//     an action with no target gets no permit;
//   - the parameters the Card reads must be exactly the target's governed
//     argument as it appears in the signed arguments, so a caller cannot
//     have the Card judge one amount and the permit carry another;
//   - the binding is stored at the operation's first decision, whatever its
//     outcome, and a later request for the same id naming a different call
//     is refused.
//
// stop is true with the response to send when the request must not be
// evaluated.
func (h *ProposalsHandler) permitGate(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal,
	spec domain.Spec, idempotent bool, req permit.Request) (b permit.Binding, resp proposalResponse, stop bool) {
	refuse := func(oc outcome, reason, msg string) (permit.Binding, proposalResponse, bool) {
		h.recordRejection(ctx, id, prop, oc, []string{reason})
		return permit.Binding{}, proposalResponse{ProposalID: prop.ID, Outcome: string(oc), Reasons: []string{reason}, Message: msg}, true
	}
	unavailable := func(err error) (permit.Binding, proposalResponse, bool) {
		h.logger.ErrorContext(ctx, "proposals: permit binding unavailable; refusing",
			slog.String("tenant", id.Tenant), slog.String("proposal_id", prop.ID), slog.String("err", err.Error()))
		return refuse(outcomeBlocked, reasonPermitsUnavailable,
			"KIFF could not check or record what this operation is bound to, so it was not authorized; retry later")
	}
	if h.permits == nil {
		return refuse(outcomeBlocked, reasonPermitsUnavailable,
			"this KIFF deployment does not issue execution permits, so a call that needs one cannot be authorized")
	}
	if !idempotent {
		return refuse(outcomeInvalid, reasonPermitNeedsID,
			"a permit is issued for one operation: send the proposal id that names it")
	}
	// The target is the one in the domain whose Cards authorize this
	// decision (the same domain the Card check uses), so a same-named
	// action in another domain never gets this tool's permit.
	target, ok, err := h.permits.Targets.Get(ctx, id.Tenant, cardDomainFor(ctx, spec), prop.ActionName)
	if err != nil {
		h.logger.ErrorContext(ctx, "proposals: permit target unavailable; refusing",
			slog.String("tenant", id.Tenant), slog.String("proposal_id", prop.ID), slog.String("err", err.Error()))
		return refuse(outcomeBlocked, reasonPermitsUnavailable,
			"KIFF could not read where this action's permit goes, so it was not authorized; retry later")
	}
	if !ok {
		return refuse(outcomeInvalid, reasonPermitNoTarget,
			"this action is not connected in verify or relay mode in this domain, so KIFF issues no permit for it")
	}
	want, err := req.BindingFor(target)
	if errors.Is(err, permit.ErrSubstituted) {
		return refuse(outcomeInvalid, reasonPermitSubstituted,
			"the permit request names another tool or audience than the one this action is connected to")
	}
	if err != nil {
		return refuse(outcomeInvalid, reasonPermitInvalid, "the permit request needs the call's arguments as a JSON object")
	}
	// The gateway's read of the call's unit sits beside the governed
	// argument and isn't one the permit signs (kiff/tafs#138): checked
	// without it, as evaluate decides without it.
	_, governed := takeObservedUnit(spec, prop)
	if !req.SameGoverned(target, governed.Parameters) {
		return refuse(outcomeInvalid, reasonPermitParameters,
			"the parameters to decide on must be exactly the governed argument in the arguments the permit signs")
	}
	// The binding is checked before anything is written, under the
	// operation's lock (decide): an operation the Card already decided
	// without a binding (held while its tool was sealed, say) is never
	// bound now, so no request, concurrent or retried, can find a
	// binding made after the owner approved.
	stored, found, err := h.permits.Store.Get(ctx, id.Tenant, prop.ID)
	if err != nil {
		return unavailable(err)
	}
	if !found {
		if dc, ok := h.mandates.(decidedChecker); ok {
			decided, err := dc.Decided(ctx, id.Tenant, prop.ID)
			if err != nil {
				return unavailable(err)
			}
			if decided {
				return refuse(outcomeInvalid, reasonPermitBindingLate,
					"this operation was decided before its tool required an execution permit, so it cannot get one now; make a new call")
			}
		}
		if stored, _, err = h.permits.Bind(ctx, id.Tenant, prop.ID, want); err != nil {
			return unavailable(err)
		}
	}
	if !stored.Same(want) {
		return refuse(outcomeInvalid, "idempotency_key_reuse",
			"this proposal id was already used for a different tool call (tool, arguments or audience differ); use a new id")
	}
	if stored.JTI != "" {
		// One permit per operation: the decision stands, but
		// nothing new is authorized and nothing is drawn.
		return stored, proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeAllowed), Deduplicated: true,
			Reasons: []string{reasonPermitAlreadyIssued}, PermitIssuedAt: timePtr(stored.MintedAt),
			Message: "this operation already received its execution permit; KIFF issues one per operation. Use a new proposal id for a new call"}, true
	}
	return stored, proposalResponse{}, false
}

// mintPermit attaches the operation's one permit to an allowed decision,
// only when this request itself consumed the authority: a fresh draw on
// the Card, or the redeeming of the owner's approval. An answer that
// repeats an earlier authorization (an approval already redeemed) gets no
// permit, so an old approval can never authorize a new call.
// A permit that cannot be minted leaves the decision allowed but
// without one, so the tool refuses to run it: it fails closed.
func (h *ProposalsHandler) mintPermit(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, b permit.Binding, resp *proposalResponse) {
	if outcome(resp.Outcome) != outcomeAllowed {
		return
	}
	cov := coveringOf(ctx)
	if !cov.consumed {
		resp.Reasons = append(resp.Reasons, reasonPermitNotConsumed)
		return
	}
	card := ""
	if len(cov.ids) > 0 {
		card = cov.ids[0]
	}
	tok, minted, err := h.permits.Mint(ctx, id.Tenant, prop.ActorID, prop.ID, card, runGrantSubjectOf(ctx), agentRunOf(ctx), cov.approval, b)
	switch {
	case err != nil:
		h.logger.ErrorContext(ctx, "proposals: permit not minted",
			slog.String("tenant", id.Tenant), slog.String("proposal_id", prop.ID), slog.String("err", err.Error()))
		resp.Reasons = append(resp.Reasons, reasonPermitNotIssued)
	case !minted:
		resp.Reasons = append(resp.Reasons, reasonPermitAlreadyIssued)
		if cur, _, err := h.permits.Bind(ctx, id.Tenant, prop.ID, b); err == nil {
			resp.PermitIssuedAt = timePtr(cur.MintedAt)
		}
	default:
		resp.Permit = tok
	}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

type agentRunKey struct{}

// withAgentRun marks ctx as deciding a call made by run, as the call's
// KIFF-Run header said.
func withAgentRun(ctx context.Context, run string) context.Context {
	return context.WithValue(ctx, agentRunKey{}, run)
}

// agentRunOf is the run ctx decides a call of, or "".
func agentRunOf(ctx context.Context) string {
	s, _ := ctx.Value(agentRunKey{}).(string)
	return s
}

// validAgentRun reports whether run has the form a run id takes: 1 to 128
// letters, digits and . _ : -. Anything else is refused, never signed.
func validAgentRun(run string) bool {
	if len(run) == 0 || len(run) > 128 {
		return false
	}
	for _, c := range run {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}
