package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/proposals"
)

// WithProposalsReader installs the read-side surface for
// GET /v1/proposals. Production wires
// proposals.NewPostgresReader; tests pass a MemoryReader
// pre-seeded with rows. nil disables the route — useful for the
// in-memory smoke run that has no per-tenant pool wired.
func WithProposalsReader(r proposals.Reader) ProposalsOption {
	return func(h *ProposalsHandler) {
		if r != nil {
			h.reader = r
		}
	}
}

// proposalsListResponse is the JSON body returned by GET
// /v1/proposals. The proposals slice is empty (not nil) when no
// rows match so the dashboard's renderer never has to nil-check.
//
// LastReceivedAt is the connection-status data source the
// dashboard's header reads. It is set to proposals[0].created_at
// when at least one row matches; absent (zero value, with omitempty
// dropping it) when zero rows match. Callers infer "this tenant has
// not been connected yet" from the field's absence.
type proposalsListResponse struct {
	Proposals      []proposals.DecisionRow `json:"proposals"`
	LastReceivedAt *time.Time              `json:"last_received_at,omitempty"`
}

// list handles GET /v1/proposals. Returns the most recent
// proposal decisions for the caller's tenant in DESC created_at
// order, capped at min(limit, MaxLimit). Default limit is
// DefaultLimit (20).
//
// Tenant scoping is enforced at the Reader layer: the
// PostgresReader uses tenant.SchemaPool to pin search_path to
// kiff_t_<tenantID> on the query connection, so a row from
// another tenant is mechanically unreachable. The MemoryReader
// filters its internal slice on tenant id. The handler never
// lets the caller specify the tenant — id.Tenant comes from the
// auth context the middleware attached.
//
// What this handler does NOT do:
//
//   - It does not surface the request `parameters` map. The map
//     is tenant-controlled free-form JSON that may carry PII,
//     and DecisionRow has no field for it. Even if a future
//     change adds parameters to kiff_audit.data, the wire shape
//     stays clean because the row struct doesn't carry the
//     field.
//   - It does not surface the caller-supplied
//     `reasoning_summary` for the same reason.
//   - It does not authenticate API keys; it relies on the
//     middleware's identity attribution. An unauthenticated
//     request returns 401 before reaching the reader.
func (h *ProposalsHandler) list(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	if id.DomainID != "" {
		// Decision rows are tenant-scoped but the read model does not expose
		// their stamped domain_id yet. Do not let a domain-bound key fall back
		// to a tenant-wide list.
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "proposal list is not available to a domain-bound credential"})
		return
	}
	if h.reader == nil {
		// Reader was not wired (e.g. an in-memory smoke run with
		// no per-tenant pool). Surface as 503 so the dashboard
		// can fall through to receipts/usage instead of
		// rendering an empty table that pretends to be authoritative.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "proposals list surface not configured"})
		return
	}

	limit := parseLimit(r.URL.Query().Get("limit"), proposals.DefaultLimit, proposals.MaxLimit)

	rows, err := h.reader.ListDecisions(r.Context(), id.Tenant, limit)
	if err != nil {
		// Don't leak internal SQL or schema details to the
		// caller; the handler's logger captures them. The
		// response stays a generic 500 with no body fields the
		// caller can pivot on.
		if h.logger != nil {
			h.logger.WarnContext(r.Context(), "proposals: list decisions failed",
				"tenant", id.Tenant,
				"err", err.Error(),
			)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list proposals failed"})
		return
	}
	if rows == nil {
		rows = []proposals.DecisionRow{}
	}

	resp := proposalsListResponse{Proposals: rows}
	if len(rows) > 0 {
		// proposals[0] is the most recent row (DESC by created_at).
		// Copy the value into a local pointer so the JSON encoder
		// emits "last_received_at" with the correct value.
		latest := rows[0].CreatedAt
		resp.LastReceivedAt = &latest
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// verifyProposalsListReaderInterface is a compile-time check
// that proposals.Reader is the type the handler stores. Avoids
// a silent drift where a future option wires something with the
// wrong shape.
var _ = func() proposals.Reader {
	return (*proposals.MemoryReader)(nil)
}()

// readerErrors that the handler treats as 500. Currently empty;
// kept for future use if any reader implementation introduces a
// "tenant not provisioned" sentinel that maps to 404.
var _ = errors.New
