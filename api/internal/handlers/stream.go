package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/stream"
)

// StreamHandler serves the tenant-scoped live event stream over
// Server-Sent Events:
//
//	GET /v1/me/stream  → text/event-stream of {decision, approval, receipt}
//	                     events for the authenticated tenant.
//
// The dashboard opens one EventSource and refetches the relevant view
// when an event arrives, so approvals/decisions/receipts appear without
// a manual reload. Events are a nudge, not the source of truth: a
// client that misses one (slow buffer, brief disconnect) reconciles on
// its next fetch. Tenant isolation is enforced by auth.IdentityFrom —
// a connection only ever sees its own tenant's events.
type StreamHandler struct {
	broker *stream.Broker
	logger *slog.Logger

	// heartbeat is the interval between keep-alive comments. Kept as a
	// field so tests can shorten it. Zero uses the default.
	heartbeat time.Duration
}

// NewStreamHandler wires the broker. Returns nil when broker is nil so
// the router can skip registration when live updates aren't wired.
func NewStreamHandler(broker *stream.Broker, logger *slog.Logger) *StreamHandler {
	if broker == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &StreamHandler{broker: broker, logger: logger, heartbeat: 25 * time.Second}
}

// Register attaches GET /v1/me/stream. No-op on a nil handler.
func (h *StreamHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/me/stream", h.stream)
}

func (h *StreamHandler) stream(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no tenant identity"})
		return
	}
	// Every decision in the account goes out on this stream, whichever
	// agent asked, so it is the account's view: a person reads it by
	// their role, a key only with the account roles. A key bound to an
	// agent, or a runtime key, would otherwise watch every other agent's
	// decisions live. The same rule as held work
	// (exceptionsIdentity).
	allowed := id.ManagesAccount() || id.ReadOnlyAccountKey()
	if id.MemberRole != "" {
		allowed = membership.Can(id.MemberRole, membership.CapRead)
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "the live event stream is the account's view, for people and account keys, not an agent's key"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // defeat proxy buffering (nginx; Caddy streams text/event-stream by default)
	w.WriteHeader(http.StatusOK)

	ch, cancel := h.broker.Subscribe(id.Tenant)
	defer cancel()

	// Initial comment so the client's connection opens promptly and any
	// intermediary flushes headers.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	hb := h.heartbeat
	if hb <= 0 {
		hb = 25 * time.Second
	}
	ticker := time.NewTicker(hb)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Keep-alive comment; also detects a dead connection via
			// the write error path on the next flush.
			if _, werr := fmt.Fprint(w, ": ping\n\n"); werr != nil {
				return
			}
			flusher.Flush()
		case e, open := <-ch:
			if !open {
				return
			}
			payload, merr := json.Marshal(e.Data)
			if merr != nil {
				payload = []byte("{}")
			}
			// SSE frame: a named event + JSON data line.
			if _, werr := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, payload); werr != nil {
				return
			}
			flusher.Flush()
		}
	}
}
