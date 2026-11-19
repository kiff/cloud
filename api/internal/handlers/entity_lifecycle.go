// Package handlers — entity_lifecycle.go ships the read-only
// governed-lifecycle view for a single entity (Slice 4).
//
// KIFF already records the facts an entity's history is made of:
// decisions (proposals), approval records, and an append-only audit
// trail with trace/correlation metadata. The framework's lifecycle
// package (v0.7.0) projects those three into one chronological,
// read-only view (proposal → validation → approval → execution →
// failure/denial/dedup). This handler exposes that projection over
// the cloud's per-tenant runtime at:
//
//	GET /v1/entities/{id}/lifecycle
//
// It is purely additive: a new read endpoint that assembles existing
// records. It changes no decision path and no other endpoint. Tenant
// scoping is enforced the same way the rest of the runtime surface is
// — each tenant's runtime carries stores pinned to kiff_t_<tenantID>,
// so an entity from another tenant is mechanically unreachable, and
// the caller never names the tenant (it comes from the auth context).
package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffdecision "github.com/kiff/kiff/pkg/kiff/decision"
	kifflifecycle "github.com/kiff/kiff/pkg/kiff/lifecycle"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
)

// EntityLifecycleHandler exposes GET /v1/entities/{id}/lifecycle. It
// depends on the same TenantRuntimeAccessor the per-request runtime
// surface uses; resolution returns the tenant's *runtime.Runtime
// whose audit / decision / approval stores are already tenant-scoped.
type EntityLifecycleHandler struct {
	tenants TenantRuntimeAccessor
	logger  *slog.Logger
}

// NewEntityLifecycleHandler wires the dependencies. A nil logger is
// replaced with slog.Default() so production always gets structured
// warns on failure paths.
func NewEntityLifecycleHandler(tenants TenantRuntimeAccessor, logger *slog.Logger) *EntityLifecycleHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &EntityLifecycleHandler{tenants: tenants, logger: logger}
}

// Register attaches GET /v1/entities/{id}/lifecycle to mux. The
// pattern is more specific than the framework-delegated
// /v1/entities/ subtree, so Go 1.22+ ServeMux precedence routes this
// exact path+method here while /v1/entities/{id}/execute etc. keep
// flowing through the framework handler (same approach as the
// tenant-wide approvals list).
func (h *EntityLifecycleHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/entities/{id}/lifecycle", h.get)
}

// get handles GET /v1/entities/{id}/lifecycle. Assembles the entity's
// governed history from the tenant runtime's audit, decision, and
// approval stores and returns the read-only lifecycle projection.
//
// Failure modes mirror the tenant-wide approvals list:
//
//   - identity missing or empty tenant: 401
//   - empty entity id in the path: 400
//   - tenant has no domain configured yet: 200 with an empty lifecycle
//     (pre-connection state, not a failure)
//   - tenant runtime resolution otherwise fails: 502
//   - store-level error while gathering records: 500
func (h *EntityLifecycleHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}

	entityID := strings.TrimSpace(r.PathValue("id"))
	if entityID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "entity id is required"})
		return
	}

	rt, err := h.tenants.For(r.Context(), id.Tenant)
	if err != nil {
		// A tenant that has not uploaded a domain yet has no runtime
		// and therefore no history — an expected pre-connection state,
		// not a data-plane failure. Return an empty lifecycle (200) the
		// same way the approvals/proposals list endpoints render an
		// empty collection.
		if errors.Is(err, domain.ErrDomainNotFound) {
			writeJSON(w, http.StatusOK, kifflifecycle.Assemble(entityID, nil, nil, nil))
			return
		}
		h.logger.WarnContext(r.Context(), "entity lifecycle: resolve tenant runtime failed",
			slog.String("tenant", id.Tenant),
			slog.String("entity_id", entityID),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tenant runtime unavailable"})
		return
	}
	if rt == nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tenant runtime unavailable"})
		return
	}

	records, decisions, approvals, err := h.gather(r, rt, entityID)
	if err != nil {
		h.logger.WarnContext(r.Context(), "entity lifecycle: gather records failed",
			slog.String("tenant", id.Tenant),
			slog.String("entity_id", entityID),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "assemble lifecycle failed"})
		return
	}

	// Assemble treats the audit trail as the chronological spine and
	// expects records in order. The in-memory store returns them in
	// append order; Postgres order is query-dependent — sort ascending
	// by CreatedAt so the projection is stable regardless of store.
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})

	writeJSON(w, http.StatusOK, kifflifecycle.Assemble(entityID, records, decisions, approvals))
}

// gather pulls the three record sets for the entity from the runtime's
// tenant-scoped stores. Each store is optional on the framework
// runtime (it tolerates nil), so a store that isn't wired contributes
// an empty slice rather than an error.
func (h *EntityLifecycleHandler) gather(r *http.Request, rt *kiffruntime.Runtime, entityID string) ([]kiffaudit.Record, []kiffdecision.Decision, []kiffapproval.Approval, error) {
	ctx := r.Context()

	var records []kiffaudit.Record
	if rt.Audit != nil {
		var err error
		records, err = rt.Audit.Query(ctx, kiffaudit.Filter{EntityID: entityID})
		if err != nil {
			return nil, nil, nil, err
		}
	}

	var decisions []kiffdecision.Decision
	if rt.Decisions != nil {
		var err error
		decisions, err = rt.Decisions.List(ctx, entityID)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	var approvals []kiffapproval.Approval
	if rt.Approvals != nil {
		var err error
		approvals, err = rt.Approvals.List(ctx, entityID)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	return records, decisions, approvals, nil
}
