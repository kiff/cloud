package codesignin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/region"
	"github.com/kiff/cloud/api/internal/words"
)

// StepUp is the recent sign-in check turning codes on needs.
type StepUp interface {
	Check(ctx context.Context, id auth.Identity) (int, error)
}

// Handler serves the routes. Without a service (no KIFF_SIGNIN_CODE_KEY),
// the web's routes are 404 and turning codes on is refused.
type Handler struct {
	svc    *Service
	stepUp StepUp
	logger *slog.Logger
	// fromBox reports whether a call comes from the box (the web); tests
	// set it.
	fromBox func(r *http.Request) bool
}

// NewHandler wires the routes; svc may be nil.
func NewHandler(svc *Service, stepUp StepUp, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, stepUp: stepUp, logger: logger, fromBox: func(r *http.Request) bool {
		_, in := region.ClientAddr(r)
		return in
	}}
}

// RegisterWeb mounts the web's two routes, outside the auth middleware: no
// one is signed in yet. They answer only calls from the box, the web, which
// passes the client address it trusts.
func (h *Handler) RegisterWeb(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/code/send", h.send)
	mux.HandleFunc("POST /v1/auth/code/verify", h.verify)
}

// RegisterAuthenticated mounts the person's setting, behind the auth
// middleware.
func (h *Handler) RegisterAuthenticated(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/sign-in", h.get)
	mux.HandleFunc("PUT /v1/me/sign-in", h.put)
}

type sendBody struct {
	Email   string `json:"email"`
	Flow    string `json:"flow"`
	IP      string `json:"ip"`
	Lang    string `json:"lang"`
	Browser string `json:"browser"`
	Country string `json:"country"`
}

func (h *Handler) web(w http.ResponseWriter, r *http.Request, into any) bool {
	// No one is signed in here: a call that carries a key or a session
	// isn't the web's, and is refused as any route refuses a credential it
	// doesn't take.
	if r.Header.Get("Authorization") != "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	if h.svc == nil || !h.fromBox(r) {
		http.NotFound(w, r)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(into); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return false
	}
	return true
}

func (h *Handler) send(w http.ResponseWriter, r *http.Request) {
	var b sendBody
	if !h.web(w, r, &b) {
		return
	}
	err := h.svc.Send(r.Context(), SendRequest{Email: b.Email, Flow: b.Flow, IP: b.IP, Lang: lang(b.Lang),
		Asked: Asked{Browser: cut(b.Browser, 40), Country: cut(b.Country, 2)}})
	if errors.Is(err, ErrBadRequest) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	// The same answer for every address.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent_if_on"})
}

type verifyBody struct {
	Email string `json:"email"`
	Flow  string `json:"flow"`
	Code  string `json:"code"`
	IP    string `json:"ip"`
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	var b verifyBody
	if !h.web(w, r, &b) {
		return
	}
	ticket, err := h.svc.Verify(r.Context(), VerifyRequest{Email: b.Email, Flow: b.Flow, Code: b.Code, IP: b.IP})
	switch {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"ticket": ticket})
	case errors.Is(err, ErrBadRequest):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
	case errors.Is(err, ErrSlowDown):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow_down"})
	case errors.Is(err, ErrWrong):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code_wrong"})
	case errors.Is(err, ErrSecondFactor):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "second_factor"})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
	}
}

// settingJSON is the person's setting as the apps and the web read it.
type settingJSON struct {
	EmailCode bool       `json:"email_code"`
	ChangedAt *time.Time `json:"changed_at,omitempty"`
	// Available is false when KIFF can't send codes (no key): the toggle
	// can't be turned on.
	Available bool `json:"available"`
}

func person(r *http.Request) (auth.Identity, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.APIKey || id.Subject == "" {
		return auth.Identity{}, false
	}
	return id, true
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := person(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	out := settingJSON{Available: h.svc != nil}
	if h.svc != nil {
		set, err := h.svc.Store.Setting(r.Context(), id.Subject)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		}
		out.EmailCode = set.EmailCode
		if !set.ChangedAt.IsZero() {
			out.ChangedAt = &set.ChangedAt
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	id, ok := person(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if id.ViewOnly() {
		// A view-only session (the browser extension's, a computer without an approval key) changes
		// no setting, neither on nor off: it reads, and the person changes it on their phone.
		writeJSON(w, http.StatusForbidden, auth.ViewOnlyRefusal())
		return
	}
	var b struct {
		EmailCode *bool `json:"email_code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&b); err != nil || b.EmailCode == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "message": `the body is {"email_code": true|false}`})
		return
	}
	if *b.EmailCode && len(id.DeviceKey) > 0 {
		// A phone's or computer's own session (a device key) only turns codes off. Turning them on
		// needs a recent sign-in that is not a code sign-in, which a device session never has: it is
		// done at kiff.dev, Settings › Sign-in.
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "turn_on_at_kiff", "message": "turning on signing in with a code is done at kiff.dev, in Settings › Sign-in, after a recent sign-in"})
		return
	}
	if *b.EmailCode {
		if h.svc == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "not_available", "message": "signing in with a code isn't available"})
			return
		}
		// On needs a recent sign-in, never one made with a code.
		if h.stepUp == nil {
			auth.WriteReauthRequired(w)
			return
		}
		if _, err := h.stepUp.Check(r.Context(), id); err != nil {
			auth.WriteReauthRequired(w)
			return
		}
		// A code never stands in for a second factor.
		if two, err := h.svc.People.SecondFactor(r.Context(), id.Subject); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
			return
		} else if two {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "second_factor", "message": "signing in with a code isn't available to a person who signs in with a second factor"})
			return
		}
	}
	if h.svc == nil {
		// Off with nothing kept: it is off.
		writeJSON(w, http.StatusOK, settingJSON{})
		return
	}
	set, err := h.svc.SetEmailCode(r.Context(), id.Subject, *b.EmailCode, words.CodeLangFromHeader(r.Header.Get("Accept-Language")))
	if err != nil {
		h.logger.WarnContext(r.Context(), "code sign-in: set failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, settingJSON{EmailCode: set.EmailCode, ChangedAt: &set.ChangedAt, Available: true})
}

func lang(s string) words.Lang { return words.CodeLang(s) }

func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
