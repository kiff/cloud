package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// toolSheets are the sheets a connected tool's Settings rows open.
var toolSheets = map[string]bool{"resource": true, "release": true, "key": true, "credential": true, "remove": true}

// findTool reads one connected tool, or answers the request itself and
// returns false.
func (h *DashboardHandler) findTool(w http.ResponseWriter, r *http.Request, bearer string) (gatewayclient.Connection, bool) {
	if h.gateway == nil {
		http.Redirect(w, r, "/tools", http.StatusSeeOther)
		return gatewayclient.Connection{}, false
	}
	conns, err := h.gateway.List(r.Context(), bearer)
	if err != nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: gatewayMessage(err)})
		http.Redirect(w, r, "/tools", http.StatusSeeOther)
		return gatewayclient.Connection{}, false
	}
	name := r.PathValue("name")
	for _, c := range conns {
		if c.Name == name {
			return c, true
		}
	}
	http.NotFound(w, r)
	return gatewayclient.Connection{}, false
}

// toolDetail is a connected tool's page: Overview, or Settings.
func (h *DashboardHandler) toolDetail(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, bearer, ok := h.toolsBearer(w, r)
		if !ok {
			return
		}
		t, ok := h.findTool(w, r, bearer)
		if !ok {
			return
		}
		flash, _ := h.sessions.ReadFlash(w, r)
		v := pages.ToolDetailView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Tool: t, Tab: tab, Flash: convertFlash(flash)}
		if tab == "overview" {
			v.Cards, v.CardsError = h.toolCardsOf(r.Context(), bearer, t.Name)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.ToolDetail(v).Render(r.Context(), w)
	}
}

func (h *DashboardHandler) toolCardsOf(ctx context.Context, bearer, tool string) ([]apiclient.Mandate, string) {
	if h.api == nil {
		return nil, ""
	}
	cards, err := h.api.ListMandatesBearer(ctx, bearer)
	if err != nil {
		return nil, "The Cards on this tool could not be read."
	}
	return pages.ToolCards(cards, tool, time.Now()), ""
}

// toolSheet serves one of a connected tool's sheets.
func (h *DashboardHandler) toolSheet(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.toolsBearer(w, r)
	if !ok {
		return
	}
	kind := r.PathValue("sheet")
	if !toolSheets[kind] {
		http.NotFound(w, r)
		return
	}
	t, ok := h.findTool(w, r, bearer)
	if !ok {
		return
	}
	if (kind == "release" && t.IdempotencyArg == "") || (kind == "credential" && t.Mode == "verify") {
		http.Redirect(w, r, "/tools/"+url.PathEscape(t.Name)+"/settings", http.StatusSeeOther)
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.ToolSheetView{Kind: kind, Tool: t, Flash: convertFlash(flash)}
	if kind == "resource" && t.ResourceArg != "" {
		cards, _ := h.toolCardsOf(r.Context(), bearer, t.Name)
		v.Scoped = pages.ScopedCards(cards, t)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pages.ToolSheet(v).Render(r.Context(), w)
}

// toolsSetSettings changes what KIFF checks on a connected tool without
// reconnecting it: one setting per sheet.
func (h *DashboardHandler) toolsSetSettings(w http.ResponseWriter, r *http.Request) {
	h.toolsAct(w, r, func(ctx context.Context, bearer, name string) (session.Flash, error) {
		var change gatewayclient.SettingsChange
		var msg string
		switch r.PostFormValue("setting") {
		case "resource":
			v := strings.TrimSpace(r.PostFormValue("resource_argument"))
			change.ResourceArg = &v
			msg = "Each call to " + name + " is now on " + v + ". Its Cards can name which values they cover, under Only for."
			if v == "" {
				msg = name + " no longer names what each call is on. Its Cards cover any call."
			}
		case "release":
			v := r.PostFormValue("outcome_contract")
			change.OutcomeContract = &v
			msg = "A call to " + name + " that certainly did nothing now gives its Card the room back."
			if v == "" {
				msg = "Every allowed call to " + name + " now counts toward its Card, whatever happened."
			}
		default:
			return session.Flash{Kind: session.FlashError, Message: "Choose a setting to change."}, nil
		}
		if err := h.gateway.SetSettings(ctx, bearer, name, change); err != nil {
			return session.Flash{}, err
		}
		h.logger.InfoContext(ctx, "tools: settings changed", slog.String("tool", name), slog.String("setting", r.PostFormValue("setting")))
		return session.Flash{Kind: session.FlashToolsChanged, Message: msg}, nil
	})
}
