package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kiff/cloud/api/internal/attention"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
)

type AttentionStore interface {
	List(context.Context, string, string, int, int, bool) (attention.Feed, error)
	MarkSeen(context.Context, string, string, map[string]int64) error
	Act(context.Context, string, string, string, string, int64) error
}
type AttentionHandler struct {
	store AttentionStore
	// warnings reads a Card's warnings, so a warning in Home is worded in
	// the reader's language. Nil: as stored, in English.
	warnings AttentionWarnings
}

// AttentionWarnings reads a Card's recorded warnings.
type AttentionWarnings interface {
	CardWarnings(ctx context.Context, tenant, cardID string) ([]mandate.CardWarning, error)
}

func NewAttentionHandler(store AttentionStore) *AttentionHandler {
	return &AttentionHandler{store: store}
}

// WithAttentionWarnings words each Card warning in Home in the reader's
// language, from the warning itself, instead of the English it was
// stored with.
func (h *AttentionHandler) WithAttentionWarnings(w AttentionWarnings) *AttentionHandler {
	h.warnings = w
	return h
}

// wordWarnings rewrites each Card warning's reason in l, composed from the
// warning (words.WarningReason). English, a warning it can't find, or a
// Card whose warnings can't be read keep the stored reason.
func (h *AttentionHandler) wordWarnings(ctx context.Context, tenant string, l words.Lang, items []attention.Item) {
	if h.warnings == nil || l == words.EN {
		return
	}
	byCard := map[string]map[string]mandate.CardWarning{}
	for i := range items {
		it := &items[i]
		eventID, ok := strings.CutPrefix(it.ID, "card-warning:")
		if it.Kind != "card_warning" || !ok {
			continue
		}
		events, read := byCard[it.SourceID]
		if !read {
			events = map[string]mandate.CardWarning{}
			if ws, err := h.warnings.CardWarnings(ctx, tenant, it.SourceID); err == nil {
				for _, w := range ws {
					events[w.ID] = w
				}
			}
			byCard[it.SourceID] = events
		}
		if w, found := events[eventID]; found {
			it.Reason = words.WarningReason(w, l)
		}
	}
}
func (h *AttentionHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/attention", h.list)
	mux.HandleFunc("POST /v1/me/attention/seen", h.seen)
	mux.HandleFunc("POST /v1/me/attention/{id}/{action}", h.act)
}
func attentionIdentity(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" || id.Subject == "" {
		writeJSON(w, 401, map[string]string{"error": "authentication required"})
		return id, false
	}
	// Fleet review is an owner-plane operation. Agent credentials cannot see or
	// acknowledge another agent's work, even when they are tenant-wide keys.
	if id.APIKey || id.Method == auth.MethodAPIKey || id.DomainID != "" {
		writeJSON(w, 403, map[string]string{"error": "human session required"})
		return id, false
	}
	return id, true
}
func (h *AttentionHandler) list(w http.ResponseWriter, r *http.Request) {
	id, ok := attentionIdentity(w, r)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 0 || page > 100000 {
		writeJSON(w, 400, map[string]string{"error": "invalid page"})
		return
	}
	var feed attention.Feed
	var err error
	if kind := r.URL.Query().Get("kind"); kind != "" {
		// Needs you lists each kind on its own: domain approvals
		// in Waiting, blocked calls in Blocked, Card warnings in Warnings.
		if kind != "card_warning" && kind != "approval" && kind != "blocked" {
			writeJSON(w, 400, map[string]string{"error": "invalid attention kind"})
			return
		}
		filtered, ok := h.store.(interface {
			ListKind(context.Context, string, string, int, int, bool, string) (attention.Feed, error)
		})
		if !ok {
			writeJSON(w, 503, map[string]string{"error": "warning attention unavailable"})
			return
		}
		feed, err = filtered.ListKind(r.Context(), id.Tenant, id.Subject, 100, page*100, r.URL.Query().Get("unread") == "1", kind)
	} else {
		feed, err = h.store.List(r.Context(), id.Tenant, id.Subject, 100, page*100, r.URL.Query().Get("unread") == "1")
	}
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "attention unavailable"})
		return
	}
	h.wordWarnings(r.Context(), id.Tenant, words.Lang(replyLanguage(r)), feed.Items)
	writeJSON(w, 200, feed)
}
func (h *AttentionHandler) seen(w http.ResponseWriter, r *http.Request) {
	id, ok := attentionIdentity(w, r)
	if !ok {
		return
	}
	var body struct {
		Items map[string]int64 `json:"items"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || len(body.Items) > 200 {
		writeJSON(w, 400, map[string]string{"error": "invalid acknowledgement"})
		return
	}
	for _, v := range body.Items {
		if v < 1 {
			writeJSON(w, 400, map[string]string{"error": "invalid revision"})
			return
		}
	}
	if err := h.store.MarkSeen(r.Context(), id.Tenant, id.Subject, body.Items); err != nil {
		writeJSON(w, 503, map[string]string{"error": "acknowledgement unavailable"})
		return
	}
	w.WriteHeader(204)
}
func (h *AttentionHandler) act(w http.ResponseWriter, r *http.Request) {
	id, ok := attentionIdentity(w, r)
	if !ok {
		return
	}
	action := r.PathValue("action")
	if action != "claim" && action != "release" && action != "resolve" {
		writeJSON(w, 400, map[string]string{"error": "unknown action"})
		return
	}
	var body struct {
		Revision int64 `json:"revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Revision < 1 {
		writeJSON(w, 400, map[string]string{"error": "revision required"})
		return
	}
	err := h.store.Act(r.Context(), id.Tenant, id.Subject, r.PathValue("id"), action, body.Revision)
	if errors.Is(err, attention.ErrConflict) {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "attention unavailable"})
		return
	}
	w.WriteHeader(204)
}
