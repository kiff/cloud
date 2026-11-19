package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handoff"
	"github.com/kiff/cloud/api/internal/mandate"
)

// HandoffsHandler serves "Your step" (package handoff): a step a taf may
// never take itself, which it prepares and hands to its owner to finish.
//
//	POST /v1/me/handoffs               the taf raises one, with its own key
//	GET  /v1/me/handoffs               the ones waiting: a person's to answer, or a taf's own
//	GET  /v1/me/handoffs/{id}          one, for its taf or a person who manages it
//	POST /v1/me/handoffs/{id}/answer   a person answers "done" or "skipped"
//	POST /v1/me/handoffs/{id}/check    after "done", the taf names its read of the vendor
//
// A hand-off grants nothing: the step it stands for stays refused at the
// gateway, and KIFF sends nothing for it. Its receipts say what the owner
// answered (handoff.Sweeper).
type HandoffsHandler struct {
	store   handoff.Store
	ownUse  *OwnUse
	sweeper *handoff.Sweeper
	alerts  alert.Notifier
	logger  *slog.Logger
	now     func() time.Time
}

// NewHandoffsHandler returns the handler. ownUse keeps a member to the tafs
// they manage (nil: everyone in the account answers, as for an admin);
// sweeper saves receipts as they're owed, and alerts tells the agent's
// runtime a hand-off closed.
func NewHandoffsHandler(store handoff.Store, ownUse *OwnUse, sweeper *handoff.Sweeper, alerts alert.Notifier, logger *slog.Logger) *HandoffsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &HandoffsHandler{store: store, ownUse: ownUse, sweeper: sweeper, alerts: alerts, logger: logger,
		now: func() time.Time { return time.Now().UTC() }}
}

// Register attaches the routes.
func (h *HandoffsHandler) Register(mux *http.ServeMux) {
	if h == nil || h.store == nil {
		return
	}
	mux.HandleFunc("POST /v1/me/handoffs", h.raise)
	mux.HandleFunc("GET /v1/me/handoffs", h.list)
	mux.HandleFunc("GET /v1/me/handoffs/{id}", h.get)
	mux.HandleFunc("POST /v1/me/handoffs/{id}/answer", h.answer)
	mux.HandleFunc("POST /v1/me/handoffs/{id}/check", h.check)
}

// maxHandoffsListed bounds one list.
const maxHandoffsListed = 100

func (h *HandoffsHandler) identity(w http.ResponseWriter, r *http.Request) (auth.Identity, string, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return auth.Identity{}, "", false
	}
	tenantID := strings.TrimSpace(id.Tenant)
	if tenantID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return auth.Identity{}, "", false
	}
	return id, tenantID, true
}

func handoffView(x handoff.Handoff) map[string]any {
	v := map[string]any{"id": x.ID, "agent": x.Agent, "what": x.What, "why": x.Why, "prepared_by": x.PreparedBy,
		"version": x.Version, "status": x.Status, "created_at": x.CreatedAt, "expires_at": x.ExpiresAt}
	if x.LinkHost != "" {
		v["link_host"] = x.LinkHost
	}
	if x.About != "" {
		v["about"] = x.About
	}
	if x.AnsweredAt != nil {
		v["answered_at"] = *x.AnsweredAt
	}
	if x.CheckedBy != "" {
		v["checked_by"] = x.CheckedBy
		v["checked_at"] = x.CheckedAt
	}
	if x.AlreadyWaiting {
		// Raised by an earlier call: the taf hands nothing over twice.
		v["already_waiting"] = true
	}
	return v
}

// receipt saves what x owes now, best effort: the sweep saves it otherwise.
func (h *HandoffsHandler) receipt(ctx context.Context, x handoff.Handoff) {
	if h.sweeper == nil {
		return
	}
	if err := h.sweeper.Receipt(ctx, x, h.now()); err != nil {
		h.logger.WarnContext(ctx, "handoffs: receipt left to the sweep", slog.String("handoff", x.ID), slog.String("err", err.Error()))
	}
}

func (h *HandoffsHandler) raise(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a key bound to an agent hands a step to its owner"})
		return
	}
	var req handoff.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send {what, why, operation, link?, about?, prepared_by?}"})
		return
	}
	now := h.now()
	x, err := req.Validate(tenantID, id.AgentID, now)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	calls, err := h.store.Operations(r.Context(), tenantID, ownAgents(id.AgentID), x.PreparedBy)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the calls it names couldn't be read"})
		return
	}
	// Each call that prepared the step was the agent's own and was sent on
	// its own: a hand-off never stands in for a refused or held call. KIFF
	// keeps the calls' proposal ids.
	if x.PreparedBy, err = handoff.CheckPrepared(calls, x.PreparedBy, id.AgentID, mandate.TrialSubject); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "prepared_by_invalid", "detail": err.Error()})
		return
	}
	got, created, err := h.store.Raise(r.Context(), x)
	switch {
	case errors.Is(err, handoff.ErrTooMany):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_waiting",
			"detail": "this taf already has as many steps waiting for its owner as it may; wait for an answer"})
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "handoffs: raise failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the hand-off was not kept"})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		h.receipt(r.Context(), got)
	}
	writeJSON(w, status, handoffView(got))
}

// ownAgents are the gateway's names for agent's calls: its own, and its
// trial's.
func ownAgents(agent string) []string { return []string{agent, mandate.TrialSubject(agent)} }

// visible reports whether id may see x: its own agent, or a person who
// manages that agent.
func (h *HandoffsHandler) visible(ctx context.Context, id auth.Identity, tenantID string, x handoff.Handoff) (bool, error) {
	if id.AgentID != "" {
		return handoff.Owns(x.Agent, id.AgentID, mandate.TrialSubject), nil
	}
	if id.APIKey {
		return false, nil
	}
	scope, err := h.ownUse.Scope(ctx, id, tenantID)
	if err != nil {
		return false, err
	}
	return scope.Manages(x.Agent), nil
}

func (h *HandoffsHandler) load(w http.ResponseWriter, r *http.Request, id auth.Identity, tenantID string) (handoff.Handoff, bool) {
	x, err := h.store.Get(r.Context(), tenantID, r.PathValue("id"))
	if errors.Is(err, handoff.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such hand-off"})
		return handoff.Handoff{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the hand-off couldn't be read"})
		return handoff.Handoff{}, false
	}
	ok, err := h.visible(r.Context(), id, tenantID, x)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "your part of the account couldn't be read"})
		return handoff.Handoff{}, false
	}
	if !ok {
		// Another agent's, or a taf this person doesn't manage: the same
		// answer as none, so it confirms nothing.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such hand-off"})
		return handoff.Handoff{}, false
	}
	return x, true
}

func (h *HandoffsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if x, ok := h.load(w, r, id, tenantID); ok {
		writeJSON(w, http.StatusOK, handoffView(x))
	}
}

func (h *HandoffsHandler) list(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.APIKey && id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "hand-offs are listed for a person or a taf's own key"})
		return
	}
	// The account's open hand-offs, oldest first, then those this caller may
	// see. With 20 open per agent at most, a member could miss theirs only
	// once more than 20 agents are full; filtering by agent in SQL is the
	// fix if that ever comes near.
	open, err := h.store.Open(r.Context(), tenantID, h.now(), maxHandoffsListed*4)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "hand-offs couldn't be read"})
		return
	}
	out := []map[string]any{}
	for _, x := range open {
		ok, err := h.visible(r.Context(), id, tenantID, x)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "your part of the account couldn't be read"})
			return
		}
		if ok && len(out) < maxHandoffsListed {
			out = append(out, handoffView(x))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"handoffs": out})
}

// handoffAnswerBody is an answer: "done" or "skipped", and a phone's
// signature over auth.HandoffAnswer's message.
type handoffAnswerBody struct {
	Answer    string           `json:"answer"`
	Signature *answerSignature `json:"signature,omitempty"`
}

func (h *HandoffsHandler) answer(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID != "" || id.APIKey {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a person answers a step handed to them"})
		return
	}
	var body handoffAnswerBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil ||
		(body.Answer != string(handoff.Done) && body.Answer != string(handoff.Skipped)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"answer": "done" | "skipped", "signature": {...}}`})
		return
	}
	x, ok := h.load(w, r, id, tenantID)
	if !ok {
		return
	}
	now := h.now()
	device, ok := h.checkSignature(w, id, tenantID, x, body, now)
	if !ok {
		return
	}
	// Who answered, as a hold's settled receipt names them: their email when
	// the session carries one, and their sign-in id.
	by := id.Email
	if by == "" {
		by = id.Subject
	}
	got, err := h.store.Answer(r.Context(), tenantID, x.ID, handoff.Status(body.Answer), by, id.Subject, device, now)
	switch {
	case errors.Is(err, handoff.ErrAnswered):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "already_answered",
			"detail": "this step was already answered, or it expired; nothing was changed", "handoff": handoffView(got)})
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "handoffs: answer failed", slog.String("tenant", tenantID), slog.String("handoff", x.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the answer was not kept"})
		return
	}
	handoff.Closed(h.alerts, got, now)
	h.receipt(r.Context(), got)
	writeJSON(w, http.StatusOK, handoffView(got))
}

// checkSignature applies a held call's rule to a hand-off's answer: a
// session bound to a phone's key answers only with that key's signature
// over this exact answer, so a stolen session token alone answers nothing.
// "Done" and "Skip it" grant no authority, so the device key signs them,
// never the approval key. A developer sign-in without a device key doesn't
// answer; the dashboard answers as it does a hold.
func (h *HandoffsHandler) checkSignature(w http.ResponseWriter, id auth.Identity, tenantID string, x handoff.Handoff, body handoffAnswerBody, now time.Time) (*handoff.Device, bool) {
	if len(id.DeviceKey) == 0 {
		if id.Method == auth.MethodDeveloper {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "device_required", "detail": "answer on your phone"})
			return nil, false
		}
		if body.Signature != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no_device_key",
				"detail": "this session has no device key to check a signature against; sign in from the phone to answer with one"})
			return nil, false
		}
		return nil, true
	}
	if body.Signature == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "signature_required",
			"detail": "this session belongs to a phone with a device key, so an answer needs that key's signature"})
		return nil, false
	}
	signed := auth.HandoffAnswer{TenantID: tenantID, HandoffID: x.ID, Answer: body.Answer, Version: x.Version, SignedAt: body.Signature.SignedAt}
	switch err := auth.VerifyHandoffAnswer(id.DeviceKey, signed, body.Signature.Value, now); {
	case errors.Is(err, auth.ErrAnswerSignedAt):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signed_at_out_of_range",
			"detail": "signed_at must be RFC 3339, within 5 minutes of KIFF's clock"})
		return nil, false
	case err != nil:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "signature_invalid",
			"detail": "the signature does not verify for this answer with this phone's key"})
		return nil, false
	}
	keyID := id.DeviceSessionID
	if id.DeviceID != "" {
		keyID = id.DeviceID
	}
	return &handoff.Device{Name: id.DeviceName, KeyID: keyID, PublicKey: id.DeviceKey,
		Message: string(signed.Message()), Signature: body.Signature.Value}, true
}

func (h *HandoffsHandler) check(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the taf that handed the step over names its read"})
		return
	}
	var body struct {
		// Operation is the runtime's kiff_operation_id for the read.
		Operation string `json:"operation"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Operation) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"operation": "<the operation id of the call that read the vendor>"}`})
		return
	}
	x, ok := h.load(w, r, id, tenantID)
	if !ok {
		return
	}
	if x.Status != handoff.Done || x.AnsweredAt == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "not_done", "detail": "only a step its owner answered done takes a check", "handoff": handoffView(x)})
		return
	}
	calls, err := h.store.Operations(r.Context(), tenantID, ownAgents(id.AgentID), []string{body.Operation})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the call couldn't be read"})
		return
	}
	c, found := calls[body.Operation]
	switch {
	case !found || !handoff.Owns(c.Agent, id.AgentID, mandate.TrialSubject):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "check_invalid", "detail": "that call is not one of this agent's"})
		return
	case !c.Sent():
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "check_invalid", "detail": "that call was not sent to the vendor"})
		return
	case c.CreatedAt.Before(*x.AnsweredAt):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "check_invalid", "detail": "that call was made before the owner answered"})
		return
	}
	got, err := h.store.Check(r.Context(), tenantID, x.ID, c.Proposal, h.now())
	switch {
	case errors.Is(err, handoff.ErrNotChecked):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "already_checked",
			"detail": "this step already has its check, or its receipt is settled", "handoff": handoffView(got)})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the check was not kept"})
		return
	}
	h.receipt(r.Context(), got)
	writeJSON(w, http.StatusOK, handoffView(got))
}
