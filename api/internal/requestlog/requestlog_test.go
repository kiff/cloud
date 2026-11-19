package requestlog_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/requestlog"
)

// lines are the JSON log lines buf holds.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

// The router's shape: a public mux, and an authenticated sub-mux behind a
// middleware that copies the request, as auth's does.
func router() http.Handler {
	authed := http.NewServeMux()
	authed.HandleFunc("GET /v1/agent-drafts/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such draft", http.StatusNotFound)
	})
	authed.HandleFunc("POST /v1/agents", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	authed.HandleFunc("GET /v1/me/stream", func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Write([]byte("data: hello\n\n"))
		f.Flush()
	})
	copying := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(r.Context()))
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("User-agent: *")) })
	mux.Handle("/v1/", copying(requestlog.Mark(authed)))
	return mux
}

// One line per request: the route as matched, never the path or its id;
// the status the handler gave; how long it took. Health checks aren't
// logged, and a path no mux matched is "unmatched".
func TestOneLinePerRequestWithTheRouteNotThePath(t *testing.T) {
	var buf bytes.Buffer
	h := requestlog.Wrap(router(), slog.New(slog.NewJSONHandler(&buf, nil)))
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/agent-drafts/drf_01M7J02JSECRETID"},
		{"POST", "/v1/agents"},
		{"GET", "/robots.txt"},
		{"GET", "/healthz"},
		{"GET", "/nowhere/tnt_example9"},
		{"GET", "/v1/me/stream"},
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, c.path, nil))
	}
	got := lines(t, &buf)
	want := []struct {
		route  string
		status float64
	}{
		{"GET /v1/agent-drafts/{id}", 404},
		{"POST /v1/agents", 201},
		{"GET /robots.txt", 200},
		{"unmatched", 404},
		{"GET /v1/me/stream", 200},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %s", len(got), len(want), buf.String())
	}
	for i, w := range want {
		l := got[i]
		if l["msg"] != requestlog.Msg || l["route"] != w.route || l["status"] != w.status {
			t.Errorf("line %d: %v, want route %q status %v", i, l, w.route, w.status)
		}
		if _, ok := l["duration_ms"].(float64); !ok {
			t.Errorf("line %d has no duration: %v", i, l)
		}
		// route, status, duration, plus slog's time, level and msg: nothing else.
		if len(l) != 6 {
			t.Errorf("line %d has fields beyond route, status and duration: %v", i, l)
		}
	}
	for _, secret := range []string{"drf_01M7J02JSECRETID", "tnt_example9", "/nowhere"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("a path's id reached the log: %q", secret)
		}
	}
}
