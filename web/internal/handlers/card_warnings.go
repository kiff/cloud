package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/session"
)

func (h *DashboardHandler) cardWarningSettings(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	if !components.Can(r.Context(), "governance.manage") {
		http.Error(w, "Admin authority is required", 403)
		return
	}
	back := "/cards/" + url.PathEscape(r.PathValue("id")) + "/warnings"
	fail := func(message string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: message})
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
	if !toolsForm(w, r) {
		return
	}
	if h.api == nil {
		fail("Budget warning settings are temporarily unavailable. Please try again.")
		return
	}
	revision, err := strconv.ParseInt(r.PostFormValue("revision"), 10, 64)
	readNumber := func(name string, min, max int) (int, bool) {
		n, e := strconv.Atoi(r.PostFormValue(name))
		return n, e == nil && n >= min && n <= max
	}
	runway, ok1 := readNumber("runway_percent", 1, 100)
	unusual, ok2 := readNumber("unusual_multiplier", 2, 100)
	burst, ok3 := readNumber("burst_percent", 1, 100)
	if err != nil || revision < 0 || !ok1 || !ok2 || !ok3 {
		fail("Enter percentages from 1 to 100 and an unusual multiplier from 2 to 100.")
		return
	}
	c := apiclient.WarningConfig{Revision: revision, Settings: apiclient.WarningSettings{Runway: r.PostFormValue("runway") == "on", Spent: r.PostFormValue("spent") == "on", Unusual: r.PostFormValue("unusual") == "on", Burst: r.PostFormValue("burst") == "on", RunwayPercent: runway, UnusualMultiplier: unusual, BurstPercent: burst}}
	err = h.api.SetCardWarningSettingsBearer(r.Context(), bearer, r.PathValue("id"), c)
	message := "Budget warnings saved."
	kind := session.FlashNotificationSaved
	if errors.Is(err, apiclient.ErrConflict) {
		kind = session.FlashError
		message = "Someone changed the warning settings first. Reload this Card and review the current settings."
	} else if err != nil {
		kind = session.FlashError
		message = "Budget warning settings could not be saved. Please try again."
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: kind, Message: message})
	http.Redirect(w, r, back, http.StatusSeeOther)
}
