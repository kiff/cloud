package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	kiffaction "github.com/kiff/kiff/pkg/kiff/action"
	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tenant"
)

// POST /v1/gateway/trial decides one call a trial of an agent's next
// version makes through the gateway (kiff/tafs#107), and records nothing:
// no proposal, no decision, no draw, no hold. The gateway asks it instead
// of deciding a call made with KIFF-Trial, and sends the call nowhere.
//
// The call is decided as the gateway's real calls are: the account's
// gateway domain, the agent the key is bound to, the ToolCall state its
// TOOL_CALL_REQUESTED event would set, the action contract, then the
// agent's Cards with what they have used so far plus what the trial's
// earlier allowed writes would have drawn (its run's simulated draws,
// mandate.Authorizer.Trial). Only an agent's key asks.

// gatewayTrialState is the state a gateway call is decided in: the one
// TOOL_CALL_REQUESTED sets (gateway.Contract).
const gatewayTrialState = "requested"

// gatewayTrialLive asks for the agent's Cards as they stand.
const gatewayTrialLive = "live"

type gatewayTrialRequest struct {
	ActionName string         `json:"action_name"`
	Parameters map[string]any `json:"parameters"`
	// Draft says whose Cards decide: "live", or a draft's id.
	Draft string `json:"draft"`
	// Run is the trial's run: its earlier allowed writes count toward the
	// Cards' limits for this one, as in a live queue.
	Run string `json:"run"`
}

// gatewayTrialResponse is KIFF's decision on a trial call: allowed,
// held (it would wait for the Card's owner) or refused.
type gatewayTrialResponse struct {
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons,omitempty"`
	Message  string   `json:"message"`
}

func trialRefused(reason, message string) gatewayTrialResponse {
	return gatewayTrialResponse{Decision: "refused", Reasons: []string{reason}, Message: message}
}

// mandateTrier decides against a subject's Cards without recording
// anything (mandate.Authorizer.Trial).
type mandateTrier interface {
	TrialWith(ctx context.Context, tenantID, trialID string, req mandate.Request, cards []mandate.Mandate, retire []string) (mandate.Decision, error)
}

func (h *ProposalsHandler) gatewayTrial(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a trial is asked with an agent's key"})
		return
	}
	if !id.APIKey || id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "a trial is asked with a key bound to the agent it tries"})
		return
	}
	if h.tenantStore != nil {
		if t, err := h.tenantStore.Get(r.Context(), id.Tenant); err == nil && t.IsErasing() {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": ErrTenantErasing.Error()})
			return
		} else if err == nil && t.Status == tenant.StatusSuspended {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": ErrTenantSuspended.Error()})
			return
		}
	}
	var req gatewayTrialRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.ActionName) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a trial call needs action_name, parameters and draft"})
		return
	}
	resp, status := h.decideTrial(r.Context(), id, req)
	writeJSON(w, status, resp)
}

// decideTrial is the trial's decision. A status other than 200 means
// KIFF could not decide; the gateway reports the call as not decided, and
// sends nothing either way.
func (h *ProposalsHandler) decideTrial(ctx context.Context, id auth.Identity, req gatewayTrialRequest) (any, int) {
	draftID := strings.TrimSpace(req.Draft)
	if draftID != gatewayTrialLive && !strings.HasPrefix(draftID, "drf_") {
		return map[string]string{"error": `draft is "live" or a draft id`}, http.StatusBadRequest
	}

	run := strings.TrimSpace(req.Run)
	if run == "" || len(run) > 128 {
		// Without its run, a trial's earlier writes can't be counted, and
		// each write would look as if it were the first.
		return trialRefused("trial_run_missing",
			"this trial call names no trial run, so KIFF can't count its earlier writes. Nothing was sent."), http.StatusOK
	}
	var draftCards []mandate.Mandate
	var retire []string
	if draftID != gatewayTrialLive {
		cards, retired, refusal := h.trialDraftCards(ctx, id, draftID)
		if refusal != nil {
			return *refusal, http.StatusOK
		}
		draftCards, retire = cards, retired
	}

	resolved, err := domain.ResolveRequestDomain(id.DomainID, gatewayDomain)
	if err != nil {
		return map[string]string{"error": "the API key is bound to a different domain than the gateway's"}, http.StatusForbidden
	}
	env, ok := resolveRequestEnvironment(id, "")
	if !ok {
		return map[string]string{"error": "the API key is bound to a different environment"}, http.StatusForbidden
	}
	ctx = withRuntimeDomain(withControlEnvironment(ctx, env), resolved)
	rt, err := h.runtimeFor(ctx, id.Tenant)
	var spec domain.Spec
	var answeredBy string
	if err == nil {
		spec, err = h.tenants.SpecForDomain(ctx, id.Tenant, runtimeDomainFrom(ctx))
	}
	if err == nil {
		answeredBy, err = h.tenants.DomainIDFor(ctx, id.Tenant, runtimeDomainFrom(ctx))
	}
	if err != nil {
		return map[string]string{"error": "the account's gateway domain could not be read"}, http.StatusServiceUnavailable
	}
	if answeredBy != "" {
		ctx = withDecidingDomain(ctx, answeredBy)
	}
	if rt.Actions == nil || rt.Validator == nil {
		return map[string]string{"error": "the account's gateway domain has no actions"}, http.StatusServiceUnavailable
	}
	contract, found := rt.Actions.Get(req.ActionName)
	if !found {
		return trialRefused("unknown_action", "this tool isn't connected to the account. Nothing was sent."), http.StatusOK
	}

	params := req.Parameters
	if params == nil {
		params = map[string]any{}
	}
	prop := kiffproposal.ActionProposal{ID: "trial", EntityID: "trial", EntityType: "ToolCall",
		ActionName: req.ActionName, ActorID: id.AgentID, Parameters: params, CreatedAt: h.now().UTC()}
	// A tool every call of which asks its owner is tried as it's decided.
	ownerAsk, prop := takeOwnerAsk(spec, prop)
	ctx = withOwnerAsk(ctx, ownerAsk)
	class, prop := takeClass(spec, prop)
	ctx = withClass(ctx, class)
	actor := kiffactor.Actor{ID: id.AgentID, Roles: append([]string(nil), id.Roles...)}
	actionCtx, err := prop.ActionContext(gatewayTrialState, actor)
	if err == nil {
		// The validator alone: ValidateAction would also write the audit
		// log, and a trial records nothing.
		_, err = rt.Validator.Validate(ctx, actionCtx, contract, rt.Permissions)
	}
	if errors.Is(err, kiffaction.ErrApprovalRequired) {
		return gatewayTrialResponse{Decision: "held", Reasons: []string{"approval_required"},
			Message: "the account's contract asks for a person's approval for this tool"}, http.StatusOK
	}
	if err != nil {
		return trialRefused("blocked", err.Error()), http.StatusOK
	}

	requiresAuthority := actionRequiresAuthority(spec, prop.ActionName)
	trier, ok := h.mandates.(mandateTrier)
	if !ok {
		if requiresAuthority {
			return trialRefused(reasonNoMandateIssued, "no Card covers this tool for this agent"), http.StatusOK
		}
		if ownerAsk != "" {
			return gatewayTrialResponse{Decision: "held", Reasons: []string{"owner_ask"}, Message: ownerAskLine(ownerAsk)}, http.StatusOK
		}
		return gatewayTrialResponse{Decision: "allowed", Message: "KIFF would allow this call"}, http.StatusOK
	}
	d, err := trier.TrialWith(ctx, id.Tenant, run, h.mandateRequest(ctx, id, prop, spec, gatewayTrialState), draftCards, retire)
	if err != nil {
		return map[string]string{"error": "the agent's Cards could not be read"}, http.StatusServiceUnavailable
	}
	switch {
	case d.Outcome == mandate.OutcomeNotCovered && requiresAuthority:
		return trialRefused(reasonNoMandateIssued, "no Card covers this tool for this agent"), http.StatusOK
	case d.Outcome == mandate.OutcomeUncountableDraw || d.Outcome == mandate.OutcomeUnreadableResource:
		return trialRefused("invalid_parameter", d.Reason), http.StatusOK
	case d.Allowed():
		return gatewayTrialResponse{Decision: "allowed", Message: "KIFF would allow this call"}, http.StatusOK
	case d.Outcome == mandate.OutcomeOwnerRequired:
		return gatewayTrialResponse{Decision: "held", Reasons: []string{"mandate_owner_required"}, Message: d.Reason}, http.StatusOK
	default:
		return trialRefused("mandate_"+string(d.Outcome), mandateMessage(d)), http.StatusOK
	}
}

// trialDraftCards are the Cards a revision draft would issue, and the ones
// it would retire, for a trial of the version that brings it (kiff/tafs#107).
// A draft that can't be tried is refused, never decided on other Cards.
func (h *ProposalsHandler) trialDraftCards(ctx context.Context, id auth.Identity, draftID string) ([]mandate.Mandate, []string, *gatewayTrialResponse) {
	cards, retire, reason, msg := trialDraft(ctx, h.drafts, h.now(), id, draftID)
	if reason != "" {
		r := trialRefused(reason, msg+" Nothing was sent.")
		return nil, nil, &r
	}
	return cards, retire, nil
}

// trialDraft reads the draft a trial is decided against: the agent's own,
// still waiting for its owner, each Card held by the agent alone. It
// returns its Cards and the ones it retires, or a reason and a message why
// it can't be tried. A trial's decisions and the Card shown to the agent
// during it (GET /v1/me/card?trial=) both come from here, so the agent is
// told the Cards its calls are decided on.
func trialDraft(ctx context.Context, drafts agentdrafts.Store, now time.Time, id auth.Identity, draftID string) (cards []mandate.Mandate, retire []string, reason, msg string) {
	if drafts == nil {
		return nil, nil, "trial_draft_unavailable", "KIFF can't read drafts here."
	}
	d, err := drafts.Get(ctx, id.Tenant, draftID)
	switch {
	case errors.Is(err, agentdrafts.ErrNotFound):
		return nil, nil, "trial_draft_not_found", "this version's draft isn't on the account."
	case err != nil:
		return nil, nil, "trial_draft_unavailable", "KIFF couldn't read this version's draft."
	case d.AgentID != id.AgentID:
		return nil, nil, "trial_draft_other_agent", "this version's draft is for another agent."
	case d.Status != agentdrafts.Draft:
		return nil, nil, "trial_draft_not_waiting", "this version's draft is " + string(d.Status) + ", not waiting for its owner: try the version against the live Cards."
	case !now.Before(d.ExpiresAt):
		return nil, nil, "trial_draft_expired", "this version's draft expired."
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(d.Cards, &raw); err != nil {
		return nil, nil, "trial_draft_invalid", "this version's draft can't be read."
	}
	cards = make([]mandate.Mandate, 0, len(raw))
	for _, c := range raw {
		var body mandateBody
		if err := json.Unmarshal(c, &body); err != nil {
			return nil, nil, "trial_draft_invalid", "this version's draft can't be read."
		}
		m, err := body.draftMandate(id.Tenant)
		if err != nil || m.Subject != id.AgentID || len(m.HolderList()) != 1 {
			return nil, nil, "trial_draft_invalid", "a Card in this version's draft isn't held by this agent alone."
		}
		cards = append(cards, m)
	}
	for _, rv := range d.Retires {
		retire = append(retire, rv.ID)
	}
	return cards, retire, "", ""
}
