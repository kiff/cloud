package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// OAuthDeviceHandler is the approval page of the gateway-OAuth design's
// device flow at /oauth/device: a CLI or agent that cannot receive a
// redirect prints a code, and a signed-in admin or editor checks it and
// allows the agent. It only ever connects an agent that can only ask.
// Signing a device in as yourself is the separate page at /auth/device.
type OAuthDeviceHandler struct {
	api      *apiclient.Client
	sessions *session.Manager
	bearers  BearerSource
	logger   *slog.Logger
}

// NewOAuthDeviceHandler wires the handler.
func NewOAuthDeviceHandler(api *apiclient.Client, sessions *session.Manager, bearers BearerSource, logger *slog.Logger) *OAuthDeviceHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &OAuthDeviceHandler{api: api, sessions: sessions, bearers: bearers, logger: logger}
}

// Register attaches the routes.
func (h *OAuthDeviceHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth/device", h.show)
	mux.HandleFunc("POST /oauth/device", h.answer)
}

func deviceSignIn(code string) string {
	next := "/oauth/device"
	if code != "" {
		next += "?code=" + url.QueryEscape(code)
	}
	return "/sign-in?next=" + url.QueryEscape(next)
}

func (h *OAuthDeviceHandler) show(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, strings.TrimSpace(r.URL.Query().Get("code")), "")
}

func (h *OAuthDeviceHandler) render(w http.ResponseWriter, r *http.Request, code, errMsg string) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, deviceSignIn(code), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if code == "" || h.api == nil {
		v := pages.OAuthConsentView{AskCode: true, ErrMsg: errMsg}
		if h.api == nil {
			v = pages.OAuthConsentView{Gone: "Connecting apps is not available on this deployment."}
		}
		_ = pages.OAuthConsent(v).Render(r.Context(), w)
		return
	}
	bearer, err := h.bearer(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, deviceSignIn(code), http.StatusFound)
		return
	}
	info, err := h.api.DeviceInfoBearer(r.Context(), bearer, code)
	switch {
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, deviceSignIn(code), http.StatusFound)
		return
	case err != nil:
		_ = pages.OAuthConsent(pages.OAuthConsentView{AskCode: true,
			ErrMsg: "That code is unknown, already used or expired. Check it, or start again in your terminal."}).Render(r.Context(), w)
		return
	}
	v := pages.OAuthConsentView{ClientName: info.ClientName, UserCode: info.UserCode, CanConsent: info.CanConsent,
		LinkAgent: info.AgentID, WrongAccount: info.WrongAccount, ErrMsg: errMsg}
	if info.CanConsent && info.AgentID == "" {
		v.NewName, v.Previous = info.ClientName, info.PreviousAgentID
		if agents, err := h.api.ListAgentsBearer(r.Context(), bearer); err == nil {
			var all []string
			for _, a := range agents {
				if a.ArchivedAt == nil {
					all = append(all, a.ID)
				}
			}
			pageAgents(&v, all, r.URL.Query())
		} else {
			h.logger.WarnContext(r.Context(), "oauth device: list agents", slog.String("err", err.Error()))
		}
	}
	if info.CanConsent && !info.WrongAccount {
		v.AgentCards, v.CardsUnknown = agentCards(r.Context(), h.api, h.logger, bearer)
	}
	_ = pages.OAuthConsent(v).Render(r.Context(), w)
}

func (h *OAuthDeviceHandler) answer(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.PostFormValue("user_code"))
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, deviceSignIn(code), http.StatusFound)
		return
	}
	if h.api == nil {
		h.render(w, r, code, "")
		return
	}
	bearer, err := h.bearer(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, deviceSignIn(code), http.StatusFound)
		return
	}
	done := func(msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.OAuthConsent(pages.OAuthConsentView{Done: msg}).Render(r.Context(), w)
	}
	if r.PostFormValue("action") == "deny" {
		if err := h.api.DeviceDenyBearer(r.Context(), bearer, code); err != nil {
			h.render(w, r, code, "The code could not be declined: it may have expired or been answered already.")
			return
		}
		done("Declined")
		return
	}
	agentID, newAgent, msg := consentChoice(r)
	if msg != "" {
		h.render(w, r, code, msg)
		return
	}
	switch err := h.api.DeviceApproveBearer(r.Context(), bearer, code, agentID, newAgent); {
	case errors.Is(err, apiclient.ErrForbidden):
		h.render(w, r, code, "Only an admin or editor of this account can connect an agent.")
	case errors.Is(err, apiclient.ErrConflict) && newAgent != "":
		h.render(w, r, code, "This account already has an agent named "+newAgent+". Choose it under An existing agent, or pick another name.")
	case err != nil:
		h.logger.WarnContext(r.Context(), "oauth device approve", slog.String("err", err.Error()))
		h.render(w, r, code, "The agent could not be connected: the code or link may have expired, or been answered already.")
	default:
		done("Connected")
	}
}

func (h *OAuthDeviceHandler) bearer(r *http.Request, s session.Session) (string, error) {
	if h.bearers != nil {
		return h.bearers.BearerFor(r.Context(), s)
	}
	return s.Bearer, nil
}
