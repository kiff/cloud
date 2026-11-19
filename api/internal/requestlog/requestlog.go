// Package requestlog writes one line per API request once it's answered:
// the route it matched, its status and how long it took. A route is the
// pattern a ServeMux matched ("GET /v1/agents/{id}"), never the path, so no
// id, tenant, subject, header or body is ever in the line. Before it, KIFF's
// log had a line for each gateway call and none for the apps' own reads, so
// what KIFF answered a phone's draft read couldn't be told.
package requestlog

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Msg is the line's message, for a log filter: { $.msg = "api_request" }.
const Msg = "api_request"

// unmatched is the route of a request no mux matched (its 404 or 405).
const unmatched = "unmatched"

// quiet are the routes not logged: the load balancer's health checks, every
// few seconds, which say nothing about the apps.
var quiet = map[string]bool{"GET /healthz": true}

type ctxKey struct{}

// holder carries the route out of the mux nearest the handler.
type holder struct{ route string }

// Wrap logs each request h answers. The route is what the mux nearest the
// handler matched (Mark), else what h's own mux matched.
func Wrap(h http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		held := &holder{}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, held))
		sw := &statusWriter{ResponseWriter: w}
		h.ServeHTTP(sw, r)
		route := held.route
		if route == "" {
			// A ServeMux sets the pattern on the request it serves.
			route = r.Pattern
		}
		if route == "" {
			route = unmatched
		}
		if quiet[route] {
			return
		}
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		logger.LogAttrs(r.Context(), slog.LevelInfo, Msg,
			slog.String("route", route),
			slog.Int("status", status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()))
	})
}

// Mark has the route of a request mux serves be the pattern mux matched,
// unless a mux nearer the handler set it first. Use it on a sub-mux behind a
// middleware that copies the request (WithContext): the pattern is set on
// the copy, which Wrap never sees.
func Mark(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		if held, ok := r.Context().Value(ctxKey{}).(*holder); ok && held.route == "" && r.Pattern != "" {
			held.route = r.Pattern
		}
	})
}

// statusWriter keeps the status the handler answered with.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps a stream (GET /v1/me/stream) flushing through the writer.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the writer underneath.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
