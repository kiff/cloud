package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// ApprovalWindowHandler serves the approval window (kiff/tafs#494): one
// fingerprint, then taps for a few minutes.
//
//	POST /v1/me/window              open one: kiff.window.v1, signed by the approval key (PhoneApprovalRules)
//	POST /v1/me/window/{id}/close   close it: the app's own hand, device-signed (PhoneSignedRules)
//	GET  /v1/me/window              the open window, if any, with its words
//
// Inside an open window a routine action is signed with the device key and
// the window's id (auth.RequestMessageWindow); the route policy checks the
// window and restarts it. The window's end is the server's: expiry, its
// cap, sign-out, a revoked session or device, a halt, or Close.
type ApprovalWindowHandler struct {
	windows auth.WindowStore
	// passkeys finds a computer's own passkey, for a window it opens: only
	// one still registered to the device, not synced, counts
	// (auth.WindowOpenedByApprover). Nil, a passkey opens no window.
	passkeys auth.PasskeyStore
	logger   *slog.Logger
	now      func() time.Time
}

// NewApprovalWindowHandler serves windows from windows.
func NewApprovalWindowHandler(windows auth.WindowStore, logger *slog.Logger) *ApprovalWindowHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ApprovalWindowHandler{windows: windows, logger: logger, now: time.Now}
}

// WithPasskeys lets a computer's own passkey open a window, for a session
// that approves with it.
func (h *ApprovalWindowHandler) WithPasskeys(passkeys auth.PasskeyStore) *ApprovalWindowHandler {
	h.passkeys = passkeys
	return h
}

// Register attaches the routes to the authenticated mux.
func (h *ApprovalWindowHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/me/window", h.open)
	mux.HandleFunc("POST /v1/me/window/{id}/close", h.close)
	mux.HandleFunc("GET /v1/me/window", h.get)
}

// phone is the request's identity when it is a phone's own session with a
// device, or false once it has answered.
func (h *ApprovalWindowHandler) phone(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || len(id.DeviceKey) == 0 || id.DeviceID == "" || id.DeviceSessionID == "" || id.AgentID != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "device_session_required",
			"detail": "only a phone's own session, signed in from a device, opens or closes an approval window"})
		return auth.Identity{}, false
	}
	return id, true
}

func (h *ApprovalWindowHandler) open(w http.ResponseWriter, r *http.Request) {
	id, ok := h.phone(w, r)
	if !ok {
		return
	}
	// The route policy verified the signature; the window stands for a
	// fingerprint, so only the approval key's opens one: a phone's approval
	// key, or a computer's own passkey for a session that approves with it
	// (the route policy took its assertion, user verified, this device's, used
	// once). A session that signs with its device key opens nothing.
	with, signed := auth.SignedWithFrom(r.Context())
	byKey := len(id.ApprovalKey) > 0
	byPasskey := id.ApprovalMode == auth.ApprovalModePasskey && len(with.Passkey) > 0
	if !signed || !with.Critical() || !(byKey || byPasskey) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval_key_required",
			"detail": "an approval window opens only with the device's approval key or its own passkey, behind its fingerprint, face or PIN"})
		return
	}
	// A software-key computer's security key approves each request alone:
	// it opens no window.
	if id.SoftwareKeyPasskey() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "security_key_each_request",
			"detail": "this computer approves each request with its security key, one at a time: it opens no approval window"})
		return
	}
	openedBy := id.ApprovalKey
	if !byKey {
		openedBy = with.Passkey
	}
	var req auth.OpenWindow
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request",
			"detail": "the body is the kiff.window.v1 message: message, session, device, expires, nonce"})
		return
	}
	now := h.now()
	expires, err := req.Check(id, now)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_window", "detail": err.Error()})
		return
	}
	win := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: id.Tenant, Subject: id.Subject, DeviceID: id.DeviceID, SessionID: id.DeviceSessionID,
		OpenedAt: now, ExpiresAt: expires, CapAt: now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(openedBy)}
	// A window opens only for what can later use it: a passkey opens one
	// only when it's this computer's own, still registered, not synced. An
	// account passkey approves each request, never a window.
	if !byKey && !auth.WindowOpenedByApprover(r.Context(), h.passkeys, win, id.DeviceSessionID, nil, id.ApprovalMode) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "passkey_not_this_device",
			"detail": "an approval window opens only with this computer's own passkey, not one registered for the account"})
		return
	}
	if err := h.windows.Open(r.Context(), win); err != nil {
		h.logger.ErrorContext(r.Context(), "window: open failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "window_unavailable", "detail": "the window couldn't be kept; try again"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"window": windowView(win, now, replyLanguage(r))})
}

func (h *ApprovalWindowHandler) close(w http.ResponseWriter, r *http.Request) {
	id, ok := h.phone(w, r)
	if !ok {
		return
	}
	win, err := h.windows.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, auth.ErrWindowNotFound) || (err == nil && (win.DeviceID != id.DeviceID || win.Tenant != id.Tenant || win.Subject != id.Subject)) {
		// Another phone's window reads as none.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_window"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "window_unavailable"})
		return
	}
	now := h.now()
	if err := h.windows.Close(r.Context(), win.ID, now, auth.WindowClosedByApp); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "window_unavailable"})
		return
	}
	win, err = h.windows.Get(r.Context(), win.ID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "window_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"window": windowView(win, now, replyLanguage(r))})
}

func (h *ApprovalWindowHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := h.phone(w, r)
	if !ok {
		return
	}
	now := h.now()
	win, err := h.windows.OpenFor(r.Context(), id.DeviceID, now)
	if errors.Is(err, auth.ErrWindowNotFound) || (err == nil && (win.Tenant != id.Tenant || win.Subject != id.Subject)) {
		writeJSON(w, http.StatusOK, map[string]any{"window": nil, "words": windowWords(nil, now, replyLanguage(r))})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "window_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"window": windowView(win, now, replyLanguage(r))})
}

// windowView is a window as the app reads it, with KIFF's words for it.
func windowView(w auth.ApprovalWindow, now time.Time, lang string) map[string]any {
	v := map[string]any{"id": w.ID, "opened": w.OpenedAt.UTC().Format(time.RFC3339), "expires": w.ExpiresAt.UTC().Format(time.RFC3339),
		"cap": w.CapAt.UTC().Format(time.RFC3339), "open": w.OpenAt(now), "key": auth.KeyDevice, "words": windowWords(&w, now, lang)}
	if w.ClosedAt != nil {
		v["closed"], v["closed_by"] = w.ClosedAt.UTC().Format(time.RFC3339), w.ClosedBy
	}
	return v
}

// windowWords says, in lang, what the window means for the next tap: how
// long no prompt comes, or that the next approval asks for the fingerprint.
func windowWords(w *auth.ApprovalWindow, now time.Time, lang string) string {
	in := wordsIn(lang)
	if w == nil || !w.OpenAt(now) {
		return in.WindowNextApproval
	}
	left := w.ExpiresAt.Sub(now)
	if w.CapAt.Before(w.ExpiresAt) {
		left = w.CapAt.Sub(now)
	}
	minutes := int(left / time.Minute)
	switch {
	case left < time.Minute:
		return in.WindowAgainUnderMinute
	case minutes < 2:
		return in.WindowNoneNextMinute
	}
	return in.WindowNoneMinutes(minutes)
}
