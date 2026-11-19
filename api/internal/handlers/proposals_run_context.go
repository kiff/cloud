package handlers

import (
	"context"
	"log/slog"

	kiffaction "github.com/kiff/kiff/pkg/kiff/action"
	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/runcontext"
)

// Reason codes the run-context gate emits. They are deliberately
// distinct: one says the control worked as designed, the other says the
// integrator is not asserting run context at all. Collapsing them would
// hide a wiring problem inside a governance outcome.
const (
	// reasonUntrustedRun: the harness reported that untrusted content
	// entered this run, and the action declares it cares.
	reasonUntrustedRun = "untrusted_run_context"
	// reasonRunContextAbsent: the action declares a run-context
	// dependency and no run context was asserted at all.
	reasonRunContextAbsent = "run_context_absent"
	// reasonUntrustedSensitiveRead: the run reported untrusted input
	// AND a sensitive read — the exfiltration path formed, not just
	// its first link.
	reasonUntrustedSensitiveRead = "untrusted_sensitive_read"
	// reasonSensitiveReadUnasserted: the action gates on the
	// conjunction and the harness said nothing about sensitive reads.
	// Distinct from reasonRunContextAbsent because the wiring problem
	// is different: this integrator asserts run context but predates
	// (or omits) the second fact, and silence must not read as "no
	// sensitive read happened".
	reasonSensitiveReadUnasserted = "sensitive_read_unasserted"
)

// runContextRefusal applies an action's declared run-context dependency
// to a proposal the contract has *already accepted*, and
// reports the stricter outcome to send when the dependency bites.
//
// It runs at the one site that records an allowed outcome, before the
// record is written, so the response and the audit trail never disagree
// about what happened — the same discipline mandateRefusal follows, and
// for the same reason: correcting an allowed record afterwards would
// leave two records for one decision.
//
// It runs *before* the mandate check, because a proposal that is going
// to be held for a human must not consume cumulative authority on its
// way there.
//
// Direction of travel: this can only make an outcome stricter. It is
// consulted solely on the path that would otherwise return `allowed`,
// so it can hold or refuse an action, and can never turn a blocked or
// invalid proposal into a permitted one. Mandates state the same
// property as "constrains, does not confer".
//
// # Absence is not a clean run
//
// A declared dependency with no assertion produces the same stricter
// outcome as a tainted run, tagged with a different reason. An action
// that declares this dependency is saying its answer depends on a fact
// about the run; a caller who supplies no such fact has not established
// a clean run, they have established nothing.
//
// the mandates design shipped the inverse of this and it is worth restating rather
// than assuming it was learned: a revoked mandate produced an empty
// set, an empty set read as "no mandate covers this", and by the
// constrains-not-confers rule that meant allowed — so revoking a limit
// made the agent less bounded. Here, "no run context present" must
// never resolve to "clean run".
func (h *ProposalsHandler) runContextRefusal(
	ctx context.Context,
	id auth.Identity,
	prop kiffproposal.ActionProposal,
	spec domain.Spec,
	callerApprovalID, currentState string,
	idempotent bool,
	proposalActor kiffactor.Actor,
	contract kiffaction.ActionContract,
	rt *kiffruntime.Runtime,
) (proposalResponse, bool) {
	rule, ok := h.runContextRuleFor(ctx, id.Tenant, spec, prop.ActionName)
	if !ok {
		return proposalResponse{}, false // the action declares no dependency
	}

	rc, asserted := runcontext.FromContext(ctx)
	disposition, reason, fires := evaluateRunContextRule(rule, rc, asserted)
	if !fires {
		return proposalResponse{}, false
	}

	// A refusal is terminal, so it is rendered here. A hold is not: it
	// has to be something a human can actually grant, which means the
	// same round-trip the contract-driven approval path builds —
	// a pending approval record, an actionable handle, and an approval
	// id bound to this request's fingerprint so a grant can never
	// authorize a different payload. Returning approval_required
	// without that would be a dead end wearing a hold's label: the
	// caller is told to get approval and has nothing to get.
	if disposition == domain.RunContextApproval {
		// The framework refuses to create an approval for a contract whose
		// ApprovalRequirement is "never", which is exactly the contract an
		// action carries when the hold comes from the run-context rule
		// rather than the static risk tier. Left alone, RequestApproval
		// fails, the error is logged as a warning, and the caller receives
		// a handle pointing at a record that does not exist — a hold that
		// looks grantable and is not.
		//
		// So the gate states what it has just determined: for THIS
		// proposal, approval is required. The static "never" describes the
		// ordinary case; the run-context rule is what makes this instance
		// different, and the contract handed to the approval path has to
		// say so or the framework's invariant is correct to reject it.
		held := contract
		held.ApprovalRequirement = kiffaction.ApprovalRequired
		return h.resolveApproval(ctx, id, prop, spec, callerApprovalID, currentState,
			idempotent, proposalActor, held, rt, reason), true
	}

	reasons := []string{reason}
	h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
	return proposalResponse{
		ProposalID: prop.ID,
		Outcome:    string(outcomeBlocked),
		Reasons:    reasons,
		Message:    runContextMessage(reason),
	}, true
}

// runContextMessage explains a refusal in the terms the integrator
// needs. Two of these say the control worked as designed and two say
// the integrator is not asserting what the action depends on; keeping
// them distinct is what lets a wiring problem be read as a wiring
// problem rather than as a governance outcome.
func runContextMessage(reason string) string {
	switch reason {
	case reasonRunContextAbsent:
		return "the action declares a run-context dependency and no run context was asserted; " +
			"absence is not evidence of a clean run"
	case reasonSensitiveReadUnasserted:
		return "this run reported untrusted input and the action gates on untrusted input " +
			"together with a sensitive read, but the harness asserted nothing about " +
			"sensitive reads; an unasserted fact is not a fact that did not happen"
	case reasonUntrustedSensitiveRead:
		return "this run reported untrusted input and then read sensitive content, " +
			"and the action declares a dependency on that combination"
	}
	return "this run reported untrusted input, and the action declares a run-context dependency"
}

// runContextRuleFor returns the action's declared run-context rule, and
// whether it declared one.
//
// The spec is passed in rather than looked up, because it was resolved
// beside the runtime that answered this proposal — one parse of one
// document, refused as a unit if it could not be read. That leaves this
// function total: there is no "could not tell whether a rule exists"
// state for a declaring action to slip through.
func (h *ProposalsHandler) runContextRuleFor(ctx context.Context, tenantID string, spec domain.Spec, action string) (domain.RunContextSpec, bool) {
	for _, a := range spec.Actions {
		if a.Name != action || a.RunContext == nil {
			continue
		}
		// A disposition outside the known vocabulary cannot reach here:
		// ParseAndBuild refuses it at activation, so an unknown value
		// never becomes an active domain. Guarding anyway, because the
		// alternative to a known disposition is silently no rule.
		//
		// Each condition is optional and validated only when present —
		// an action may declare either or both. An empty string is
		// "this condition is not declared", not a bad disposition, and
		// conflating the two would force every action that gates on the
		// conjunction alone into the unknown-disposition path below.
		for field, d := range map[string]string{
			"on_untrusted_input":          a.RunContext.OnUntrustedInput,
			"on_untrusted_sensitive_read": a.RunContext.OnUntrustedSensitiveRead,
		} {
			if d == "" || domain.ValidRunContextDisposition(d) {
				continue
			}
			h.logger.ErrorContext(ctx, "proposals: active domain carries an unknown run-context disposition",
				slog.String("tenant", tenantID),
				slog.String("action", action),
				slog.String("field", field),
				slog.String("disposition", d),
			)
			// Treat as the strictest interpretation rather than as no
			// rule: the author declared a dependency.
			return domain.RunContextSpec{OnUntrustedInput: domain.RunContextBlock}, true
		}
		// Both empty is refused at activation, so an active domain
		// reaching here has declared at least one condition.
		return *a.RunContext, true
	}
	return domain.RunContextSpec{}, false
}

// evaluateRunContextRule applies an action's declared run-context
// conditions to what the harness asserted, and reports the disposition
// to impose, the reason code to record, and whether any condition
// fired at all.
//
// # Two conditions, one closed vocabulary
//
//	on_untrusted_input          the run consumed untrusted content
//	on_untrusted_sensitive_read that, AND it read something sensitive
//
// The second exists because the first does not discriminate on an
// agent whose work begins with untrusted content. Measured over 198
// trials in a prompt-injection lab: 90 of 90 benign runs
// were tainted, so `on_untrusted_input` held every benign write and
// bought nothing over requiring approval on all of them. The
// conjunction held none of them and still caught every run that
// reached for private data.
//
// # Where the fail-open would have been
//
// The conjunction is false when either fact is false, and a fact the
// harness never mentioned is not false — it is unknown. A guard that
// predates `sensitive_read` asserts only `untrusted_input`, and
// reading its silence as "no sensitive read happened" would allow
// exactly the action the rule exists to hold. So an unasserted
// sensitive_read refuses, under its own reason code, the same way an
// absent run context does. The mandates design shipped the inverse of this once
// already: a revoked mandate produced an empty set, an empty set read
// as "nothing to check", and "nothing to check" meant allowed.
//
// When an action declares both conditions, the stricter disposition
// wins. Declaring both is redundant rather than wrong — the broad
// condition is a superset of the conjunction — and picking the
// stricter one keeps this function unable to soften anything.
func evaluateRunContextRule(
	rule domain.RunContextSpec,
	rc runcontext.RunContext,
	asserted bool,
) (disposition, reason string, fires bool) {
	// No assertion at all, for either condition. Absence is not
	// evidence of a clean run.
	if !asserted {
		return strictest(rule.OnUntrustedInput, rule.OnUntrustedSensitiveRead),
			reasonRunContextAbsent, true
	}

	if rule.OnUntrustedSensitiveRead != "" {
		sensitive, sensitiveAsserted := rc.SensitiveReadAsserted()
		switch {
		case !sensitiveAsserted && rc.UntrustedInput:
			// The tainted half is established and the other half is
			// unknown. Refusing here is the whole point of the
			// pointer on that field.
			return rule.OnUntrustedSensitiveRead, reasonSensitiveReadUnasserted, true
		case rc.UntrustedInput && sensitive:
			return rule.OnUntrustedSensitiveRead, reasonUntrustedSensitiveRead, true
		}
	}

	if rule.OnUntrustedInput != "" && rc.UntrustedInput {
		return rule.OnUntrustedInput, reasonUntrustedRun, true
	}

	return "", "", false
}

// strictest returns the stricter of two dispositions, ignoring empty
// ones. Used where a refusal has to be rendered before it is known
// which condition would have fired — an absent assertion establishes
// nothing, so it cannot establish which half of the rule to apply.
func strictest(a, b string) string {
	if a == domain.RunContextBlock || b == domain.RunContextBlock {
		return domain.RunContextBlock
	}
	if a != "" {
		return a
	}
	return b
}
