package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

const approverPrefix = "kiff_approver_"

func tokenHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func (h *MandatesHandler) registerApproverManagement(mux *http.ServeMux) {
	if _, ok := h.store.(mandate.ApproverStore); !ok {
		return
	}
	mux.HandleFunc("POST /v1/me/approver-grants", h.createApprover)
	mux.HandleFunc("GET /v1/me/approver-grants", h.listApprovers)
	mux.HandleFunc("POST /v1/me/approver-grants/{id}/revoke", h.revokeApprover)
}

// These narrowly mounted routes authenticate their own restricted token.
// That token is unknown to the regular session/runtime authenticator, and
// cannot reach any other API or gateway surface.
func (h *MandatesHandler) RegisterExternalApprover(mux *http.ServeMux) {
	if h == nil {
		return
	}
	if _, ok := h.store.(mandate.ApproverStore); !ok {
		return
	}
	mux.HandleFunc("GET /v1/approver/holds", h.externalHolds)
	mux.HandleFunc("GET /v1/approver/holds/{id}", h.externalHold)
	mux.HandleFunc("POST /v1/approver/holds/{id}/answer", h.externalAnswer)
}

func (h *MandatesHandler) createApprover(w http.ResponseWriter, r *http.Request) {
	id, t, ok := h.exceptionsIdentity(w, r, membership.CapKeysManage)
	if !ok {
		return
	}
	// The owner makes the delegation in a signed-in management session.
	if !id.IsPerson() || id.MemberRole != membership.RoleAdmin {
		writeJSON(w, 403, map[string]string{"error": "an authenticated admin session creates an approver grant"})
		return
	}
	var req struct {
		Name      string    `json:"name"`
		Principal string    `json:"principal"`
		CardID    string    `json:"card_id"`
		Worker    string    `json:"worker"`
		Action    string    `json:"action"`
		Parameter string    `json:"parameter"`
		Max       int64     `json:"max"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if !approverDecode(w, r, &req) {
		return
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		approverError(w, err)
		return
	}
	secret := approverPrefix + base64.RawURLEncoding.EncodeToString(entropy[:])
	now := h.now()
	g := mandate.ApproverGrant{ID: "apg_" + hex.EncodeToString(entropy[:12]), TenantID: t, Name: strings.TrimSpace(req.Name), Principal: strings.TrimSpace(req.Principal), CardID: req.CardID, Worker: req.Worker, Action: req.Action, Parameter: req.Parameter, Max: req.Max, CreatedBy: id.Subject, CreatedAt: now, ExpiresAt: req.ExpiresAt, KeyHash: tokenHash(secret)}
	if g.Principal == "" {
		g.Principal = "approver:" + g.ID
	}
	if len(g.Principal) > 120 || len(g.Worker) > 120 || len(g.Action) > 120 || len(g.Parameter) > 120 {
		approverError(w, mandate.ErrApproverGrant)
		return
	}
	if err := h.store.(mandate.ApproverStore).CreateApproverGrant(r.Context(), g); err != nil {
		approverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"grant": g, "credential": secret})
}
func (h *MandatesHandler) revokeApprover(w http.ResponseWriter, r *http.Request) {
	_, t, ok := h.exceptionsIdentity(w, r, membership.CapKeysManage)
	if !ok {
		return
	}
	if err := h.store.(mandate.ApproverStore).RevokeApproverGrant(r.Context(), t, r.PathValue("id"), h.now()); err != nil {
		approverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
}
func (h *MandatesHandler) externalIdentity(w http.ResponseWriter, r *http.Request) (mandate.ApproverGrant, bool) {
	w.Header().Set("Cache-Control", "no-store")
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer "+approverPrefix) || len(value) > 160 {
		writeJSON(w, 401, map[string]string{"error": "a scoped approver credential is required"})
		return mandate.ApproverGrant{}, false
	}
	g, err := h.store.(mandate.ApproverStore).ApproverByHash(r.Context(), tokenHash(strings.TrimPrefix(value, "Bearer ")))
	if err != nil {
		approverError(w, err)
		return g, false
	}
	if !g.Active(h.now()) {
		approverError(w, mandate.ErrApproverGrant)
		return g, false
	}
	return g, true
}

type approverView struct {
	Hold        map[string]any        `json:"hold"`
	Card        mandate.Mandate       `json:"card"`
	Grant       mandate.ApproverGrant `json:"grant"`
	ReviewToken string                `json:"review_token"`
	State       string                `json:"-"`
	CardState   string                `json:"-"`
}

func (h *MandatesHandler) reviewExternal(r *http.Request, g mandate.ApproverGrant, id string) (approverView, error) {
	e, err := h.store.Exception(r.Context(), g.TenantID, id)
	if err != nil {
		return approverView{}, err
	}
	cards, err := h.store.ForSubject(r.Context(), g.TenantID, g.Worker)
	if err != nil {
		return approverView{}, err
	}
	var m mandate.Mandate
	for _, c := range cards {
		if c.ID == g.CardID {
			m = c
		}
	}
	if !mandate.ApproverCovers(g, e, m, h.now()) {
		return approverView{}, mandate.ErrExceptionNotFound
	}
	p, err := mandate.ReadException(r.Context(), h.store, e, h.now())
	if err != nil {
		return approverView{}, err
	}
	e = p.Exception
	if !mandate.ApproverCovers(g, e, m, h.now()) {
		return approverView{}, mandate.ErrExceptionNotFound
	}
	args, ok := h.heldArguments(r, g.TenantID, []mandate.Exception{e})
	if !ok {
		return approverView{}, errors.New("original arguments unavailable")
	}
	// Gateway arguments are immutable for this bound operation. Never ask
	// a decision model to guess them from numeric Card parameters.
	if heldGatewayCall(e) && len(args[e.ProposalID]) == 0 {
		return approverView{}, errors.New("original arguments unavailable")
	}
	v := h.amountUnits(r, g.TenantID).annotate(withArguments(projectedExceptionView(p, h.now()), args, ok, e), e)
	v["external_answer"] = e.ExternalAnswer
	state, cardState := mandate.ReviewState(e), mandate.ReviewCard(m)
	raw, err := json.Marshal(struct {
		State, Card, Grant string
		Arguments          json.RawMessage
	}{state, cardState, g.ID, args[e.ProposalID]})
	if err != nil {
		return approverView{}, err
	}
	return approverView{Hold: v, Card: m, Grant: g, ReviewToken: tokenHash(string(raw)), State: state, CardState: cardState}, nil
}
func (h *MandatesHandler) externalHold(w http.ResponseWriter, r *http.Request) {
	g, ok := h.externalIdentity(w, r)
	if !ok {
		return
	}
	v, err := h.reviewExternal(r, g, r.PathValue("id"))
	if err != nil {
		approverError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (h *MandatesHandler) externalHolds(w http.ResponseWriter, r *http.Request) {
	g, ok := h.externalIdentity(w, r)
	if !ok {
		return
	}
	// A withdrawn approval is held in projection before any worker retry.
	// Include stored approvals, then filter on their current projected state.
	es, err := h.store.Exceptions(r.Context(), g.TenantID, "")
	if err != nil {
		approverError(w, err)
		return
	}
	out := []approverView{}
	for _, e := range es {
		if e.Status != mandate.ExceptionHeld && e.Status != mandate.ExceptionApproved {
			continue
		}
		if e.Subject != g.Worker || !strings.EqualFold(e.Action, g.Action) {
			continue
		}
		v, err := h.reviewExternal(r, g, e.ID)
		if errors.Is(err, mandate.ErrExceptionNotFound) {
			continue
		}
		if err != nil {
			approverError(w, err)
			return
		}
		if v.Hold["status"] == string(mandate.ExceptionHeld) {
			out = append(out, v)
		}
	}
	if err := h.store.(mandate.ApproverStore).TouchApprover(r.Context(), g.TenantID, g.ID, h.now()); err != nil {
		approverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"holds": out})
}
func (h *MandatesHandler) externalAnswer(w http.ResponseWriter, r *http.Request) {
	g, ok := h.externalIdentity(w, r)
	if !ok {
		return
	}
	var req struct {
		ReviewToken string `json:"review_token"`
		Decision    string `json:"decision"`
		ReasonCode  string `json:"reason_code"`
		Reason      string `json:"reason,omitempty"`
		Model       string `json:"model,omitempty"`
	}
	if !approverDecode(w, r, &req) {
		return
	}
	// A retry can arrive after collection or rejection, when pending tool
	// arguments are no longer exposed. Replay the recorded bound answer;
	// never reinterpret it as permission for a new request.
	old, err := h.store.Exception(r.Context(), g.TenantID, r.PathValue("id"))
	if err == nil && old.ExternalAnswer != nil && old.ExternalAnswer.GrantID == g.ID && old.ExternalAnswer.ReviewToken == req.ReviewToken {
		a := mandate.ExternalAnswer{GrantID: g.ID, Principal: g.Principal, Decision: req.Decision, ReasonCode: req.ReasonCode, ReviewToken: req.ReviewToken, Model: req.Model, Reason: req.Reason}
		e, err := h.store.(mandate.ApproverStore).AnswerExternal(r.Context(), g.TenantID, old.ID, g.ID, mandate.ExternalReview{Answer: a}, h.now())
		if err != nil {
			approverError(w, err)
			return
		}
		v := exceptionView(e, h.now())
		v["external_answer"] = e.ExternalAnswer
		writeJSON(w, 200, v)
		return
	}
	v, err := h.reviewExternal(r, g, r.PathValue("id"))
	if err != nil {
		approverError(w, err)
		return
	}
	if req.ReviewToken == "" || req.ReviewToken != v.ReviewToken {
		approverError(w, mandate.ErrStaleReview)
		return
	}
	a := mandate.ExternalAnswer{GrantID: g.ID, Principal: g.Principal, Decision: req.Decision, ReasonCode: req.ReasonCode, ReviewToken: req.ReviewToken, Model: req.Model, Reason: req.Reason}
	e, err := h.store.(mandate.ApproverStore).AnswerExternal(r.Context(), g.TenantID, r.PathValue("id"), g.ID, mandate.ExternalReview{State: v.State, Card: v.CardState, Answer: a}, h.now())
	if err != nil {
		approverError(w, err)
		return
	}
	if e.Status != mandate.ExceptionHeld {
		h.alertAnswered(e, h.now())
	}
	answer := exceptionView(e, h.now())
	answer["external_answer"] = e.ExternalAnswer
	writeJSON(w, 200, answer)
}
func approverDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "one JSON object required"})
		return false
	}
	return true
}
func approverError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	message := "the request could not be checked; nothing was authorized"
	switch {
	case errors.Is(err, mandate.ErrExceptionNotFound):
		status, message = 404, "no such held request"
	case errors.Is(err, mandate.ErrApproverGrant):
		status, message = 403, err.Error()
	case errors.Is(err, mandate.ErrStaleReview), errors.Is(err, mandate.ErrExceptionState), errors.Is(err, mandate.ErrExceptionExpired), errors.Is(err, mandate.ErrCumulativeApproval):
		status, message = 409, err.Error()
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func (h *MandatesHandler) listApprovers(w http.ResponseWriter, r *http.Request) {
	_, tenant, ok := h.exceptionsIdentity(w, r, membership.CapKeysManage)
	if !ok {
		return
	}
	card := strings.TrimSpace(r.URL.Query().Get("card_id"))
	if card == "" {
		writeJSON(w, 400, map[string]string{"error": "card_id is required"})
		return
	}
	store := h.store.(mandate.ApproverStore)
	grants, err := store.ListApproverGrants(r.Context(), tenant, card)
	if err != nil {
		approverError(w, err)
		return
	}
	now := h.now()
	for i := range grants {
		g := &grants[i]
		switch {
		case g.RevokedAt != nil:
			g.ConnectionStatus = "revoked"
		case !now.Before(g.ExpiresAt):
			g.ConnectionStatus = "expired"
		default:
			cards, err := h.store.ForSubject(r.Context(), tenant, g.Worker)
			if err != nil {
				approverError(w, err)
				return
			}
			var m mandate.Mandate
			for _, c := range cards {
				if c.ID == g.CardID {
					m = c
				}
			}
			_, err = store.ApproverGrant(r.Context(), tenant, g.ID)
			if err != nil && !errors.Is(err, mandate.ErrApproverGrant) {
				approverError(w, err)
				return
			}
			if err != nil || !g.ValidFor(m, now) {
				g.ConnectionStatus = "inactive"
			} else if g.LastSeenAt == nil {
				g.ConnectionStatus = "awaiting_connection"
			} else if now.Sub(*g.LastSeenAt) <= 2*time.Minute {
				g.ConnectionStatus = "connected"
			} else {
				g.ConnectionStatus = "not_responding"
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"grants": grants})
}
