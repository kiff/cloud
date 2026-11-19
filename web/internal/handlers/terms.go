package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// Terms serves the public Terms of Service at /terms.
//
// v0.1 of the contract between Kiff Agents OÜ (an Estonian
// private limited company) and customers using KIFF Cloud.
// Authored from widely-used SaaS templates and aligned with the
// privacy policy at /privacy and the security posture at
// /security.
//
// Keep the public surface free of private repository links,
// internal review notes, and source paths.
func Terms() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Terms().Render(r.Context(), w)
	})
}
