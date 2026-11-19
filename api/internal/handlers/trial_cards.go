package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
	"github.com/oklog/ulid/v2"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/proposals"
)

// Trying a version (kiff/tafs#173). The owner tries an agent's next
// version beside the live one: confirming its draft "as": "trial" issues
// the version's Cards as trial Cards, held by mandate.TrialSubject(agent)
// with their own ids and counters, and the draft is trying. A call the
// gateway makes with KIFF-Trial naming that draft carries trial_draft to
// decide, and is decided on the trial Cards alone and sent when allowed.
// Calls without it never see trial Cards, so the live version keeps its
// Cards, limits and schedule.
//
// The trial ends when its owner goes back (DELETE …/trial, from the phone),
// when the agent's runtime ends it idle (the same route, with the agent's
// own key), when the version is installed (confirm, as for any draft), or
// when its Cards lapse after agentdrafts.TrialTTL. Its trial Cards are
// revoked then, and a call held under them is refused with trial_ended.
// Its counts are the calls the record keeps under trial_draft.

// agentsOwn reports whether subject, a held call's or a Card's holder, is
// agent: the agent itself, or its trial's Cards.
func agentsOwn(subject, agent string) bool {
	return agent != "" && (subject == agent || subject == mandate.TrialSubject(agent))
}

// withTrialOf says, on a held call's view, that a trial made it
// (kiff/tafs#173): the agent being tried, beside its trial subject, and the
// draft whose trial it is.
func withTrialOf(v map[string]any, e mandate.Exception) {
	if agent, ok := mandate.TrialAgent(e.Subject); ok {
		v["agent"], v["trial_draft"] = agent, e.Request.TrialDraft
	}
}

type trialDraftKey struct{}

// withTrialDraft marks ctx as deciding a call of draft's trial.
func withTrialDraft(ctx context.Context, draft string) context.Context {
	return context.WithValue(ctx, trialDraftKey{}, draft)
}

// trialDraftFrom is the draft whose trial ctx decides a call of, or "".
func trialDraftFrom(ctx context.Context) string {
	s, _ := ctx.Value(trialDraftKey{}).(string)
	return s
}

// trialRefusal refuses a trial's call unless its trial is open: the
// draft is the agent's own and being tried, its Cards not lapsed. Recorded
// under the trial, so its counts include it.
func (h *ProposalsHandler) trialRefusal(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal) (proposalResponse, bool) {
	draft := trialDraftFrom(ctx)
	if draft == "" {
		return proposalResponse{}, false
	}
	now := h.now()
	reason, msg := "trial_unavailable", "KIFF can't read this trial here, so the call was refused. Nothing was sent."
	if h.drafts != nil {
		d, err := h.drafts.Get(ctx, id.Tenant, draft)
		switch {
		case errors.Is(err, agentdrafts.ErrNotFound) || (err == nil && d.AgentID != id.AgentID):
			reason, msg = "trial_draft_not_found", "this trial's draft isn't this agent's. Nothing was sent."
		case err != nil:
			h.logger.ErrorContext(ctx, "proposals: trial draft unavailable", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		case d.TrialOpen(now):
			return proposalResponse{}, false
		default:
			reason, msg = "trial_not_trying", "this version isn't being tried, so its calls aren't sent. Nothing was sent."
			if _, _, ended := d.TrialEnd(now); ended {
				reason, msg = "trial_ended", "the trial of this version ended, so its calls aren't sent any more. Nothing was sent."
			}
		}
	}
	reasons := []string{reason}
	h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
	return proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeBlocked), Reasons: reasons, Message: msg}, true
}

// WithMandatesCalls gives the handler the account's calls, which a
// trial's counts are read from (kiff/tafs#173). Nil: a trial shows no
// counts.
func WithMandatesCalls(calls proposals.CallsReader) MandatesOption {
	return func(h *MandatesHandler) { h.calls = calls }
}

func (h *MandatesHandler) registerTrials(mux *http.ServeMux) {
	if h.drafts == nil {
		return
	}
	mux.HandleFunc("GET /v1/me/agent-drafts/{id}/trial", h.getTrial)
	mux.HandleFunc("DELETE /v1/me/agent-drafts/{id}/trial", h.endTrialRoute)
}

// startTrial confirms a revision draft of exactly the version shown as a
// trial: its Cards, over the agent's Cards in force, are issued as trial
// Cards with their own ids, and the draft is trying. The agent's other
// open trial ends first: one trial per agent.
func (h *MandatesHandler) startTrial(r *http.Request, id auth.Identity, draftID, version string) (agentdrafts.D, int, string) {
	if !mayConfirm(id) {
		return agentdrafts.D{}, http.StatusForbidden, "trying a version needs a person or key that may issue Cards and mint keys"
	}
	tenantID := id.Tenant
	cur, err := h.drafts.Get(r.Context(), tenantID, draftID)
	switch {
	case err != nil:
	case cur.Status != agentdrafts.Draft:
		err = agentdrafts.ErrState
	case !h.now().Before(cur.ExpiresAt):
		err = agentdrafts.ErrExpired
	case cur.Version != version:
		// Before anything changes: the agent's open trial ends only for
		// the version the owner was shown.
		err = agentdrafts.ErrVersion
	}
	if err != nil {
		return agentdrafts.D{}, draftStatus(err), draftMessage(err)
	}
	if !cur.Revision {
		return agentdrafts.D{}, http.StatusConflict, "only a new version of an agent that already holds Cards can be tried; confirm a new agent's draft instead"
	}
	var raw []json.RawMessage
	_ = json.Unmarshal(cur.Cards, &raw)
	plan, status, msg := h.draftCards(r, tenantID, cur.AgentID, raw, len(raw) == 0, cur.RetireRest)
	if status != 0 {
		return agentdrafts.D{}, status, "the version could not be tried: " + msg
	}
	if !slices.Equal(plan.revises, cur.Revises) || !slices.Equal(plan.retires, cur.Retires) {
		return agentdrafts.D{}, http.StatusConflict, cardRevisedMsg
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), agentdrafts.ConfirmTimeout)
	defer cancel()
	open, err := h.drafts.TryingFor(ctx, tenantID, cur.AgentID)
	if err != nil {
		h.logger.ErrorContext(ctx, "agent draft: list trials failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return agentdrafts.D{}, http.StatusInternalServerError, "the agent's open trial could not be read"
	}
	for _, o := range open {
		by := agentdrafts.EndedBack
		if !o.TrialOpen(h.now()) {
			by = agentdrafts.EndedExpired // it lapsed before this one started
		}
		if _, err := h.endTrial(ctx, o, by); err != nil && !errors.Is(err, agentdrafts.ErrState) {
			h.logger.ErrorContext(ctx, "agent draft: end trial failed", slog.String("tenant", tenantID), slog.String("draft", o.ID), slog.String("err", err.Error()))
			return agentdrafts.D{}, http.StatusInternalServerError, "the agent's open trial could not be ended"
		}
	}
	live, err := h.store.ForSubject(ctx, tenantID, cur.AgentID)
	if err != nil {
		return agentdrafts.D{}, http.StatusInternalServerError, "the agent's Cards could not be read"
	}
	now := h.now()
	until := now.Add(agentdrafts.TrialTTL)
	retire := make([]string, len(cur.Retires))
	for i, rv := range cur.Retires {
		retire[i] = rv.ID
	}
	// Each trial issues Cards under new ids: a revoked id is never issued
	// again, and its draws stay with it.
	tag := strings.ToLower(ulid.MustNew(ulid.Timestamp(now), ulid.DefaultEntropy()).String()[18:])
	cards := trialCards(cur.AgentID, plan.cards, live, retire, now, until, tag)
	ids := make([]string, len(cards))
	for i, m := range cards {
		ids[i] = m.ID
		cards[i].DraftID = cur.ID // every trial Card is issued by this draft
	}
	d, err := h.drafts.StartTrial(ctx, tenantID, draftID, version, agentdrafts.Trial{CardIDs: ids, StartedAt: now, ExpiresAt: until}, now)
	if err != nil {
		return agentdrafts.D{}, draftStatus(err), draftMessage(err)
	}
	if err := h.store.IssueAll(ctx, cards, issuerOf(id), nil, nil, now); err != nil {
		h.logger.ErrorContext(ctx, "agent draft: issue trial Cards failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		if _, err := h.drafts.EndTrial(ctx, tenantID, d.ID, agentdrafts.EndedBack, now); err != nil {
			h.logger.ErrorContext(ctx, "agent draft: end failed trial failed", slog.String("tenant", tenantID), slog.String("draft", d.ID), slog.String("err", err.Error()))
		}
		return agentdrafts.D{}, http.StatusInternalServerError, "the trial could not start: its Cards could not be issued"
	}
	return d, 0, ""
}

// trialCards are a trial's Cards: the agent's Cards in force, less those
// the draft retires, with the draft's Cards over them, each held by the
// agent's trial subject alone under the id <id>~trial-<tag>, from now until
// `until` at the latest.
func trialCards(agentID string, draft, live []mandate.Mandate, retire []string, now, until time.Time, tag string) []mandate.Mandate {
	byID := map[string]mandate.Mandate{}
	for _, m := range live {
		if !m.Active(now) || m.ArchivedAt != nil || slices.Contains(retire, m.ID) {
			continue
		}
		byID[m.ID] = m
	}
	for _, c := range draft {
		if cur, ok := byID[c.ID]; ok {
			byID[c.ID] = mandate.AsIssuedOver(c, &cur)
		} else {
			byID[c.ID] = mandate.AsIssuedOver(c, nil)
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]mandate.Mandate, 0, len(ids))
	for _, id := range ids {
		m := byID[id]
		m.ID = id + mandate.TrialSuffix + "-" + tag
		m.Subject, m.Holders = mandate.TrialSubject(agentID), nil
		m.RevokedAt, m.ArchivedAt = nil, nil
		m.ValidFrom = now
		if m.ValidUntil.IsZero() || m.ValidUntil.After(until) {
			m.ValidUntil = until
		}
		out = append(out, m)
	}
	return out
}

// endTrial ends d's trial (by: how) and revokes its Cards, which withdraws
// the calls held under them. The trial is ended even when a Card can't be
// revoked yet: its calls are refused from then on (trialRefusal), and the
// error says the Cards are still being revoked.
func (h *MandatesHandler) endTrial(ctx context.Context, d agentdrafts.D, by string) (agentdrafts.D, error) {
	ended, err := h.drafts.EndTrial(ctx, d.TenantID, d.ID, by, h.now())
	if err != nil {
		return agentdrafts.D{}, err
	}
	return ended, h.revokeTrialCards(ctx, ended)
}

// revokeTrialCards revokes d's trial Cards. Revoking one again keeps when
// it was first revoked, so it can be retried.
func (h *MandatesHandler) revokeTrialCards(ctx context.Context, d agentdrafts.D) error {
	if d.Trial == nil {
		return nil
	}
	for _, id := range d.Trial.CardIDs {
		if err := h.store.Revoke(ctx, d.TenantID, id, h.now()); err != nil && !errors.Is(err, mandate.ErrNotFound) {
			return fmt.Errorf("revoke trial Card %s: %w", id, err)
		}
	}
	return nil
}

// trialDraftFor reads the draft a trial route names. An agent's key reads
// only its own agent's drafts; a draft never tried has no trial.
func (h *MandatesHandler) trialDraftFor(w http.ResponseWriter, r *http.Request) (auth.Identity, agentdrafts.D, bool) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return auth.Identity{}, agentdrafts.D{}, false
	}
	d, err := h.drafts.Get(r.Context(), tenantID, r.PathValue("id"))
	if err == nil && id.APIKey && d.AgentID != id.AgentID {
		err = agentdrafts.ErrNotFound
	}
	if err != nil {
		writeDraftError(w, err)
		return auth.Identity{}, agentdrafts.D{}, false
	}
	if d.Trial == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this draft was never tried"})
		return auth.Identity{}, agentdrafts.D{}, false
	}
	return id, d, true
}

// getTrial serves GET /v1/me/agent-drafts/{id}/trial: whether the draft is
// being tried, how its trial ended, and its calls so far.
func (h *MandatesHandler) getTrial(w http.ResponseWriter, r *http.Request) {
	_, d, ok := h.trialDraftFor(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.trialView(r.Context(), d))
}

// endTrialRoute serves DELETE /v1/me/agent-drafts/{id}/trial. From the
// owner's phone it is Go back: the trial ends and the draft waits again.
// The agent's own key may end its own draft's trial (its runtime's sweep
// of idle trials); that only takes authority away, and is recorded as
// expired. It answers the trial's final counts.
func (h *MandatesHandler) endTrialRoute(w http.ResponseWriter, r *http.Request) {
	id, d, ok := h.trialDraftFor(w, r)
	if !ok {
		return
	}
	if !id.APIKey && !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "ending a trial is a management action"})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), agentdrafts.ConfirmTimeout)
	defer cancel()
	by := agentdrafts.EndedBack
	if id.APIKey || !d.TrialOpen(h.now()) {
		by = agentdrafts.EndedExpired
	}
	ended, err := h.endTrial(ctx, d, by)
	switch {
	case errors.Is(err, agentdrafts.ErrState):
		// Already ended: its Cards are revoked again, in case that was
		// interrupted, and it says how it ended.
		if err := h.revokeTrialCards(ctx, d); err != nil {
			h.logger.WarnContext(ctx, "agent draft: revoke trial Cards failed", slog.String("tenant", d.TenantID), slog.String("draft", d.ID), slog.String("err", err.Error()))
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this draft isn't being tried", "trial": h.trialView(ctx, d)})
	case err != nil && ended.ID == "":
		writeDraftError(w, err)
	case err != nil:
		h.logger.ErrorContext(ctx, "agent draft: revoke trial Cards failed", slog.String("tenant", d.TenantID), slog.String("draft", d.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the trial ended and its calls are refused, but some of its Cards are not revoked yet; try again"})
	default:
		writeJSON(w, http.StatusOK, h.trialView(ctx, ended))
	}
}

// trialView is a draft's trial: trying or ended (how and when), its
// Cards, and its calls (kiff/tafs#173), counted from the account's record,
// so the app and the agent's runtime can't disagree with it.
func (h *MandatesHandler) trialView(ctx context.Context, d agentdrafts.D) map[string]any {
	t := d.Trial
	v := map[string]any{"draft": d.ID, "agent_id": d.AgentID, "version": d.Version, "status": "trying",
		"cards": t.CardIDs, "started_at": t.StartedAt, "expires_at": t.ExpiresAt}
	if at, by, ended := d.TrialEnd(h.now()); ended {
		v["status"], v["ended_at"], v["ended_by"] = "ended", at, by
	}
	if h.calls == nil {
		return v
	}
	page, err := h.calls.ListCalls(ctx, d.TenantID, proposals.CallQuery{Trial: d.ID, Since: t.StartedAt, Limit: 1})
	if err != nil {
		h.logger.WarnContext(ctx, "agent draft: trial counts unavailable", slog.String("tenant", d.TenantID), slog.String("draft", d.ID), slog.String("err", err.Error()))
		return v
	}
	c := page.Counts
	v["calls"], v["allowed"], v["held"], v["refused"] = c.All, c.Allowed, c.Held, c.Refused
	return v
}

// trialSummary is the trial a draft view carries: nil when never tried.
func trialSummary(d agentdrafts.D, now time.Time) map[string]any {
	t := d.Trial
	if t == nil {
		return nil
	}
	v := map[string]any{"cards": t.CardIDs, "started_at": t.StartedAt, "expires_at": t.ExpiresAt}
	if at, by, ended := d.TrialEnd(now); ended {
		v["ended_at"], v["ended_by"] = at, by
	}
	return v
}
