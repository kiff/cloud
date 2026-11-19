package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
)

// SupportHandler exposes the tenant-facing support-ticket surface:
//
//	POST /v1/me/support/tickets            — file a ticket
//	GET  /v1/me/support/tickets            — list my tenant's tickets
//	GET  /v1/me/support/tickets/{id}       — one ticket + its thread
//	POST /v1/me/support/tickets/{id}/reply — add a reply (reopens it)
//
// Every route is identity-scoped: the tenant comes from the
// authenticated identity, never the URL or body, and a ticket from
// another tenant is a 404 (indistinguishable from missing). The
// operator side (/v1/ops/support*) lands in a follow-up.
type SupportHandler struct {
	store  support.Store
	logger *slog.Logger

	maxBodyBytes int64
}

// SupportOption configures a SupportHandler.
type SupportOption func(*SupportHandler)

// WithSupportLogger substitutes the logger.
func WithSupportLogger(l *slog.Logger) SupportOption {
	return func(h *SupportHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// NewSupportHandler wires the store. store is required.
func NewSupportHandler(store support.Store, opts ...SupportOption) *SupportHandler {
	h := &SupportHandler{store: store, logger: slog.Default(), maxBodyBytes: 64 * 1024}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register attaches the tenant support routes to mux.
func (h *SupportHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/me/support/tickets", h.create)
	mux.HandleFunc("GET /v1/me/support/tickets", h.list)
	mux.HandleFunc("GET /v1/me/support/tickets/{id}", h.get)
	mux.HandleFunc("POST /v1/me/support/tickets/{id}/reply", h.reply)
}

// field caps — small, generous, and enforced so a ticket can't be
// used to store arbitrary blobs.
const (
	maxTitleLen    = 200
	maxCategoryLen = 40
	maxBodyLen     = 8000
)

type createTicketRequest struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Category string `json:"category"`
}

type replyRequest struct {
	Body string `json:"body"`
}

type ticketDetailResponse struct {
	Ticket   support.Ticket    `json:"ticket"`
	Messages []support.Message `json:"messages"`
}

func (h *SupportHandler) create(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	var req createTicketRequest
	if !h.readJSON(w, r, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	body := strings.TrimSpace(req.Body)
	category := strings.TrimSpace(req.Category)
	if category == "" {
		category = "general"
	}
	if title == "" || body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title and body are required"})
		return
	}
	if len(title) > maxTitleLen || len(body) > maxBodyLen || len(category) > maxCategoryLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title, body, or category exceeds the allowed length"})
		return
	}

	now := time.Now().UTC()
	t := support.Ticket{
		ID:            tenant.NewID(),
		TenantID:      id.Tenant,
		AuthorSubject: id.Subject,
		AuthorEmail:   id.Email,
		Title:         title,
		Body:          body,
		Category:      category,
		Status:        support.StatusOpen,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := h.store.CreateTicket(r.Context(), t); err != nil {
		h.logger.ErrorContext(r.Context(), "support: create ticket failed",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create ticket failed"})
		return
	}
	h.logger.InfoContext(r.Context(), "support: ticket created",
		slog.String("tenant", id.Tenant), slog.String("ticket", t.ID))
	writeJSON(w, http.StatusCreated, t)
}

func (h *SupportHandler) list(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	tickets, err := h.store.ListForTenant(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "support: list tickets failed",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list tickets failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tickets": tickets})
}

func (h *SupportHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	ticketID := strings.TrimSpace(r.PathValue("id"))
	t, err := h.store.GetForTenant(r.Context(), id.Tenant, ticketID)
	if err != nil {
		if errors.Is(err, support.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ticket not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "support: get ticket failed",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "get ticket failed"})
		return
	}
	msgs, err := h.store.ListMessages(r.Context(), t.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "support: list messages failed",
			slog.String("ticket", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load ticket thread failed"})
		return
	}
	writeJSON(w, http.StatusOK, ticketDetailResponse{Ticket: t, Messages: msgs})
}

func (h *SupportHandler) reply(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	ticketID := strings.TrimSpace(r.PathValue("id"))
	// Scope the ticket to the tenant first — a reply to another
	// tenant's ticket is a 404, same as a missing one.
	t, err := h.store.GetForTenant(r.Context(), id.Tenant, ticketID)
	if err != nil {
		if errors.Is(err, support.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ticket not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "support: reply lookup failed",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reply failed"})
		return
	}

	var req replyRequest
	if !h.readJSON(w, r, &req) {
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

	m := support.Message{
		ID:            tenant.NewID(),
		TicketID:      t.ID,
		AuthorKind:    support.AuthorUser,
		AuthorSubject: id.Subject,
		Body:          body,
		CreatedAt:     time.Now().UTC(),
	}
	if err := h.store.AddMessage(r.Context(), m); err != nil {
		h.logger.ErrorContext(r.Context(), "support: add reply failed",
			slog.String("ticket", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reply failed"})
		return
	}
	// A user reply reopens the ticket so it resurfaces for the
	// operator. Best-effort: the reply already landed.
	if t.Status == support.StatusAnswered || t.Status == support.StatusClosed {
		if serr := h.store.SetStatus(r.Context(), t.ID, support.StatusOpen); serr != nil {
			h.logger.WarnContext(r.Context(), "support: reopen on reply failed",
				slog.String("ticket", t.ID), slog.String("err", serr.Error()))
		}
	}
	writeJSON(w, http.StatusCreated, m)
}

// requireIdentity resolves the authenticated identity + tenant,
// writing a 401 and returning ok=false on failure.
func (h *SupportHandler) requireIdentity(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return auth.Identity{}, false
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return auth.Identity{}, false
	}
	return id, true
}

// readJSON decodes a capped JSON body, writing a 400 on failure.
func (h *SupportHandler) readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	limit := h.maxBodyBytes
	if limit <= 0 {
		limit = 64 * 1024
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return false
		}
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
