package handlers

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// cardSheet serves one change to a Card on its own screen: GET
// /cards/{id}/holder, /revoke, /approvers/new and
// /approvers/{grant}/revoke. Each only reads; the form it shows posts to
// the route that already makes the change.
func (h *DashboardHandler) cardSheet(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, bearer, ok := h.cardSession(w, r)
		if !ok {
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		back := "/cards/" + url.PathEscape(id)
		fail := func(msg string) {
			_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
			http.Redirect(w, r, back, http.StatusSeeOther)
		}
		if h.api == nil {
			fail("apps/api is not configured.")
			return
		}
		cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
		if err != nil {
			fail("This Card could not be read.")
			return
		}
		v := pages.CardSheetView{Kind: kind, Now: time.Now()}
		found := false
		for _, m := range cards {
			if m.ID == id {
				v.Card, found = m, true
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		if v.Card.RevokedAt != nil {
			fail("This Card is revoked, so it cannot be changed.")
			return
		}
		switch kind {
		case "warnings":
			back += "/settings"
			if !components.Can(r.Context(), "governance.manage") {
				http.Error(w, "Admin authority is required", http.StatusForbidden)
				return
			}
			flash, _ := h.sessions.ReadFlash(w, r)
			v.Flash = convertFlash(flash)
			v.WarningConfig, err = h.api.CardWarningSettingsBearer(r.Context(), bearer, id)
			if err != nil {
				v.WarningError = "Budget warning settings could not be read. Reload and try again."
			}
		case "only-for":
			back += "/settings"
			if !pages.IsToolCard(v.Card) {
				fail("Only a tool Card can be limited here.")
				return
			}
			flash, _ := h.sessions.ReadFlash(w, r)
			v.Flash = convertFlash(flash)
			h.fillOnlyFor(r.Context(), bearer, &v)
		case "holder":
			// Offered by name, so a typo does not share the Card with an
			// agent nobody runs. Best effort: without them the sheet
			// takes a typed id.
			if list, err := h.api.ListAgentsBearer(r.Context(), bearer); err == nil {
				for _, a := range list {
					if a.ArchivedAt == nil {
						v.Agents = append(v.Agents, a.ID)
					}
				}
			}
		case "approver-new", "approver-revoke":
			back += "/approvers"
			me, err := h.api.GetMeBearer(r.Context(), bearer)
			if err != nil || me.MemberRole != "admin" {
				fail("Only an admin of this account can manage approvers.")
				return
			}
			if kind == "approver-new" {
				v.Scopes = h.approverScopes(r.Context(), bearer, v.Card, h.cardLimits(r.Context(), bearer, id))
				if !v.Card.ComesHome() || len(v.Scopes) == 0 {
					fail("This Card has nothing an approver could answer.")
					return
				}
				break
			}
			grants, err := h.api.ListApproversBearer(r.Context(), bearer, id)
			if err != nil {
				fail("The approvers could not be read. Reload and try again.")
				return
			}
			if v.Grant, found = findGrant(grants, r.PathValue("grant")); !found {
				http.NotFound(w, r)
				return
			}
		}
		_ = pages.CardSheet(v).Render(r.Context(), w)
	}
}

func findGrant(gs []apiclient.ApproverGrant, id string) (apiclient.ApproverGrant, bool) {
	for _, g := range gs {
		if g.ID == id {
			return g, true
		}
	}
	return apiclient.ApproverGrant{}, false
}
