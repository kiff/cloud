package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

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
	ip := appClientIP(r)
	if h.appFails.blocked(ip, h.clock()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow_down", "error_description": "too many failed sign-ins from this address; wait, then start again"})
		return
	}
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
	var replay *appsignin.Replay
	switch {
	case errors.Is(err, appsignin.ErrOff):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_client"})
		return
	case errors.As(err, &replay):
		// A spent code used again ends what it made (RFC 6749 §4.1.2).
		if replay.SessionID != "" {
			now := h.clock()
			_ = h.sessions.Revoke(r.Context(), replay.SessionID, now)
			h.revokeDevice(r, replay.DeviceID, now)
			h.logger.WarnContext(r.Context(), "app token: a spent code was used again; its session is ended", slog.String("session", replay.SessionID))
		}
		h.appFails.add(ip, h.clock())
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "the code is unknown, used, expired, or doesn't match"})
		return
	case err != nil:
		h.appFails.add(ip, h.clock())
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
	h.app.Minted(req.Code, m.sess.ID, m.created)
	h.finishMint(r, id, m)
	resp := deviceTokenResponse{AccessToken: m.token, TokenType: "Bearer", ExpiresIn: int(h.sessionTTL.Seconds()), Tenant: id.Tenant, Subject: id.Subject}
	if in.deviceKey != nil {
		resp.ApprovalMode = m.shownMode
	}
	writeJSON(w, http.StatusOK, resp)
}

// appFailLimit failed token requests per address in appFailWindow, then slow_down.
const (
	appFailLimit  = 20
	appFailWindow = 10 * time.Minute
)

// appFailures counts failed token requests per address.
type appFailures struct {
	mu sync.Mutex
	by map[string][]time.Time
}

func (f *appFailures) recent(ip string, now time.Time) []time.Time {
	kept := f.by[ip][:0]
	for _, t := range f.by[ip] {
		if now.Sub(t) < appFailWindow {
			kept = append(kept, t)
		}
	}
	return kept
}

func (f *appFailures) blocked(ip string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recent(ip, now)) >= appFailLimit
}

func (f *appFailures) add(ip string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.by == nil {
		f.by = map[string][]time.Time{}
	}
	if len(f.by) > 10000 {
		for k := range f.by {
			if len(f.recent(k, now)) == 0 {
				delete(f.by, k)
			}
		}
	}
	f.by[ip] = append(f.recent(ip, now), now)
}

// appClientIP is the caller's address: the last X-Forwarded-For entry, the one
// KIFF's proxy appended, else the connection's.
func appClientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		parts := strings.Split(f, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
