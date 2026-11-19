package handlers

import (
	"errors"
	"net/http"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/session"
)

func (h *DashboardHandler) setTimeZone(w http.ResponseWriter, r *http.Request) {
	const back = "/dashboard/settings?tab=account"
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	fail := func(msg string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
	if h.api == nil {
		fail("The API is not configured.")
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	switch err := h.api.SetTimeZoneBearer(r.Context(), bearer, r.PostFormValue("time_zone")); {
	case errors.Is(err, apiclient.ErrForbidden):
		fail("Only the account’s owner or an admin can change its time zone.")
		return
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	case err != nil:
		fail("Time zone could not be saved. Use a named time zone such as Europe/Paris and try again.")
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashNotificationSaved, Message: "Time zone saved. New Cards will use it; existing Cards keep their day boundary."})
	http.Redirect(w, r, back, http.StatusSeeOther)
}
