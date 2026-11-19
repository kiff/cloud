package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// OAuthConsentHandler is the consent page of the gateway-OAuth design at
// /oauth/consent: an app (Claude, ChatGPT, Claude Code…) asks to connect
// an agent, and a signed-in admin or editor picks or creates the agent.
// KIFF then mints that agent's credential, which can only ask; the
// person's own authority never reaches the app. It sits behind
// RequireSession, so a visitor who is not signed in goes through sign-in
// first and returns.
type OAuthConsentHandler struct {
	api      *apiclient.Client
	sessions *session.Manager
	bearers  BearerSource
	logger   *slog.Logger
}

// NewOAuthConsentHandler wires the handler.
func NewOAuthConsentHandler(api *apiclient.Client, sessions *session.Manager, bearers BearerSource, logger *slog.Logger) *OAuthConsentHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &OAuthConsentHandler{api: api, sessions: sessions, bearers: bearers, logger: logger}
}

// Register attaches the routes.
func (h *OAuthConsentHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth/consent", h.show)
	mux.HandleFunc("POST /oauth/consent", h.answer)
}

func signInFor(requestID string) string {
	return "/sign-in?next=" + url.QueryEscape("/oauth/consent?request="+url.QueryEscape(requestID))
}

func (h *OAuthConsentHandler) show(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.URL.Query().Get("request"))
	h.render(w, r, requestID, "")
}

// render shows the request with the account's agents, or why it cannot.
func (h *OAuthConsentHandler) render(w http.ResponseWriter, r *http.Request, requestID, errMsg string) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, signInFor(requestID), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := pages.OAuthConsentView{RequestID: requestID, ErrMsg: errMsg}
	if h.api == nil {
		view.Gone = "Connecting apps is not available on this deployment."
		_ = pages.OAuthConsent(view).Render(r.Context(), w)
		return
	}
	bearer, err := h.bearer(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, signInFor(requestID), http.StatusFound)
		return
	}
	req, err := h.api.OAuthRequestBearer(r.Context(), bearer, requestID)
	switch {
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, signInFor(requestID), http.StatusFound)
		return
	case err != nil:
		view.Gone = "This connection request is unknown or has expired. Start again from your app."
		_ = pages.OAuthConsent(view).Render(r.Context(), w)
		return
	}
	view.ClientName, view.RedirectHost, view.Verified, view.CanConsent = req.ClientName, req.RedirectHost, req.Verified, req.CanConsent
	view.RedirectLocal = isLoopbackHost(req.RedirectHost)
	view.LinkAgent, view.WrongAccount = req.AgentID, req.WrongAccount
	// On the generic URL: offer a new agent for this app by default, and
	// the account's agents to search when the person means one of them.
	if req.CanConsent && req.AgentID == "" {
		view.NewName = req.ClientName
		view.Previous = req.PreviousAgentID
		agents, err := h.api.ListAgentsBearer(r.Context(), bearer)
		if err != nil {
			h.logger.WarnContext(r.Context(), "oauth consent: list agents", slog.String("err", err.Error()))
		}
		var all []string
		for _, a := range agents {
			if a.ArchivedAt == nil {
				all = append(all, a.ID)
			}
		}
		pageAgents(&view, all, r.URL.Query())
	}
	// The Card that would apply, shown before the person allows: the
	// link's agent, or each agent they can choose.
	if req.CanConsent && !req.WrongAccount {
		view.AgentCards, view.CardsUnknown = agentCards(r.Context(), h.api, h.logger, bearer)
	}
	_ = pages.OAuthConsent(view).Render(r.Context(), w)
}

// pageAgents fills the view's page of agent choices from all of the
// account's agents: the agent the app connected as before first, then the
// rest; filtered by q (a case-insensitive substring of the name); and cut
// to page (1-based). It only narrows what is shown; who may be connected
// is still decided by the API.
func pageAgents(v *pages.OAuthConsentView, all []string, query url.Values) {
	ordered := make([]string, 0, len(all))
	for _, a := range all {
		if a == v.Previous {
			ordered = append(ordered, a)
		}
	}
	for _, a := range all {
		if a != v.Previous {
			ordered = append(ordered, a)
		}
	}
	q := strings.TrimSpace(query.Get("q"))
	// At most 100 characters, cut on a character boundary.
	if r := []rune(q); len(r) > 100 {
		q = string(r[:100])
	}
	matches := ordered
	if q != "" {
		matches = nil
		for _, a := range ordered {
			if strings.Contains(strings.ToLower(a), strings.ToLower(q)) {
				matches = append(matches, a)
			}
		}
	}
	pageCount := (len(matches) + pages.ConsentAgentsPerPage - 1) / pages.ConsentAgentsPerPage
	if pageCount == 0 {
		pageCount = 1
	}
	page, _ := strconv.Atoi(query.Get("page"))
	if page < 1 {
		page = 1
	}
	if page > pageCount {
		page = pageCount
	}
	from := (page - 1) * pages.ConsentAgentsPerPage
	to := min(from+pages.ConsentAgentsPerPage, len(matches))
	v.Agents = matches[from:to]
	v.AgentQuery, v.AgentCount, v.AgentMatches = q, len(all), len(matches)
	v.AgentPage, v.AgentPages = page, pageCount
}

// consentChoice reads who the person chose to connect as on the generic
// URL: choice "new" with new_name, or "agent:<id>" for an existing agent.
// "existing" with a typed name is still read, for a page rendered before
// agents became choices. msg says what is missing. The generic form marks
// itself with choosing=1, so posting it with no choice is refused here
// rather than reaching the API; through a connect link there is no choice
// and nothing is missing.
func consentChoice(r *http.Request) (agentID, newAgent, msg string) {
	choice := r.PostFormValue("choice")
	switch {
	case choice == "" && r.PostFormValue("choosing") == "1":
		return "", "", "Choose who to connect as: a new agent or one of your agents."
	case choice == "new":
		if newAgent = strings.TrimSpace(r.PostFormValue("new_name")); newAgent == "" {
			return "", "", "Give the new agent a name."
		}
	case strings.HasPrefix(choice, "agent:"):
		if agentID = strings.TrimSpace(strings.TrimPrefix(choice, "agent:")); agentID == "" {
			return "", "", "Pick the agent to connect."
		}
	case choice == "existing":
		if agentID = strings.TrimSpace(r.PostFormValue("existing")); agentID == "" {
			return "", "", "Pick the agent to connect."
		}
	}
	return agentID, newAgent, ""
}

// agentCards are the account's Cards in force that apply to calls through
// the gateway, by holder, for the consent and device pages: those with no
// domain or the gateway's, matched as the API matches them (a Card for
// another domain never covers a gateway call). unknown is true when they
// could not be read.
func agentCards(ctx context.Context, api *apiclient.Client, logger *slog.Logger, bearer string) (cards map[string][]pages.ConsentCard, unknown bool) {
	mandates, err := api.ListMandatesBearer(ctx, bearer)
	if err != nil {
		logger.WarnContext(ctx, "oauth consent: list cards", slog.String("err", err.Error()))
		return nil, true
	}
	out := map[string][]pages.ConsentCard{}
	for _, m := range mandates {
		if !m.Active || m.ArchivedAt != nil || m.RevokedAt != nil {
			continue
		}
		if m.Domain != "" && !strings.EqualFold(m.Domain, gatewayDomain) {
			continue
		}
		c := pages.ConsentCard{ID: m.ID, Environment: m.Environment, ComesHome: m.ComesHome()}
		for _, g := range m.Grants {
			c.Actions = append(c.Actions, g.Action)
		}
		for _, holder := range m.Holders() {
			out[holder] = append(out[holder], c)
		}
	}
	return out, false
}

func (h *OAuthConsentHandler) answer(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.PostFormValue("request"))
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, signInFor(requestID), http.StatusFound)
		return
	}
	if h.api == nil {
		h.render(w, r, requestID, "")
		return
	}
	bearer, err := h.bearer(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, signInFor(requestID), http.StatusFound)
		return
	}
	if r.PostFormValue("action") == "deny" {
		to, err := h.api.OAuthDenyBearer(r.Context(), bearer, requestID)
		if err != nil {
			h.render(w, r, requestID, "The request could not be declined: it may have expired or been answered already.")
			return
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	// Through a connect link the API uses the link's agent and ignores
	// these. On the generic URL the person chose a new agent (created by
	// the API) or an existing one.
	agentID, newAgent, msg := consentChoice(r)
	if msg != "" {
		h.render(w, r, requestID, msg)
		return
	}
	to, err := h.api.OAuthConsentBearer(r.Context(), bearer, requestID, agentID, newAgent)
	switch {
	case errors.Is(err, apiclient.ErrForbidden):
		h.render(w, r, requestID, "Only an admin or editor of this account can connect an agent.")
		return
	case errors.Is(err, apiclient.ErrConflict) && newAgent != "":
		h.render(w, r, requestID, "This account already has an agent named "+newAgent+". Choose it under An existing agent, or pick another name.")
		return
	case err != nil:
		h.logger.WarnContext(r.Context(), "oauth consent", slog.String("err", err.Error()))
		h.render(w, r, requestID, "The agent could not be connected: the request or link may have expired, or been answered already. Start again from your app.")
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (h *OAuthConsentHandler) bearer(r *http.Request, s session.Session) (string, error) {
	if h.bearers != nil {
		return h.bearers.BearerFor(r.Context(), s)
	}
	return s.Bearer, nil
}

// isLoopbackHost reports whether host (host[:port]) is this computer.
func isLoopbackHost(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.HasSuffix(h, "]") {
		h = h[:i]
	}
	h = strings.Trim(h, "[]")
	return h == "127.0.0.1" || h == "::1" || h == "localhost"
}
