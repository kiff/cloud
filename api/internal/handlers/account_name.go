package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Unnamed is what an account nobody has named is called for everyone but
// the person it was made for: someone invited to it never sees its
// owner's email.
const Unnamed = "Unnamed account"

// accountLabel is what viewer sees the account called: the name an admin
// gave it. Unnamed, the person it was made for sees their own email (the
// slug if it can't be read, never a sign-in id), and everyone else
// Unnamed. viewer "" is someone other than its maker.
func accountLabel(ctx context.Context, t tenant.Tenant, emails EmailLookup, viewer string) string {
	name, first := tenant.NameFor(t)
	if name != "" {
		return name
	}
	if first == "" || first != viewer {
		return Unnamed
	}
	if emails != nil {
		if e, err := emails.EmailForSubject(ctx, first); err == nil && strings.TrimSpace(e) != "" {
			return strings.TrimSpace(e)
		}
	}
	return t.Slug
}

// setAccountName names the account. Like its time zone, only the
// owner or an admin may, never an agent's key.
func (h *MeHandler) setAccountName(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session or key is required"})
		return
	}
	if id.AgentID != "" || !can(id, membership.CapAccountSettings) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the account's owner or an admin can name it"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body must be {"name": "..."}`})
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide one JSON object"})
		return
	}
	name, valid := tenant.ParseAccountName(body.Name)
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the name needs 1 to 80 characters, on one line"})
		return
	}
	if err := h.tenants.SetMetadata(r.Context(), id.Tenant, tenant.MetadataKeyAccountName, name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the name could not be saved"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}
