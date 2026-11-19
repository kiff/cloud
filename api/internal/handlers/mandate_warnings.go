package handlers

import (
	"errors"
	"net/http"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

func (h *MandatesHandler) registerWarnings(mux *http.ServeMux) {
	if _, ok := h.store.(mandate.WarningStore); !ok {
		return
	}
	mux.HandleFunc("GET /v1/me/mandates/{id}/warnings", h.warningSettings)
	mux.HandleFunc("PUT /v1/me/mandates/{id}/warnings", h.warningSettings)
}

func (h *MandatesHandler) warningSettings(w http.ResponseWriter, r *http.Request) {
	id, tenant, ok := h.identity(w, r)
	if !ok {
		return
	}
	// An admin's setup key can configure notices; a key bound to a runtime
	// agent cannot gain that authority even if it also carries an admin role.
	if id.AgentID != "" || !can(id, membership.CapGovernance) {
		writeJSON(w, 403, map[string]string{"error": "only account admins can configure Card warnings"})
		return
	}
	cards, err := h.store.List(r.Context(), tenant)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Card could not be read"})
		return
	}
	found := false
	for _, m := range cards {
		if m.ID == r.PathValue("id") {
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "Card not found"})
		return
	}
	store := h.store.(mandate.WarningStore)
	if r.Method == http.MethodGet {
		c, err := store.GetWarningConfig(r.Context(), tenant, r.PathValue("id"))
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "warning settings could not be read"})
			return
		}
		writeJSON(w, 200, c)
		return
	}
	var req struct {
		Revision int64                   `json:"revision"`
		Settings mandate.WarningSettings `json:"settings"`
	}
	if !approverDecode(w, r, &req) {
		return
	}
	if req.Revision < 0 {
		writeJSON(w, 400, map[string]string{"error": "revision must not be negative"})
		return
	}
	if err = req.Settings.Validate(); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	c, err := store.SetWarningConfig(r.Context(), tenant, r.PathValue("id"), id.Subject, req.Revision, req.Settings)
	if errors.Is(err, mandate.ErrWarningConflict) {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "warning settings could not be saved"})
		return
	}
	writeJSON(w, 200, c)
}
