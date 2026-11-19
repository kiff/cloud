package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

// The integration proofs on the operator console: the monitoring page
// (one row per app), a run's replay, and the overrides of an app's
// write hold. The API re-checks the operator gate and every rule; this
// side only renders and forwards.

func (h *OpsHandler) registerProofs(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops/proofs", h.proofs)
	mux.HandleFunc("GET /ops/proofs/{run_id}", h.proofRun)
	mux.HandleFunc("POST /ops/proofs/apps/{app}/{action}", h.proofOverride)
}

func (h *OpsHandler) proofs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	if h.api == nil {
		_ = pages.OpsUnavailable("Proofs", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/proofs", http.StatusFound)
		return
	}
	rows, err := h.api.OpsProofsBearer(r.Context(), bearer)
	if err != nil {
		h.renderError(w, r, "Proofs", chrome, err)
		return
	}
	_ = pages.OpsProofs(*rows, r.URL.Query().Get("refused") != "", chrome).Render(r.Context(), w)
}

func (h *OpsHandler) proofRun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	if h.api == nil {
		_ = pages.OpsUnavailable("Proofs", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/proofs", http.StatusFound)
		return
	}
	replay, err := h.api.OpsProofRunBearer(r.Context(), bearer, r.PathValue("run_id"))
	if err != nil {
		if errors.Is(err, apiclient.ErrReceiptNotFound) {
			http.NotFound(w, r)
			return
		}
		h.renderError(w, r, "Proofs", chrome, err)
		return
	}
	_ = pages.OpsProofRun(*replay, chrome).Render(r.Context(), w)
}

// proofOverrideFor are the lengths an operator can pick; the API caps a
// hold at 30 days and a release at 7.
var proofOverrideFor = map[string]time.Duration{
	"4h": 4 * time.Hour, "24h": 24 * time.Hour, "72h": 72 * time.Hour,
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

// proofOverride handles POST /ops/proofs/apps/{app}/{hold|release|clear}.
func (h *OpsHandler) proofOverride(w http.ResponseWriter, r *http.Request) {
	back := "/ops/proofs"
	action := r.PathValue("action")
	if action != "hold" && action != "release" && action != "clear" {
		http.NotFound(w, r)
		return
	}
	bearer, ok := h.guardMutation(w, r)
	if !ok {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	o := apiclient.OpsProofOverride{Reason: r.FormValue("reason"), OwnerApproval: r.FormValue("owner_approval")}
	if action != "clear" {
		d, ok := proofOverrideFor[r.FormValue("for")]
		if !ok {
			http.Redirect(w, r, back+"?refused=1", http.StatusSeeOther)
			return
		}
		o.Until = time.Now().UTC().Add(d).Format(time.RFC3339)
	}
	if err := h.api.OpsProofOverrideBearer(r.Context(), bearer, r.PathValue("app"), action, o); err != nil {
		h.logger.WarnContext(r.Context(), "ops console: proof override refused",
			slog.String("action", action), slog.String("err", err.Error()))
		http.Redirect(w, r, back+"?refused="+url.QueryEscape(action), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
