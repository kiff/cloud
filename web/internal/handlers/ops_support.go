package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

// Operator support-ticket triage. The operator reads the cross-tenant
// queue, opens a ticket, answers it, and sets its status. Data lives
// in apps/api behind the /v1/ops/support* gate; these handlers forward
// the operator's bearer and render the result on the shared ops shell.
// Plain server-rendered POST forms, no JS.

// support renders GET /ops/support — the triage queue, optionally
// filtered by ?status=.
func (h *OpsHandler) support(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	if h.api == nil {
		_ = pages.OpsUnavailable("Support", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/support", http.StatusFound)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	tickets, err := h.api.OpsSupportListBearer(r.Context(), bearer, status)
	if err != nil {
		h.renderError(w, r, "Support", chrome, err)
		return
	}
	_ = pages.OpsSupport(tickets, status, chrome).Render(r.Context(), w)
}

// supportDetail renders GET /ops/support/{id} — one ticket, its
// thread, and the reply + status forms.
func (h *OpsHandler) supportDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	id := r.PathValue("id")
	if h.api == nil {
		_ = pages.OpsUnavailable("Support", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/support", http.StatusFound)
		return
	}
	detail, err := h.api.OpsSupportDetailBearer(r.Context(), bearer, id)
	if err != nil {
		if errors.Is(err, apiclient.ErrReceiptNotFound) {
			http.NotFound(w, r)
			return
		}
		h.renderError(w, r, "Support", chrome, err)
		return
	}
	_ = pages.OpsSupportDetail(*detail, chrome).Render(r.Context(), w)
}

// supportReply handles POST /ops/support/{id}/reply — the operator
// answers. The API attributes the reply, moves the ticket to
// "answered", and emails the submitter. Redirects back to the ticket.
func (h *OpsHandler) supportReply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detailPath := "/ops/support/" + url.PathEscape(id)
	if bearer, ok := h.guardMutation(w, r); ok {
		body := strings.TrimSpace(r.FormValue("body"))
		if body == "" {
			// Nothing to send; bounce back without calling the API.
			http.Redirect(w, r, detailPath, http.StatusSeeOther)
			return
		}
		if err := h.api.OpsSupportReplyBearer(r.Context(), bearer, id, body); err != nil {
			h.logger.WarnContext(r.Context(), "ops console: support reply failed",
				slog.String("ticket", id), slog.String("err", err.Error()))
		}
	}
	http.Redirect(w, r, detailPath, http.StatusSeeOther)
}

// supportStatus handles POST /ops/support/{id}/status — set the
// ticket's status (e.g. in_progress, closed). Redirects back.
func (h *OpsHandler) supportStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detailPath := "/ops/support/" + url.PathEscape(id)
	if bearer, ok := h.guardMutation(w, r); ok {
		status := strings.TrimSpace(r.FormValue("status"))
		if status != "" {
			if err := h.api.OpsSupportStatusBearer(r.Context(), bearer, id, status); err != nil {
				h.logger.WarnContext(r.Context(), "ops console: support status failed",
					slog.String("ticket", id), slog.String("status", status), slog.String("err", err.Error()))
			}
		}
	}
	http.Redirect(w, r, detailPath, http.StatusSeeOther)
}
