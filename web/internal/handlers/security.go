package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// Security serves the public security posture page at /security.
//
// The page is a v0 of the trust-posture design's
// transparent-disclosure pillar: posture summary, current
// operational practices, sub-processor list, incident response,
// data minimization, and a candid roadmap section that lists what
// the cloud does not yet have.
func Security() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Security().Render(r.Context(), w)
	})
}
