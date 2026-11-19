package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
)

func (h *MeHandler) setTimeZone(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session or key is required"})
		return
	}
	if id.AgentID != "" || !can(id, membership.CapAccountSettings) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the account's owner or an admin can change its time zone"})
		return
	}
	var body struct {
		TimeZone string `json:"time_zone"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide a named time_zone, such as Europe/Paris"})
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide one JSON object"})
		return
	}
	zone, valid := tenant.ParseTimeZone(body.TimeZone)
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose a named IANA time zone, such as Europe/Paris, or UTC"})
		return
	}
	if err := h.tenants.SetMetadata(r.Context(), id.Tenant, tenant.MetadataKeyTimeZone, zone); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the time zone could not be saved"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"time_zone": zone})
}
