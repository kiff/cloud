package handlers

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// agentSheet serves one of an agent's sheets: each is a full screen
// that posts to the route that already does the change.
func (h *DashboardHandler) agentSheet(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, bearer, ok := h.cardSession(w, r)
		if !ok {
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		if h.api == nil {
			http.Error(w, "apps/api is not configured", http.StatusServiceUnavailable)
			return
		}
		d := h.loadAgents(r.Context(), bearer)
		if d.loadErr != nil {
			_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "This agent could not be loaded."})
			http.Redirect(w, r, "/agents", http.StatusSeeOther)
			return
		}
		flash, _ := h.sessions.ReadFlash(w, r)
		v := pages.AgentSheetView{Kind: kind, Flash: convertFlash(flash)}
		found := false
		for _, a := range d.summaries() {
			if a.ID == id {
				v.Agent, found = a, true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		switch kind {
		case "gateway":
			if h.gateway == nil {
				http.NotFound(w, r)
				return
			}
			v.GatewayURL = h.publicGatewayURL()
		case "key-new":
			// A kiff-guard key needs only the API; the gateway choice is
			// offered when this deployment has a gateway.
			if h.gateway != nil {
				v.GatewayURL = h.publicGatewayURL()
			}
		case "link":
			if links, err := h.api.ConnectLinksBearer(r.Context(), bearer, id); err != nil {
				h.logger.WarnContext(r.Context(), "agents: connect links failed", slog.String("err", err.Error()))
				v.ConnectLinksError = "Connect links could not be loaded."
			} else {
				v.ConnectLinks = links
			}
		case "key", "key-revoke":
			keyID := r.PathValue("key")
			kfound := false
			for _, k := range v.Agent.Keys {
				if k.ID == keyID {
					v.Key, kfound = k, true
				}
			}
			if !kfound {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		_ = pages.AgentSheet(v).Render(r.Context(), w)
	}
}

// createAgentKeyOfKind is the New key sheet's answer: a gateway key for
// Claude Code or Codex, or a kiff-guard key.
func (h *DashboardHandler) createAgentKeyOfKind(w http.ResponseWriter, r *http.Request) {
	switch r.PostFormValue("kind") {
	case "gateway":
		h.createGatewayKey(w, r)
	case "guard":
		h.createAgentKeyForAgent(w, r)
	default:
		if _, _, ok := h.cardSession(w, r); !ok {
			return
		}
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "Choose where the key is used."})
		http.Redirect(w, r, "/agents/"+url.PathEscape(strings.TrimSpace(r.PathValue("id")))+"/keys/new", http.StatusSeeOther)
	}
}
