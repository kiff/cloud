package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/permit"
)

// PermitTargetsHandler serves /v1/me/permit-targets: where
// each action's execution permit goes. The gateway replaces the whole list
// with the owner's or an admin's key whenever its connections change, in
// the same change, as it applies the gateway contract. The route needs
// domains.manage, so an agent's or runtime's key cannot set where its own
// permits go. A connector key sets only the gateway's targets.
type PermitTargetsHandler struct {
	targets permit.Targets
	logger  *slog.Logger
	now     func() time.Time
	owners  GatewayToolOwners
}

// WithGatewayOwners lets a connector key change only the targets of tools
// it connected. Without it, a connector key adds and changes none.
func (h *PermitTargetsHandler) WithGatewayOwners(o GatewayToolOwners) *PermitTargetsHandler {
	h.owners = o
	return h
}

// NewPermitTargetsHandler serves targets from store.
func NewPermitTargetsHandler(store permit.Targets, logger *slog.Logger) *PermitTargetsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PermitTargetsHandler{targets: store, logger: logger, now: time.Now}
}

const maxPermitTargets = 500

func (h *PermitTargetsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/permit-targets", h.list)
	mux.HandleFunc("PUT /v1/me/permit-targets", h.replace)
}

func (h *PermitTargetsHandler) list(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no identity"})
		return
	}
	ts, err := h.targets.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "permit targets: list failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the permit targets could not be read"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": ts})
}

func (h *PermitTargetsHandler) replace(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no identity"})
		return
	}
	var body struct {
		Targets []permit.Target `json:"targets"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || len(body.Targets) > maxPermitTargets {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body must be {\"targets\": [...]}, at most 500"})
		return
	}
	if id.IsConnector() && !h.connectorMayReplace(w, r, id.Tenant, body.Targets, newConnectorScope(r.Context(), h.owners, id.Tenant, id.APIKeyID())) {
		return
	}
	seen := map[string]bool{}
	for _, t := range body.Targets {
		k := t.Domain + "/" + t.Action
		if err := t.Validate(); err != nil || seen[k] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "each target needs a domain, a distinct action, a tool and an audience: " + k})
			return
		}
		seen[k] = true
	}
	if err := h.targets.Replace(r.Context(), id.Tenant, body.Targets, id.Subject, h.now()); err != nil {
		h.logger.ErrorContext(r.Context(), "permit targets: replace failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the permit targets were not changed"})
		return
	}
	h.logger.InfoContext(r.Context(), "permit targets replaced", slog.String("tenant", id.Tenant), slog.Int("targets", len(body.Targets)))
	writeJSON(w, http.StatusOK, map[string]any{"targets": body.Targets})
}
