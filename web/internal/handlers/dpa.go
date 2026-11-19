package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// DPA serves the public Data Processing Agreement template at
// /legal/dpa.
//
// This is the GDPR Article 28 contract that governs Kiff Agents
// OÜ's processor-side relationship with customers whose tenant
// data we hold. Tenants whose own customers are EU residents
// need this artifact to demonstrate Article 28 compliance
// against us; without a published template, enterprise sales
// stalls.
//
// v0.1 is a template, not a signed contract. Keep the public
// surface free of private repository links and internal review
// notes.
func DPA() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.DPA().Render(r.Context(), w)
	})
}
