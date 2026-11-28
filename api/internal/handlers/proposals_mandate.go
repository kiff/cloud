package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
)

// reasonNoMandateIssued is the issued-authority design refusal: the action declares it
// requires issued authority and no mandate covers this subject. Kept
// distinct from every mandate_* reason because it names a missing step
// rather than an exhausted one.
const reasonNoMandateIssued = "no_mandate_issued"

// Card-holder identity refusals. Distinct reasons so an integrator can
// tell "this key speaks for another agent" from "this key speaks for
// nobody" without reading the message.
const (
	// reasonActorNotKeyHolder: the key is bound to one agent and the
	// proposal names another.
	reasonActorNotKeyHolder = "actor_not_key_holder"
	// reasonKeyNotBoundToAgent: the action requires a card and the key
	// is not bound to any agent, so nothing ties the actor_id it sent
	// to the card's holder.
	reasonKeyNotBoundToAgent = "key_not_bound_to_agent"
)

// MandateAuthorizer is the cumulative-authority check. Nil
// disables it, and a deployment with no mandates behaves exactly as
// before.
type MandateAuthorizer interface {
	Authorize(ctx context.Context, tenantID, proposalID string, req mandate.Request) (mandate.Decision, error)
	// Covers reports, without drawing, whether a card covers the
	// request. The decide path uses it to keep unbound keys off cards.
	Covers(ctx context.Context, tenantID string, req mandate.Request) (bool, error)
}

// WithProposalsMandates installs the mandate gate.
func WithProposalsMandates(a MandateAuthorizer) ProposalsOption {
	return func(h *ProposalsHandler) {
		if a != nil {
			h.mandates = a
		}
	}
}

// mandateRefusal runs the cumulative-authority check for a proposal the
// contract has already accepted, and reports the refusal to send when
// the action would exceed the authority the subject has left.
//
// It is called at the two sites that record an allowed outcome, before
// the record is written, so the decision and the audit trail never
// disagree about what happened. Recording allowed and then correcting
// it afterwards would leave two records for one decision, which is the
// evidence failure this system exists to refuse.
//
// It runs last, after state, permissions, parameters and approval,
// because it is the only check whose answer depends on other requests —
// and because a proposal the contract already refuses must not consume
// authority.
//
// Deliberately independent of tenant mode. In observe the decision is
// advisory and the caller continues its existing flow, so the
// authorization is real whether or not the caller acts on it — and
// counting it is what lets a tenant see what their agent *would* have
// drawn before enforcing anything. We never learn what executed, so
// the ledger records authority, never spend. applyMode softens the
// outcome afterwards like any other, leaving it truthful either way.
func (h *ProposalsHandler) mandateRefusal(
	ctx context.Context,
	id auth.Identity,
	prop kiffproposal.ActionProposal,
	spec domain.Spec,
	currentState string,
) (proposalResponse, bool) {
	if h.mandates == nil {
		return proposalResponse{}, false
	}
	requiresAuthority := actionRequiresAuthority(spec, prop.ActionName)

	subject := prop.ActorID
	if subject == "" {
		// A proposal with no actor cannot be bound to a mandate,
		// because a mandate names a subject.
		//
		// When the action requires issued authority, that is fatal
		// rather than a no-op: there is no holder, so no card can
		// cover it, and allowing would be the exact hole the issued-authority design
		// closes.
		if requiresAuthority {
			return h.noAuthorityRefusal(ctx, id, prop,
				"this action requires issued authority and the proposal names no actor to hold it"), true
		}
		// The contract already decided whether it may act; there is
		// nothing to draw against.
		return proposalResponse{}, false
	}

	req := h.mandateRequest(ctx, id, prop, spec, currentState)
	d, err := h.mandates.Authorize(ctx, id.Tenant, prop.ID, req)
	if err != nil {
		return h.mandateUnavailable(ctx, id, prop, err), true
	}
	// the issued-authority design. `not_covered` means no mandate names this (subject,
	// action) at all, and Decision.Allowed() reports it as allowed
	// because the mandates design made absence a no-op. For an action that declares
	// it requires issued authority, absence is the refusal — an
	// instrument you do not need in order to act is not an instrument.
	//
	// Its own reason, never a generic block. The first integrator to
	// meet this will have wired everything correctly; `blocked` would
	// send them to audit their domain, permissions and state machine
	// before discovering that nothing has been issued yet.
	if d.Outcome == mandate.OutcomeNotCovered && requiresAuthority {
		return h.noAuthorityRefusal(ctx, id, prop,
			"no authority has been issued to "+subject+" for "+prop.ActionName+
				"; issue an agent card for this action before it can run"), true
	}
	if d.Outcome == mandate.OutcomeUncountableDraw || d.Outcome == mandate.OutcomeUnreadableResource {
		reasons := []string{"invalid_parameter"}
		h.recordOutcome(ctx, id, prop, outcomeInvalid, reasons)
		msg := "a parameter summed by the agent card cannot be counted as an integer"
		if d.Outcome == mandate.OutcomeUnreadableResource {
			msg = "a parameter the agent card's resources are matched on is longer than " +
				strconv.Itoa(mandate.MaxResourceLength) + " characters or was sent more than once; it cannot be matched exactly: " + d.Reason
		}
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeInvalid),
			Reasons:    reasons,
			Message:    msg,
		}, true
	}
	if d.Allowed() {
		// An allowed decision with a held call is the owner's approval,
		// redeemed: the permit names it.
		recordCovering(ctx, d.Covering, d.Consumed, d.ExceptionID)
		return proposalResponse{}, false
	}
	if d.Outcome == mandate.OutcomeApprovedNoRoom {
		reasons := []string{"mandate_approved_no_room"}
		h.recordOutcome(ctx, id, prop, outcomeApprovedNoRoom, reasons)
		return proposalResponse{
			ProposalID: prop.ID, Outcome: string(outcomeApprovedNoRoom), Reasons: reasons,
			Message: d.Reason + "; nothing was authorized or sent; retry this same operation only after room returns",
			Exception: &exceptionHandle{ID: d.ExceptionID, Status: "approved_no_room",
				ReviewURL: "/needs-you/" + url.PathEscape(d.ExceptionID), ExpiresAt: d.ExceptionExpiresAt},
		}, true
	}

	// Outside a Card set to come home: held for its owner (the mandates design
	// amendment). Nothing is authorized; the caller re-decides the same
	// proposal id, and sees the owner's answer then.
	if d.Outcome == mandate.OutcomeOwnerRequired {
		reasons := []string{"mandate_owner_required"}
		h.recordOutcome(ctx, id, prop, outcomeOwnerRequired, reasons)
		h.notifyHeld(id.Tenant, prop, d)
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeOwnerRequired),
			Reasons:    reasons,
			Message:    "this action is outside the agent's card, so it went to the card's owner. Nothing was authorized; re-decide the same proposal id to see their answer: " + d.Reason,
			// The dashboard's Needs you page, where the owner answers.
			Exception: &exceptionHandle{ID: d.ExceptionID, Status: string(mandate.ExceptionHeld),
				ReviewURL: "/needs-you/" + url.PathEscape(d.ExceptionID), ExpiresAt: d.ExceptionExpiresAt,
				Why: h.heldWhy(ctx, id.Tenant, spec, d.Breaches)},
		}, true
	}
	// Nobody answered before the hold expired: refused. The
	// same proposal id stays refused; a new attempt is a new proposal.
	if d.Outcome == mandate.OutcomeOwnerExpired {
		reasons := []string{"mandate_owner_expired"}
		h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeBlocked),
			Reasons:    reasons,
			Message:    "nobody answered this action before its hold expired; nothing was authorized. Propose it again with a new proposal id to ask the card's owner again",
			Exception:  &exceptionHandle{ID: d.ExceptionID, Status: string(mandate.ExceptionExpired), ExpiresAt: d.ExceptionExpiresAt},
		}, true
	}
	// Its agent withdrew the held call: refused, and for good.
	if d.Outcome == mandate.OutcomeWithdrawn {
		reasons := []string{"mandate_withdrawn"}
		h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeBlocked),
			Reasons:    reasons,
			Message:    "this action's agent withdrew it when its run was cancelled; nothing was authorized",
			Exception:  &exceptionHandle{ID: d.ExceptionID, Status: string(mandate.ExceptionInvalidated)},
		}, true
	}
	if d.Outcome == mandate.OutcomeOwnerRejected {
		reasons := []string{"mandate_owner_rejected"}
		h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
		msg := "the card's owner rejected this action; nothing was authorized"
		ex := &exceptionHandle{ID: d.ExceptionID, Status: string(mandate.ExceptionRejected)}
		if r := d.Refusal; r != nil {
			// What to do instead, in the text for an agent that
			// only reads the message, and in fields for one that branches.
			ex.NextStep, ex.Note = r.NextStep, r.Note
			if r.NextStep != "" {
				msg += "; the owner chose the next step " + r.NextStep
			}
			if r.Note != "" {
				msg += "; the owner's note: " + strconv.Quote(r.Note)
			}
		}
		return proposalResponse{
			ProposalID: prop.ID,
			Outcome:    string(outcomeBlocked),
			Reasons:    reasons,
			Message:    msg,
			Exception:  ex,
		}, true
	}

	// A Card set to refuse, or a request no owner could answer, is
	// refused. More room is obtained by raising the limit deliberately,
	// which leaves a record of having done so (the mandates design, open question
	// 3, still governing Cards set to refuse).
	reasons := []string{"mandate_" + string(d.Outcome)}
	h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
	return proposalResponse{
		ProposalID: prop.ID,
		Outcome:    string(outcomeBlocked),
		Reasons:    reasons,
		Message:    mandateMessage(d),
	}, true
}

// mandateRequest is what a proposal asks of its actor's Cards. A trial
// (gateway_trial.go) builds it here too, so it is decided on exactly what
// a real call would be.
func (h *ProposalsHandler) mandateRequest(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, spec domain.Spec, currentState string) mandate.Request {
	cardDomain := cardDomainFor(ctx, spec)
	numeric, uncountable := numericParameters(prop.Parameters)
	resources, unreadable := stringParameters(prop.Parameters)
	subject, trial, words := prop.ActorID, trialDraftFrom(ctx), tafWordsFrom(ctx)
	if trial != "" {
		// A trial's call is decided on its trial Cards alone (kiff/tafs#173).
		subject = mandate.TrialSubject(subject)
	}
	req := mandate.Request{
		OwnerAsk:    ownerAskFrom(ctx),
		OwnerAskWhy: ownerAskWhyFrom(ctx),
		Subject:     subject,
		TrialDraft:  trial,
		AskedBy:     runGrantSubjectOf(ctx),
		Domain:      cardDomain,
		DomainID:    decidingDomainFrom(ctx),
		Environment: h.cardEnvironmentFor(ctx, id.Tenant, prop.ActionName, cardDomain),
		Action:      prop.ActionName,
		EntityState: currentState,
		EntityID:    prop.EntityID,
		Parameters:  numeric,
		Uncountable: uncountable,
		Resources:   resources,
		Unreadable:  unreadable,
		Declares:    declaredStrings(spec, prop.ActionName),
		Observe:     observing(ctx),
		TafAbout:    words.about,
		TafReason:   words.reason,
	}
	h.classInto(ctx, id.Tenant, &req)
	return req
}

// declaredStrings are the string parameters the action's contract
// declares, keyed by lowercase name: a per-item total counts only the
// actions that declare its parameter, and holds their calls that name none.
func declaredStrings(spec domain.Spec, action string) map[string]bool {
	var out map[string]bool
	for _, a := range spec.Actions {
		if !strings.EqualFold(a.Name, action) {
			continue
		}
		for _, p := range a.Parameters {
			if t := strings.ToLower(strings.TrimSpace(p.Type)); t == "string" || t == "enum" {
				if out == nil {
					out = map[string]bool{}
				}
				out[strings.ToLower(p.Name)] = true
			}
		}
	}
	return out
}

// mandateUnavailable is the refusal when the card check cannot be
// answered. Fail closed. Unlike the billing limiter, which allows when
// uncertain because blocking production traffic over our meter is the
// worse failure, a mandate is the authority itself — allowing here
// means acting with authority nobody could confirm.
func (h *ProposalsHandler) mandateUnavailable(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, err error) proposalResponse {
	h.logger.ErrorContext(ctx, "proposals: mandate check unavailable; refusing",
		slog.String("tenant", id.Tenant),
		slog.String("proposal", prop.ID),
		slog.String("err", err.Error()),
	)
	reasons := []string{"mandate_unavailable"}
	h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
	return proposalResponse{
		ProposalID: prop.ID,
		Outcome:    string(outcomeBlocked),
		Reasons:    reasons,
		Message:    "the authority limit for this agent could not be established, so the action was refused rather than allowed on an unknown balance",
	}
}

func mandateMessage(d mandate.Decision) string {
	switch d.Outcome {
	case mandate.OutcomeExpired:
		return "the mandate authorizing this agent is revoked or outside its validity window; issue or renew it before this action can run"
	case mandate.OutcomeProposalReused:
		// The id is spent, not the card: say what to do instead.
		return "this proposal id already drew on an agent card and cannot authorize again: " + d.Reason
	case mandate.OutcomeLimitReached:
		// Not "no authority left": the card is refused when this action
		// would exceed what remains, which is not the same as the card
		// being empty. A €2,500 refund against €2,000 of remaining
		// authority is refused while €2,000 is still there for
		// something smaller, and a message claiming otherwise
		// contradicts the statement the reader is looking at.
		return fmt.Sprintf("this action exceeds the remaining authority under mandate %s: %s", d.MandateID, d.Reason)
	case mandate.OutcomePerActionExceeded:
		// The Card may have plenty left: this one action is too large.
		return fmt.Sprintf("this action is larger than a single action may be under mandate %s: %s", d.MandateID, d.Reason)
	case mandate.OutcomeResourceOutside:
		// The Card allows the action, on other things than this one.
		return fmt.Sprintf("this action is on a resource mandate %s does not name: %s", d.MandateID, d.Reason)
	default:
		return "this action is outside the authority granted to this agent"
	}
}

// stringParameters projects the proposal's string parameters onto what a
// Card's resource scope can name, keyed by lowercase name.
//
// A value no scope can list exactly goes to unreadable instead, as a
// digest of what was sent: one longer than MaxResourceLength, or a name
// sent in two cases (one of them a string). A scope naming it refuses the
// call, and the digest keeps two such calls distinct in the fingerprint
// an approval binds to.
func stringParameters(params map[string]any) (resources, unreadable map[string]string) {
	byName := map[string][]string{}
	for k, v := range params {
		key := strings.ToLower(k)
		byName[key] = append(byName[key], k+"\x00"+fmt.Sprint(v))
		if s, ok := v.(string); ok {
			if resources == nil {
				resources = map[string]string{}
			}
			resources[key] = s
		}
	}
	for key, sent := range byName {
		s, isString := resources[key]
		if !isString || len(sent) == 1 && len(s) <= mandate.MaxResourceLength {
			continue
		}
		delete(resources, key)
		slices.Sort(sent)
		sum := sha256.Sum256([]byte(strings.Join(sent, "\x00")))
		if unreadable == nil {
			unreadable = map[string]string{}
		}
		unreadable[key] = "sha256:" + hex.EncodeToString(sum[:])
	}
	if len(resources) == 0 {
		resources = nil
	}
	return resources, unreadable
}

// numericParameters projects the proposal's parameters onto the int64s
// a mandate can sum, and marks present values it cannot count exactly.
//
// The card engine checks Uncountable only for a sum aggregate that
// covers this action. Unrelated strings and actions with no covering
// sum card retain their prior behavior. Keys are folded because card
// validation matches parameter names without regard to case.
func numericParameters(params map[string]any) (map[string]int64, map[string]bool) {
	if len(params) == 0 {
		return nil, nil
	}
	out := make(map[string]int64, len(params))
	uncountable := make(map[string]bool)
	seen := make(map[string]bool, len(params))
	for k, v := range params {
		k = strings.ToLower(k)
		if seen[k] {
			delete(out, k)
			uncountable[k] = true
			continue
		}
		seen[k] = true
		switch n := v.(type) {
		case int:
			out[k] = int64(n)
		case int64:
			out[k] = n
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n ||
				n < float64(math.MinInt64) || n >= float64(math.MaxInt64) {
				uncountable[k] = true
				continue
			}
			out[k] = int64(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				out[k] = i
			} else {
				uncountable[k] = true
			}
		default:
			uncountable[k] = true
		}
	}
	return out, uncountable
}

// noAuthorityRefusal renders the issued-authority design refusal: the action declares it
// requires issued authority and none covers this subject.
//
// Separate from the ceiling refusals because it is a different kind of
// answer. `mandate_limit_reached` says the agent spent what it had;
// this says nobody ever gave it any. Collapsing them would tell an
// operator to raise a limit that does not exist.
func (h *ProposalsHandler) noAuthorityRefusal(
	ctx context.Context,
	id auth.Identity,
	prop kiffproposal.ActionProposal,
	message string,
) proposalResponse {
	reasons := []string{reasonNoMandateIssued}
	h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
	return proposalResponse{
		ProposalID: prop.ID,
		Outcome:    string(outcomeBlocked),
		Reasons:    reasons,
		Message:    message,
	}
}

// holderRefusal enforces card-holder identity for runtime keys.
//
// A card names its holder, and the card check draws on the card of
// whichever actor_id the proposal carries. Without this, any key with
// the right role could name another agent and spend its card. So:
//
//   - A key bound to an agent may only propose as that agent, on every
//     action. A binding that held only on some actions would not be a
//     binding.
//   - On an action that requires a card, or that the named agent holds
//     a card for, an API key must be bound, and bound to the actor it
//     names. Unbound keys keep working for actions no card of the named
//     agent covers, so no card-free integration breaks.
//
// Only API keys are held to this. A signed-in owner (session or
// developer token) acting from the dashboard — the card page's test
// request — is an operator path: the owner issued the card and may
// exercise it deliberately. A missing actor_id never reaches here; the
// shape check refuses it as invalid first.
//
// decide runs this before the idempotency cache, so a replayed proposal
// id is held to the same rule as a fresh one, and records the refusal
// as a rejection so it never becomes the cached answer for that id.
//
// The binding is an opaque agent id on the key, independent of the
// customer's identity provider; later workload credentials can resolve
// to the same id.
func (h *ProposalsHandler) holderRefusal(
	ctx context.Context,
	id auth.Identity,
	prop kiffproposal.ActionProposal,
	spec domain.Spec,
) (proposalResponse, bool) {
	if !id.APIKey {
		return proposalResponse{}, false
	}
	if id.AgentID != "" {
		if prop.ActorID == id.AgentID {
			return proposalResponse{}, false
		}
		return holderRefusalResponse(prop, reasonActorNotKeyHolder,
			"this key is bound to agent "+id.AgentID+" and cannot act as "+prop.ActorID+
				"; use the key issued for "+prop.ActorID), true
	}
	if actionRequiresAuthority(spec, prop.ActionName) {
		return holderRefusalResponse(prop, reasonKeyNotBoundToAgent,
			prop.ActionName+" requires a card, and this key is not bound to an agent, so it cannot act as "+
				prop.ActorID+"; create a key for the agent from its card"), true
	}
	// A card may also cover an action that does not require one, and
	// the card check draws on it. So an unbound key may not act as an
	// agent holding a card for this action at all — asked here, before
	// the idempotency cache, so a replay of the holder's allowed id is
	// refused as a fresh request would be. State is not known yet, so
	// any card granting the action counts. Actions no card of this agent
	// covers stay open to unbound keys.
	if h.mandates == nil {
		return proposalResponse{}, false
	}
	cardDomain := cardDomainFor(ctx, spec)
	covered, err := h.mandates.Covers(ctx, id.Tenant, mandate.Request{
		Subject:     prop.ActorID,
		Domain:      cardDomain,
		DomainID:    decidingDomainFrom(ctx),
		Environment: h.cardEnvironmentFor(ctx, id.Tenant, prop.ActionName, cardDomain),
		Action:      prop.ActionName,
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "proposals: card coverage unavailable; refusing",
			slog.String("tenant", id.Tenant),
			slog.String("proposal", prop.ID),
			slog.String("err", err.Error()),
		)
		return holderRefusalResponse(prop, "mandate_unavailable",
			"whether a card covers this action for "+prop.ActorID+" could not be established, so the action was refused"), true
	}
	if !covered {
		return proposalResponse{}, false
	}
	return holderRefusalResponse(prop, reasonKeyNotBoundToAgent,
		prop.ActorID+" holds a card for "+prop.ActionName+", and this key is not bound to an agent, so it cannot act as "+
			prop.ActorID+"; create a key for the agent from its card"), true
}

// holderRefusalResponse builds the refusal. The caller records it, as a
// rejection rather than a decision (see decide).
func holderRefusalResponse(prop kiffproposal.ActionProposal, reason, message string) proposalResponse {
	return proposalResponse{
		ProposalID: prop.ID,
		Outcome:    string(outcomeBlocked),
		Reasons:    []string{reason},
		Message:    message,
	}
}

// cardDomainFor is the domain a card must be scoped to for it to apply
// to this request: the domain whose runtime is answering it. That is
// the resolved selector (an explicit request domain, or the domain a
// key is bound to) when there is one, otherwise the tenant's default
// domain, identified from the spec resolved beside that runtime the
// same way its storage id was derived. Never empty, so a domain-scoped
// card is never matched by default.
func cardDomainFor(ctx context.Context, spec domain.Spec) string {
	if d := runtimeDomainFrom(ctx); d != "" {
		return d
	}
	if d := domain.Slugify(spec.Domain); d != "" {
		return d
	}
	return domain.DefaultDomainID
}

// cardEnvironmentFor is the environment a card must be scoped to for it
// to apply to this request. The request's own environment when
// it names one; otherwise the environment of the active control that
// governs this action in the card's domain. Empty when that is unknown
// or ambiguous (several environments, or the controls cannot be read):
// an environment-scoped card then does not apply, the same way
// treats an unknown domain.
func (h *ProposalsHandler) cardEnvironmentFor(ctx context.Context, tenantID, action, domainID string) string {
	if env := controlEnvironmentFrom(ctx); env != "" {
		return env
	}
	if h.controls == nil {
		return ""
	}
	list, err := domain.ActiveControls(ctx, h.controls, tenantID)
	if err != nil {
		return ""
	}
	env, found := "", false
	for _, c := range list {
		if !slices.Contains(c.Actions, action) {
			continue
		}
		if d := domain.Slugify(c.Domain); d != "" && d != domainID {
			continue
		}
		if found && c.Environment != env {
			return ""
		}
		env, found = c.Environment, true
	}
	return env
}

// actionRequiresAuthority reports whether the action declares that an
// issued mandate is a precondition.
//
// The spec is passed down from the caller rather than looked up here,
// for the reason runContextRuleFor states: it was resolved beside the
// runtime that answered this proposal, so there is no "could not tell"
// state for a declaring action to slip through.
func actionRequiresAuthority(spec domain.Spec, action string) bool {
	for _, a := range spec.Actions {
		if a.Name == action {
			return a.RequiresAuthority
		}
	}
	return false
}

// cardLister is the mandate gate's view of the account's Cards, when it
// has one: heldWhy reads their units from it.
type cardLister interface {
	Cards(ctx context.Context, tenantID string) ([]mandate.Mandate, error)
}

// heldWhy is each limit a held call went over, as the owner reads it on
// the hold (annotate): an amount in its Card's unit when the Card's
// actions in spec agree on one, else KIFF's own reason. English: the
// agent reads it, and says it in its user's words.
func (h *ProposalsHandler) heldWhy(ctx context.Context, tenantID string, spec domain.Spec, breaches []mandate.Breach) []string {
	var cards map[string]mandate.Mandate
	if l, ok := h.mandates.(cardLister); ok {
		if ms, err := l.Cards(ctx, tenantID); err == nil {
			cards = make(map[string]mandate.Mandate, len(ms))
			for _, m := range ms {
				cards[m.ID] = m
			}
		}
	}
	out := make([]string, 0, len(breaches))
	for _, b := range breaches {
		why := b.Reason
		if m, found := cards[b.MandateID]; found && b.Quantity.Kind == mandate.KindSum {
			if p, has := unitKindsOf(spec.Actions, m).units[strings.ToLower(b.Quantity.Parameter)]; has {
				if r := reasonInUnit(breachView(b), p.Unit, p.Scale, langEN); r != "" {
					why = r
				}
			}
		}
		if why != "" {
			out = append(out, why)
		}
	}
	return out
}
