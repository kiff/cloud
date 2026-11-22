package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

// Operator support-ticket triage. Cross-tenant by design —
// operators see every tenant's tickets — so these routes sit behind
// the /v1/ops gate (allow-list re-checked in Register's h.gate). The
// tenant-facing counterpart is SupportHandler (/v1/me/support/*).

const opsSupportMaxBody = 16 * 1024

// listSupport serves GET /v1/ops/support[?status=]: the triage queue,
// most-recently-updated first, optionally filtered by status.
func (h *OpsHandler) listSupport(w http.ResponseWriter, r *http.Request) {
	status := support.Status(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && !support.ValidStatus(status) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown status filter"})
		return
	}
	tickets, err := h.support.ListAll(r.Context(), status)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "ops support: list failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list tickets failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tickets": tickets})
}

// supportDetail serves GET /v1/ops/support/{id}: one ticket + thread,
// any tenant.
func (h *OpsHandler) supportDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	t, err := h.support.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, support.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ticket not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "ops support: get failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "get ticket failed"})
		return
	}
	msgs, err := h.support.ListMessages(r.Context(), id)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "ops support: list messages failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load thread failed"})
		return
	}
	writeJSON(w, http.StatusOK, ticketDetailResponse{Ticket: t, Messages: msgs})
}

// supportReply serves POST /v1/ops/support/{id}/reply: the operator
// answers. The reply is attributed to the operator subject, and the
// ticket moves to "answered" (awaiting the tenant). The close-the-loop
// email to the submitter lands in the next slice.
func (h *OpsHandler) supportReply(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	t, err := h.support.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, support.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ticket not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "ops support: reply lookup failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reply failed"})
		return
	}

	var req struct {
		Body string `json:"body"`
	}
	if !h.readOpsSupportJSON(w, r, &req) {
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is required"})
		return
	}
	if len(body) > maxBodyLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body exceeds the allowed length"})
		return
	}

	opSubject := ""
	if idn, err := auth.IdentityFrom(r.Context()); err == nil {
		opSubject = idn.Subject
	}
	m := support.Message{
		ID:            tenant.NewID(),
		TicketID:      id,
		AuthorKind:    support.AuthorOperator,
		AuthorSubject: opSubject,
		Body:          body,
		CreatedAt:     time.Now().UTC(),
	}
	if err := h.support.AddMessage(r.Context(), m); err != nil {
		h.logger.ErrorContext(r.Context(), "ops support: add reply failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reply failed"})
		return
	}
	// Operator responded → answered, awaiting the tenant. Best-effort.
	if err := h.support.SetStatus(r.Context(), id, support.StatusAnswered); err != nil {
		h.logger.WarnContext(r.Context(), "ops support: set answered failed", slog.String("err", err.Error()))
	}
	// Close the loop: notify the submitter that a reply landed (link
	// only, never the body). Best-effort, detached; uses the ticket we
	// already loaded above.
	h.notifySupportUpdate(t)
	writeJSON(w, http.StatusCreated, m)
}

// supportStatus serves POST /v1/ops/support/{id}/status: set a
// ticket's status explicitly (e.g. in_progress, closed).
func (h *OpsHandler) supportStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req struct {
		Status string `json:"status"`
	}
	if !h.readOpsSupportJSON(w, r, &req) {
		return
	}
	status := support.Status(strings.TrimSpace(req.Status))
	if !support.ValidStatus(status) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown status"})
		return
	}
	if err := h.support.SetStatus(r.Context(), id, status); err != nil {
		if errors.Is(err, support.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ticket not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "ops support: set status failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "set status failed"})
		return
	}
	// Closing a ticket is a resolution the submitter cares about —
	// close the loop by email (link only). Other transitions (e.g.
	// in_progress) are internal triage and stay quiet. Best-effort:
	// reload the ticket for the author email; skip silently if gone.
	if status == support.StatusClosed {
		if t, err := h.support.GetByID(r.Context(), id); err == nil {
			h.notifySupportUpdate(t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": string(status)})
}

// notifySupportUpdate emails the ticket submitter that their ticket
// was updated — a close-the-loop nudge carrying a link to the in-app
// thread and NO ticket content. Best-effort and detached: it never
// blocks or fails the operator action. No-op when notifications are
// not wired or the submitter's email was never captured.
func (h *OpsHandler) notifySupportUpdate(t support.Ticket) {
	if h.notifier == nil || strings.TrimSpace(t.AuthorEmail) == "" {
		return
	}
	to := t.AuthorEmail
	link := h.supportLinkBase + "/dashboard/support/" + t.ID
	h.dispatch(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// The submitter's own language on their account; English when it
		// isn't known.
		lang := words.EN
		if tn, err := h.tenants.Get(ctx, t.TenantID); err == nil {
			lang = personlang.Of(tn, t.AuthorSubject)
		}
		if err := h.notifier.SupportTicketUpdated(ctx, to, link, lang); err != nil {
			h.logger.ErrorContext(ctx, "ops support: close-the-loop email failed",
				slog.String("ticket", t.ID), slog.String("err", err.Error()))
			return
		}
		h.logger.InfoContext(ctx, "ops support: close-the-loop email sent",
			slog.String("ticket", t.ID))
	})
}

func (h *OpsHandler) readOpsSupportJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body := http.MaxBytesReader(w, r.Body, opsSupportMaxBody)
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read request body"})
		return false
	}
	if len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty request body"})
		return false
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	return true
}
