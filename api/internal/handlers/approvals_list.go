// Package handlers — approvals_list.go ships the tenant-wide
// list-approvals surface for the dashboard's approval queue UX.
//
// Today the framework exposes a per-entity list at
// GET /v1/entities/{id}/approvals; the dashboard's tenant-wide
// queue cannot render without N round-trips per row. This handler
// answers the same question for the whole tenant in one call by
// driving runtime.Approvals.List(ctx, "") (empty entityID =
// "all entities") through the per-tenant runtime the cloud
// already resolves.
//
// Tenant scoping is enforced by the runtime resolution path:
// each tenant's runtime carries its own ApprovalStore wired with
// search_path pinned to kiff_t_<tenantID>, so a row from
// another tenant is mechanically unreachable. The handler never
// lets the caller specify the tenant — id.Tenant comes from the
// auth context.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"

	"github.com/kiff/cloud/api/internal/approvals"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
)

// TenantApprovalLister is the management-side read: every domain in the
// tenant plus unattributed legacy rows. Declared here as a result-shaped
// interface so the handler does not depend on a transaction, and its
// tests do not need a database. approvals.TenantReader implements it.
type TenantApprovalLister interface {
	ListAll(ctx context.Context, tenantID, entityID string) ([]approvals.Approval, error)
}

// ApprovalsListHandler exposes GET /v1/approvals. It depends on
// the same TenantRuntimeAccessor the per-request runtime
// surface uses; resolution returns the tenant's *runtime.Runtime
// whose Approvals store is already tenant-scoped.
type ApprovalsListHandler struct {
	tenants TenantRuntimeAccessor
	// reader serves management identities: every domain plus the
	// unattributed legacy rows. Nil disables the management path, which
	// then reports 503 rather than silently falling back to a single
	// domain's view.
	reader TenantApprovalLister
	logger *slog.Logger
}

// NewApprovalsListHandler wires the dependencies. logger is
// optional; a nil logger is replaced with slog.Default() so
// production calls always get structured warns on failure paths.
func NewApprovalsListHandler(tenants TenantRuntimeAccessor, reader TenantApprovalLister, logger *slog.Logger) *ApprovalsListHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ApprovalsListHandler{tenants: tenants, reader: reader, logger: logger}
}

// Register attaches GET /v1/approvals to mux. The literal-path
// pattern matches before the framework-delegated /v1/approvals/
// prefix pattern (Go 1.22+ ServeMux precedence rule), so
// /v1/approvals (no trailing slash, no id) lands here while
// /v1/approvals/{id}/grant continues to flow through the framework
// handler.
func (h *ApprovalsListHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/approvals", h.list)
}

// approvalsListLimitDefault and approvalsListLimitMax mirror the
// proposals-list posture: the dashboard's M1 ask is
// "show pending approvals," and 50 is plenty for that. The cap
// keeps a misbehaving caller from pulling the whole table.
const (
	approvalsListLimitDefault = 50
	approvalsListLimitMax     = 200
)

// approvalsListResponse mirrors proposalsListResponse. The
// approvals slice is empty (not nil) when no rows match so the
// dashboard's renderer never has to nil-check.
//
// LastReceivedAt is the timestamp of the most recent matching
// approval (i.e. approvals[0].CreatedAt after the DESC sort).
// Absent (zero, with omitempty dropping it) when zero rows
// match. The dashboard's connection-status header reads it the
// same way it reads proposalsListResponse.LastReceivedAt.
// Approvals carry their owning domain. A management caller
// needs it to address the right runtime when reviewing — without it a
// review resolves the tenant's default domain and fails for anything
// else — and the dashboard needs it to label which domain a queued
// approval belongs to.
type approvalsListResponse struct {
	Approvals      []approvals.Approval `json:"approvals"`
	LastReceivedAt *time.Time           `json:"last_received_at,omitempty"`
}

// list handles GET /v1/approvals. Returns the most recent
// approvals for the caller's tenant, filtered by ?status= when
// supplied (default: pending), sorted DESC by CreatedAt, capped
// at min(?limit=, approvalsListLimitMax).
//
// Failure modes:
//
//   - identity missing or empty tenant: 401
//   - tenant has no domain configured yet: 200 with an empty queue
//     (pre-connection state, not a failure — mirrors proposals/receipts)
//   - tenant runtime resolution otherwise fails: 502 (matches the
//     existing TenantRuntimeHandler posture: credential is valid; the
//     tenant data plane is broken)
//   - approval store missing on the runtime: 503 (defensive — the
//     resolver always wires one, but the framework's runtime
//     allows nil here for non-approval-using domains)
//   - store-level error from List: 500 with a generic body
//   - unknown status value: 400
func (h *ApprovalsListHandler) list(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	statusFilter, err := parseApprovalStatus(r.URL.Query().Get("status"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), approvalsListLimitDefault, approvalsListLimitMax)

	rows, err := h.fetch(r, id)
	if err != nil {
		// A tenant that has not uploaded a domain yet has no runtime
		// to resolve — and therefore no approval queue. That is an
		// expected pre-connection state, not a data-plane failure, so
		// render an empty queue (200) the same way the proposals and
		// receipts list endpoints do. The dashboard shows "No pending
		// approvals" instead of an alarming 502.
		if errors.Is(err, domain.ErrDomainNotFound) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(approvalsListResponse{Approvals: []approvals.Approval{}})
			return
		}
		if errors.Is(err, errNoApprovalStore) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "approval store not configured"})
			return
		}
		h.logger.WarnContext(r.Context(), "approvals list: fetch approvals failed",
			slog.String("tenant", id.Tenant),
			slog.String("domain", id.DomainID),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tenant runtime unavailable"})
		return
	}
	rows = filterByStatus(rows, statusFilter)
	sortApprovalsDescByCreatedAt(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []approvals.Approval{}
	}

	resp := approvalsListResponse{Approvals: rows}
	if len(rows) > 0 {
		latest := rows[0].CreatedAt
		resp.LastReceivedAt = &latest
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// tenantApprovals pulls every approval for the tenant. Empty
// entityID on Approvals.List means "all entities," per the
// framework's contract (see approval.Store.List in
// pkg/kiff/approval/approval.go and the in-memory + Postgres
// reference implementations).
// fetch routes by identity, which is the whole of the domain-scoped-approvals design
//
//   - A domain-bound credential reads through the runtime resolved for
//     *its* domain, whose approval store carries the domain predicate.
//     It therefore sees its own domain and nothing else — including no
//     unattributed legacy rows, which is the intended fail-closed
//     behaviour rather than an omission.
//   - A management identity reads the tenant-wide view directly, across
//     every domain and including legacy rows, each annotated with its
//     owner. It must not borrow a domain runtime's store: that would
//     silently reduce the tenant-wide queue to one domain.
func (h *ApprovalsListHandler) fetch(r *http.Request, id auth.Identity) ([]approvals.Approval, error) {
	if id.DomainID != "" {
		rt, err := h.tenants.ForDomain(r.Context(), id.Tenant, id.DomainID)
		if err != nil {
			return nil, err
		}
		if rt == nil || rt.Approvals == nil {
			return nil, errNoApprovalStore
		}
		rows, err := rt.Approvals.List(r.Context(), "")
		if err != nil {
			return nil, err
		}
		// The store already filtered to this domain; annotate so the
		// response shape does not depend on which path served it.
		out := make([]approvals.Approval, 0, len(rows))
		for _, a := range rows {
			out = append(out, approvals.Approval{Approval: a, DomainID: id.DomainID})
		}
		return out, nil
	}

	if h.reader == nil {
		return nil, errNoApprovalStore
	}
	return h.reader.ListAll(r.Context(), id.Tenant, "")
}

// errNoApprovalStore surfaces a misconfiguration as a clean 503 rather
// than a nil-deref panic, and — for the management path — rather than a
// silent fallback to a single domain's view.
var errNoApprovalStore = errors.New("approval store not configured")

// parseApprovalStatus returns the requested filter, defaulting
// to pending when the query parameter is absent. Unknown values
// produce a typed error so the caller can render 400.
func parseApprovalStatus(raw string) (kiffapproval.Status, error) {
	if raw == "" {
		return kiffapproval.StatusPending, nil
	}
	switch kiffapproval.Status(raw) {
	case kiffapproval.StatusPending, kiffapproval.StatusGranted, kiffapproval.StatusDenied:
		return kiffapproval.Status(raw), nil
	}
	return "", errors.New("status must be one of: pending, granted, denied")
}

// filterByStatus returns the subset of approvals whose Status
// matches the filter. The function returns the original slice
// unchanged when filter is empty (no slot for that path today
// because parseApprovalStatus always returns a non-empty value;
// kept for forward-compat with a future "all statuses" query).
func filterByStatus(rows []approvals.Approval, filter kiffapproval.Status) []approvals.Approval {
	if filter == "" {
		return rows
	}
	out := rows[:0]
	for _, row := range rows {
		if row.Status == filter {
			out = append(out, row)
		}
	}
	return out
}

// sortApprovalsDescByCreatedAt sorts rows newest-first. The
// framework's in-memory store returns rows in append (oldest
// first) order; Postgres reads come back in whatever order the
// query specifies, which today is unsorted. The handler does
// the sort so the response shape is stable regardless of store.
func sortApprovalsDescByCreatedAt(rows []approvals.Approval) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
}
