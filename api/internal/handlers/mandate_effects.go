package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/kiff/cloud/api/internal/auth"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

func (h *MandatesHandler) reconcileEffects(w http.ResponseWriter, r *http.Request) {
	id, tenant, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID == "" && !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "reconciliation requires the calling agent or a management identity"})
		return
	}
	proposal := r.PathValue("proposal")
	evidence, ok := h.outcomes.(tooloutcome.EvidenceReader)
	s, stores := h.store.(mandate.EffectReleaseStore)
	if !ok || !stores {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "trusted outcome reconciliation unavailable"})
		return
	}
	e, found, err := evidence.NoEffect(r.Context(), tenant, proposal)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "trusted outcome could not be read; budget remains charged"})
		return
	}
	if !found {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no trusted terminal no-effect outcome; budget remains charged"})
		return
	}
	if id.AgentID != "" && !strings.EqualFold(id.AgentID, e.Subject) && !strings.EqualFold(mandate.TrialSubject(id.AgentID), e.Subject) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such operation"})
		return
	}
	releases, err := s.ReleaseEffects(r.Context(), e)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "effect release not recorded; budget remains charged"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": releases, "operation_reopened": false})
}

func (h *MandatesHandler) grantRoom(w http.ResponseWriter, r *http.Request) {
	id, tenant, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "extra room requires management authority; runtime credentials cannot change a Card"})
		return
	}
	if !h.mayChangeCard(w, r, id, tenant, r.PathValue("id")) {
		return
	}
	var body struct {
		ID           string               `json:"revision_id"`
		Quantity     string               `json:"quantity"`
		Window       mandate.Window       `json:"window"`
		Measure      mandate.Measure      `json:"measure"`
		Amount       int64                `json:"amount"`
		Duration     mandate.RoomDuration `json:"duration"`
		Until        time.Time            `json:"until"`
		WindowEnd    time.Time            `json:"window_end"`
		ExpectedBase *int64               `json:"expected_base_limit"`
	}
	if !decodeCardBody(w, http.MaxBytesReader(w, r.Body, 16<<10), &body) {
		return
	}
	s, ok := h.store.(mandate.RoomStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "extra room unavailable"})
		return
	}
	m, err := s.GrantRoom(r.Context(), tenant, r.PathValue("id"), mandate.RoomRequest{ID: body.ID, Quantity: body.Quantity, Window: body.Window, Measure: body.Measure, Amount: body.Amount, Duration: body.Duration, Until: body.Until, WindowEnd: body.WindowEnd, ExpectedBase: body.ExpectedBase}, issuerOf(id), h.now())
	switch {
	case errors.Is(err, mandate.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such Card"})
	case errors.Is(err, mandate.ErrInvalidMandate), errors.Is(err, mandate.ErrMoveInactive):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "extra room not recorded"})
	default:
		writeJSON(w, http.StatusOK, mandateView(m, h.now()))
	}
}

// surchargeEffects records that a vendor used more than its Card decided, as
// the gateway's verify read of that call says: the
// Card counts the larger figure. It reads the gateway's own record, never
// the caller's word, and a repeat charges nothing more.
func (h *MandatesHandler) surchargeEffects(w http.ResponseWriter, r *http.Request) {
	id, tenant, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID == "" && !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "a surcharge is recorded by the calling agent or a management identity"})
		return
	}
	proposal := r.PathValue("proposal")
	evidence, ok := h.outcomes.(tooloutcome.VerifiedReader)
	s, stores := h.store.(mandate.EffectSurchargeStore)
	if !ok || !stores {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "verify reads can't be counted here"})
		return
	}
	e, found, err := evidence.Verified(r.Context(), tenant, proposal)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the verify read could not be read"})
		return
	}
	if !found {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no verify read found the vendor used more"})
		return
	}
	if id.AgentID != "" && !strings.EqualFold(id.AgentID, e.Subject) && !strings.EqualFold(mandate.TrialSubject(id.AgentID), e.Subject) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such operation"})
		return
	}
	surcharges, err := s.SurchargeEffects(r.Context(), e)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the surcharge was not recorded"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"surcharges": surcharges})
}

// happenedStore is what "Did this happen?" reads and keeps (tooloutcome).
type happenedStore interface {
	Unproven(ctx context.Context, tenant, proposal string) ([]tooloutcome.Unproven, error)
	UnprovenCall(ctx context.Context, tenant, proposal string) (tooloutcome.Unproven, bool, error)
	RecordHappened(ctx context.Context, tenant, proposal string, a tooloutcome.HappenedAnswer) (tooloutcome.HappenedAnswer, error)
	// MarkChecking keeps that by asked for help checking proposal, until
	// until; a mark in force is kept and answered instead.
	MarkChecking(ctx context.Context, tenant, proposal, by string, until time.Time) (tooloutcome.Checking, error)
}

// errRoomNotBack: the answer No was kept, and giving the Card its room back
// didn't finish; asking again finishes it.
var errRoomNotBack = errors.New("the answer was kept, but the room was not given back yet; answer again to finish it")

// errRequestNotReleased: the answer No was kept and the Card's room is
// back, but the refund's request wasn't handed back to the tafs yet;
// answering again finishes it, and changes nothing else.
var errRequestNotReleased = errors.New("the answer was kept and the room is back, but the refund's request was not handed back to the tafs yet; answer again to finish it")

// answerFirst keeps the first answer to proposal's question, whoever asks,
// then does what that answer says: the Card's room comes back only when the
// kept answer is No. A later answer, or one racing it, changes nothing; a
// No whose release failed releases on the next ask (releasing is
// idempotent).
func answerFirst(ctx context.Context, s happenedStore, rs mandate.EffectReleaseStore, tenant, proposal string,
	ans tooloutcome.HappenedAnswer) (tooloutcome.HappenedAnswer, bool, error) {
	c, found, err := s.UnprovenCall(ctx, tenant, proposal)
	if err != nil || !found {
		return tooloutcome.HappenedAnswer{}, found, err
	}
	kept, err := s.RecordHappened(ctx, tenant, proposal, ans)
	if err != nil {
		return tooloutcome.HappenedAnswer{}, true, err
	}
	if kept.Happened {
		return kept, true, nil
	}
	if _, err := rs.ReleaseEffects(ctx, mandate.NoEffectEvidence{TenantID: tenant, ProposalID: proposal, Subject: c.Agent,
		Action: c.Tool, Source: tooloutcome.SourceOwnerNo, EvidenceID: "happened-" + proposal,
		Reason: "the owner said it didn't happen", At: kept.AnsweredAt}); err != nil {
		return kept, true, errRoomNotBack
	}
	// A governed refund's request goes back to the tafs: the owner says
	// the money didn't move.
	if t, ok := s.(refundAnswers); ok {
		if err := t.OwnerSaidNotSent(ctx, tenant, proposal); err != nil {
			return kept, true, errRequestNotReleased
		}
	}
	return kept, true, nil
}

// refundAnswers settles a governed refund on the owner's "No".
type refundAnswers interface {
	OwnerSaidNotSent(ctx context.Context, tenant, key string) error
}

// withoutArchived is calls less those of agents archived since: a removed
// taf's Cards are revoked with it, so its asks no longer count on any Card,
// and nobody can act on them. Nothing is answered for the owner, and the
// records stay as they are. Best effort: when the agents can't be read,
// every call stays listed.
func (h *MandatesHandler) withoutArchived(ctx context.Context, tenantID string, calls []tooloutcome.Unproven) []tooloutcome.Unproven {
	if h.agents == nil || len(calls) == 0 {
		return calls
	}
	list, err := h.agents.List(ctx, tenantID)
	if err != nil {
		h.logger.WarnContext(ctx, "did this happen: agents unreadable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return calls
	}
	archived := map[string]bool{}
	for _, a := range list {
		if a.ArchivedAt != nil {
			archived[a.ID] = true
		}
	}
	kept := make([]tooloutcome.Unproven, 0, len(calls))
	for _, c := range calls {
		if !archived[c.Agent] {
			kept = append(kept, c)
		}
	}
	return kept
}

// unproven lists the money calls whose outcome KIFF can't prove, for the
// owner to answer "Did this happen?", each with
// its context and the three answers (kiff/tafs#498), kept to the calls of
// the agents the reader's part of the account holds.
func (h *MandatesHandler) unproven(w http.ResponseWriter, r *http.Request) {
	id, tenant, ok := h.identity(w, r)
	if !ok {
		return
	}
	s, ok := h.outcomes.(happenedStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"calls": []happenedCall{}})
		return
	}
	calls, err := s.Unproven(r.Context(), tenant, "")
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the calls to confirm couldn't be read"})
		return
	}
	calls = h.withoutArchived(r.Context(), tenant, calls)
	if h.ownUse != nil {
		scope, err := h.ownUse.Scope(r.Context(), id, tenant)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "your part of the account couldn't be read"})
			return
		}
		if !scope.Whole() {
			kept := calls[:0]
			for _, c := range calls {
				if scope.Hold(c.Agent, "") {
					kept = append(kept, c)
				}
			}
			calls = kept
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": h.happenedViews(r, tenant, calls)})
}

// answerHappened keeps the owner's signed answer. "No" gives the Card its
// room back: the owner's word is the evidence, as floor rule 1c says, and
// it can't make the call be sent again. "Yes" keeps it counted. "Not
// sure" decides nothing: it keeps the ask with them while Coach helps
// (checkHappened takes it by the device key alone).
func (h *MandatesHandler) answerHappened(w http.ResponseWriter, r *http.Request) {
	id, tenant, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID != "" || id.APIKey {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a person answers whether a call happened"})
		return
	}
	var req struct {
		Happened *bool  `json:"happened"`
		Answer   string `json:"answer"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		req.Answer = ""
	}
	switch req.Answer {
	case answerYes:
		req.Happened = new(bool)
		*req.Happened = true
	case answerNo:
		req.Happened = new(bool)
	case answerNotSure:
		h.markChecking(w, r, id, tenant, r.PathValue("proposal"))
		return
	}
	if req.Happened == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send {\"answer\": \"yes\" | \"no\" | \"not_sure\"} (or {\"happened\": true | false})"})
		return
	}
	// "No" gives the Card its room back, so it widens what the taf may
	// spend today: the approval key, behind the fingerprint, never a tap
	// inside an approval window (kiff/tafs#494). "Yes" only confirms money
	// was counted: routine.
	if with, signed := auth.SignedWithFrom(r.Context()); signed && !*req.Happened && with.Window != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "critical_key_required",
			"detail": "\"No\" gives the Card its room back: sign it with the phone's approval key, behind its fingerprint or face"})
		return
	}
	s, ok := h.outcomes.(happenedStore)
	rs, stores := h.store.(mandate.EffectReleaseStore)
	if !ok || !stores {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "answers can't be kept here"})
		return
	}
	proposal := r.PathValue("proposal")
	a, found, err := answerFirst(r.Context(), s, rs, tenant, proposal,
		tooloutcome.HappenedAnswer{Happened: *req.Happened, AnsweredBy: id.Subject, AnsweredAt: h.now()})
	switch {
	case errors.Is(err, errRoomNotBack), errors.Is(err, errRequestNotReleased):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the answer was not kept"})
		return
	case !found:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no call waits for this answer"})
		return
	}
	out := map[string]any{"proposal": proposal, "answer": a}
	if !*req.Happened {
		// Nothing is reversed at the vendor by a No: the owner checks there.
		out["words"] = happenedNoWords(replyLanguage(r))
	}
	writeJSON(w, http.StatusOK, out)
}

// happenedNoWords is what the owner reads after a No, in lang: the Card
// has its room back and nothing was reversed at the vendor, so they check
// there, and tell Coach if the payment went through after all.
func happenedNoWords(lang string) string {
	return wordsIn(lang).HappenedNoAfter
}
