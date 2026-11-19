package handlers

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/session"
)

const demoPrefix = "/demo"

// DemoApp mounts the real dashboard and Studio handlers under /demo. Their API
// client is backed by an in-process fixture transport, and a synthetic session
// is attached directly to the request context. No auth, database, API, or
// executor call leaves the web process.
func DemoApp(logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	client, err := apiclient.NewSessionAwareWithTransport("http://kiff-demo.invalid", 2*time.Second, newDemoAPI())
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "demo unavailable", http.StatusInternalServerError)
		})
	}
	// This manager only satisfies the existing handler's flash boundary. Demo
	// responses strip cookies, so its fixed key never signs browser state.
	sessions, err := session.NewManager([]byte("kiff-public-demo-session-key-2026-only"))
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "demo unavailable", http.StatusInternalServerError)
		})
	}

	dash := NewDashboardHandler(client, sessions, StaticBearerSource(), logger)
	dash.PublicAPIURL = "https://api.kiff.dev"
	innerMux := http.NewServeMux()
	dash.Register(innerMux)
	NewStudioHandler(client, sessions, StaticBearerSource(), logger).Register(innerMux)
	inner := dash.WithChrome(innerMux)

	demoSession := session.Session{
		Subject: "demo-user", Email: "alex@northstar.example", Bearer: "demo-session",
		DisplayName: "Alex Morgan", ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		LastDomain: "accounts-payable", LastWindow: "30d",
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == demoPrefix || r.URL.Path == demoPrefix+"/" {
			http.Redirect(w, r, demoPrefix+"/dashboard/domains?tour=1&step=0", http.StatusFound)
			return
		}
		if !isDemoAppPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		clone := r.Clone(withSession(r.Context(), demoSession))
		clone.URL.Path = strings.TrimPrefix(r.URL.Path, demoPrefix)
		clone.RequestURI = clone.URL.RequestURI()
		clone.Header = r.Header.Clone()
		clone.Header.Del("Cookie")

		// Live updates are intentionally off in a deterministic fixture session.
		if clone.URL.Path == "/dashboard/stream" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		rec := newMemoryResponseWriter()
		inner.ServeHTTP(rec, clone)
		writeDemoResponse(w, rec)
	})
}

func writeDemoResponse(w http.ResponseWriter, rec *memoryResponseWriter) {
	for key, values := range rec.header {
		if strings.EqualFold(key, "Set-Cookie") || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'none'; frame-ancestors 'self'; base-uri 'none'; img-src 'self' data:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; font-src 'self'; form-action 'self'")

	if location := rec.header.Get("Location"); location != "" {
		w.Header().Set("Location", rewriteDemoURL(location))
	}

	body := rec.body.Bytes()
	if strings.Contains(rec.header.Get("Content-Type"), "text/html") {
		// The real dashboard is intentionally written against its canonical
		// /dashboard namespace. Rewriting only the rendered response keeps the
		// production templates unchanged and makes this mount removable.
		body = bytes.ReplaceAll(body, []byte(`var es = new EventSource("/dashboard/stream");`), []byte(`return;`))
		body = bytes.ReplaceAll(body, []byte(`"/dashboard`), []byte(`"`+demoPrefix+`/dashboard`))
		body = bytes.ReplaceAll(body, []byte(`'/dashboard`), []byte(`'`+demoPrefix+`/dashboard`))
		body = bytes.ReplaceAll(body, []byte(`"/cards`), []byte(`"`+demoPrefix+`/cards`))
		body = bytes.ReplaceAll(body, []byte(`'/cards`), []byte(`'`+demoPrefix+`/cards`))
		body = bytes.ReplaceAll(body, []byte(`"/agents`), []byte(`"`+demoPrefix+`/agents`))
		body = bytes.ReplaceAll(body, []byte(`'/agents`), []byte(`'`+demoPrefix+`/agents`))
		body = bytes.ReplaceAll(body, []byte(`<form method="post" action="/sign-out">`), []byte(`<form data-demo-sign-out>`))
		body = bytes.Replace(body, []byte("</head>"), []byte(`<link rel="stylesheet" href="/static/css/demo-app.css"></head>`), 1)
		body = bytes.Replace(body, []byte("</body>"), []byte(`<script src="/static/js/demo-app.js" defer></script></body>`), 1)
	}

	w.WriteHeader(rec.statusCode())
	_, _ = w.Write(body)
}

func rewriteDemoURL(location string) string {
	if strings.HasPrefix(location, "/dashboard") {
		return demoPrefix + location
	}
	if strings.HasPrefix(location, "/cards") || strings.HasPrefix(location, "/agents") {
		return demoPrefix + location
	}
	if strings.HasPrefix(location, "/sign-in") || strings.HasPrefix(location, "/sign-out") {
		return demoPrefix + "/dashboard/domains"
	}
	return location
}

func isDemoAppPath(path string) bool {
	return strings.HasPrefix(path, demoPrefix+"/dashboard") ||
		path == demoPrefix+"/cards" ||
		strings.HasPrefix(path, demoPrefix+"/cards/") ||
		path == demoPrefix+"/agents" ||
		strings.HasPrefix(path, demoPrefix+"/agents/")
}
