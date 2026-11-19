package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/session"
)

// attentionQueue answers GET /dashboard/attention, which Needs you took
// in: domain approvals wait in Waiting, blocked calls in Blocked.
func (h *DashboardHandler) attentionQueue(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/needs-you", http.StatusSeeOther)
}

// attentionBack is where a work item's action returns: the Needs you page
// it was taken from, else Needs you.
func attentionBack(raw string) string {
	if strings.HasPrefix(raw, "/needs-you") && !strings.HasPrefix(raw, "/needs-you/") && !strings.Contains(raw, "//") && !strings.ContainsAny(raw, "\\\r\n") {
		return raw
	}
	return "/needs-you"
}

func (h *DashboardHandler) attentionAction(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	if h.api == nil {
		http.Error(w, "Attention unavailable", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	revision, err := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	action := r.PathValue("action")
	if err != nil || revision < 1 || (action != "claim" && action != "release" && action != "resolve") {
		http.Error(w, "Invalid action", 400)
		return
	}
	back := attentionBack(r.FormValue("back"))
	if err := h.api.ActAttentionBearer(r.Context(), bearer, r.PathValue("id"), action, revision); err != nil {
		msg := "This could not be changed. Try again shortly."
		if errors.Is(err, apiclient.ErrConflict) {
			msg = "Someone else changed this first (for example, another person took it). Here is where it stands now."
		}
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
