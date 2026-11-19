package handlers

import "net/http"

// streamClient has no timeout: an SSE connection is long-lived. Cancellation
// is driven by the request context (client disconnect), not a deadline.
var streamClient = &http.Client{Timeout: 0}

// stream proxies the tenant's live event stream (Server-Sent Events) from
// the API (api.kiff.dev/v1/me/stream) to the browser. The browser holds an
// app-session cookie, not an API credential, so the dashboard can't open the
// API stream directly — this handler authenticates the session, attaches the
// session bearer, and pipes the event stream straight through.
//
//	GET /dashboard/stream  → text/event-stream (decision/approval events)
//
// The connection lives until the browser disconnects (r.Context cancels,
// which cancels the upstream request and unblocks the copy loop).
func (h *DashboardHandler) stream(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.api == nil {
		http.Error(w, "streaming not configured", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		// Upstream session no longer usable; the page-level auth will
		// bounce the next navigation. For the stream, just close.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	upstream := h.api.BaseURL() + "/v1/me/stream"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := streamClient.Do(req)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream stream error", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Pipe upstream → browser, flushing each chunk so events arrive live.
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if rerr != nil {
			return
		}
	}
}
