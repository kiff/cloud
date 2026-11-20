package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/appsignin"
	"github.com/kiff/cloud/api/internal/auth"
)

// WithDeviceAppSignIn adds the tafs apps' token route (package appsignin's
// README): a code from signing in inside the app, redeemed with its verifier,
// mints the device's session the way the device flow's approve does, and
// records the terms the person ticked in the same step.
func WithDeviceAppSignIn(app *appsignin.Handler, consents appsignin.ConsentStore) DeviceAuthOption {
	return func(h *DeviceAuthHandler) { h.app, h.consents = app, consents }
}

// appTokenRequest is POST /v1/auth/app/token's body: the code's half, then
// exactly /v1/auth/device/start's fields.
type appTokenRequest struct {
	ClientID     string `json:"client_id"`
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
	TermsVersion string `json:"terms_version"`
	deviceFields
}

// appToken handles POST /v1/auth/app/token. Everything that can be checked
// without the code is checked first, so a refusal the app can fix (the terms
// changed, a field) leaves the code to try again within its minute. Then the
// code is spent, the session minted, and the consent recorded; a consent that
// isn't recorded undoes the session.
func (h *DeviceAuthHandler) appToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.app.On() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_client", "error_description": "signing in inside the app isn't on yet"})
		return
	}
	var req appTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "the body is JSON"})
		return
	}
	in, refusal := req.deviceFields.check()
	if refusal != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string(refusal))
		return
	}
	if strings.TrimSpace(req.TermsVersion) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "terms_version is the version the person ticked"})
		return
	}
	if req.TermsVersion != h.app.TermsVersion() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "terms_outdated", "terms_version": h.app.TermsVersion()})
		return
	}
	who, err := h.app.Redeem(r.Context(), req.ClientID, req.Code, req.CodeVerifier, req.RedirectURI)
	switch {
	case errors.Is(err, appsignin.ErrOff):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_client"})
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "the code is unknown, used, expired, or doesn't match"})
		return
	}
	id := auth.Identity{Subject: who.Subject, Tenant: who.Tenant, Email: who.Email, Method: auth.MethodSession}
	now := h.clock()
	m, ok := h.mintSession(w, r, id, in, "", now)
	if !ok {
		return
	}
	if err := h.consents.Record(r.Context(), appsignin.Consent{Tenant: id.Tenant, Subject: id.Subject, TermsVersion: req.TermsVersion,
		Client: appsignin.ClientID, DeviceID: m.sess.DeviceID, SessionID: m.sess.ID, AcceptedAt: now}); err != nil {
		h.undoMint(r, m, now)
		h.logger.ErrorContext(r.Context(), "app token: consent not recorded", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "temporarily_unavailable", "error_description": "could not finish signing in; start again"})
		return
	}
	h.finishMint(r, id, m)
	resp := deviceTokenResponse{AccessToken: m.token, TokenType: "Bearer", ExpiresIn: int(h.sessionTTL.Seconds()), Tenant: id.Tenant, Subject: id.Subject}
	if in.deviceKey != nil {
		resp.ApprovalMode = m.shownMode
	}
	writeJSON(w, http.StatusOK, resp)
}
