package handlers

import (
	"bytes"
	"net/http"
	"strings"
)

// NormalizeTypography keeps authored source documents intact while applying
// the public site's punctuation style to pages and public text surfaces.
// Static assets, video, binary downloads, and event streams pass through
// unchanged.
func NormalizeTypography(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") ||
			strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			next.ServeHTTP(w, r)
			return
		}

		buffered := &typographyResponseWriter{header: w.Header(), status: http.StatusOK}
		next.ServeHTTP(buffered, r)

		contentType := buffered.header.Get("Content-Type")
		if contentType == "" && buffered.body.Len() > 0 {
			contentType = http.DetectContentType(buffered.body.Bytes())
			buffered.header.Set("Content-Type", contentType)
		}

		body := buffered.body.Bytes()
		if strings.Contains(contentType, "text/html") ||
			strings.Contains(contentType, "text/plain") ||
			strings.Contains(contentType, "text/markdown") {
			body = normalizeTypographyBody(body)
			buffered.header.Del("Content-Length")
		}

		w.WriteHeader(buffered.status)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}

type typographyResponseWriter struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func (w *typographyResponseWriter) Header() http.Header { return w.header }

func (w *typographyResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *typographyResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.body.Write(p)
}

func normalizeTypographyBody(src []byte) []byte {
	out := bytes.Clone(src)

	// Definition-like constructions read naturally with a colon.
	out = bytes.ReplaceAll(out, []byte("</strong> — "), []byte("</strong>: "))
	out = bytes.ReplaceAll(out, []byte("</code> — "), []byte("</code>: "))

	// In prose, the former aside becomes a normal clause. Compact dashes that
	// remain in headings or labels become a plain hyphen.
	out = bytes.ReplaceAll(out, []byte(" — "), []byte(", "))
	out = bytes.ReplaceAll(out, []byte("—"), []byte("-"))
	return out
}
