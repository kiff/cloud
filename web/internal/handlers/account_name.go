package handlers

import (
	"errors"
	"net/http"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/session"
)

// setAccountName names the account, from Settings: the name people see in
// invitations, the account switcher and the app.
func (h *DashboardHandler) setAccountName(w http.ResponseWriter, r *http.Request) {
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
	switch err := h.api.SetAccountNameBearer(r.Context(), bearer, r.PostFormValue("name")); {
	case errors.Is(err, apiclient.ErrForbidden):
		fail("Only the account’s owner or an admin can name it.")
		return
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	case err != nil:
		fail("The name could not be saved. Use 1 to 80 characters, on one line.")
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashNotificationSaved, Message: "Account name saved."})
	http.Redirect(w, r, back, http.StatusSeeOther)
}
