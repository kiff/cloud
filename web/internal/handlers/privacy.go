package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// Privacy serves the public privacy policy at /privacy.
//
// The page is a v0.1 of the controller-side privacy notice for
// Kiff Agents OÜ. It documents what we collect, why, retention,
// sub-processors (cross-link to /security), international
// transfer instruments, data-subject rights, and the cookie
// disclosure for the strictly-necessary HMAC session cookie set
// by the dashboard auth path.
//
// Keep the public surface free of private repository links,
// internal review notes, and source paths.
func Privacy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Privacy().Render(r.Context(), w)
	})
}
