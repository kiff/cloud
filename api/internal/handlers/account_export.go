package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// AccountHandler exposes the self-serve data-subject-rights surface:
//
//	GET  /v1/me/export — machine-readable JSON bundle of the caller's
//	                     account + tenant data (Articles 15/20).
//
// The erasure endpoint (POST /v1/me/delete, Article 17) lands in a
// follow-up slice and will register here too.
//
// Everything is tenant-scoped from the authenticated Identity; the
// handler never reads a tenant id from the request body or query, so
// a caller cannot export another tenant's data.
type AccountHandler struct {
	tenants tenant.Store
	keys    auth.APIKeyStore
	// sessions are the account's phone sessions, refused the moment a
	// deletion is accepted; nil: none to refuse.
	sessions auth.DeveloperSessionStore
	receipts receipt.Store
	// stepUp: deleting the account needs a recent sign-in.
	stepUp *auth.StepUp
	// cards and calls, when set, put the account's Cards with every
	// version, and every decision in its Record, in the export (the Data
	// Act's exportable data: the customer's own input and what came of it).
	cards ExportCards
	calls proposals.CallsReader
}

// ExportCards reads the account's Cards and each one's versions.
type ExportCards interface {
	List(ctx context.Context, tenantID string) ([]mandate.Mandate, error)
	Revisions(ctx context.Context, tenantID, mandateID string) ([]mandate.Revision, error)
}

// WithExportCards adds the account's Cards and their versions to the export.
func (h *AccountHandler) WithExportCards(c ExportCards) *AccountHandler {
	h.cards = c
	return h
}

// WithExportDecisions adds every decision in the account's Record.
func (h *AccountHandler) WithExportDecisions(c proposals.CallsReader) *AccountHandler {
	h.calls = c
	return h
}

// WithStepUp requires a recent sign-in to delete the account.
// WithSessions lets an accepted deletion sign every phone out of the
// account at once.
func (h *AccountHandler) WithSessions(s auth.DeveloperSessionStore) *AccountHandler {
	h.sessions = s
	return h
}

// RegisterPublic mounts what needs no session: finishing a deletion the
// account accepted, whose signature is its authority.
func (h *AccountHandler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/account-deletions/finish", h.finishDeletion)
	mux.HandleFunc("POST /v1/account-deletions/status", h.deletionStatus)
}

func (h *AccountHandler) WithStepUp(s *auth.StepUp) *AccountHandler {
	h.stepUp = s
	return h
}

// NewAccountHandler wires the stores the export bundle draws from.
// keys and receipts may be nil (e.g. a smoke run with no receipt
// store); the bundle renders the sections it can and omits the rest.
func NewAccountHandler(tenants tenant.Store, keys auth.APIKeyStore, receipts receipt.Store) *AccountHandler {
	return &AccountHandler{tenants: tenants, keys: keys, receipts: receipts}
}

// Register attaches the account routes to mux.
func (h *AccountHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/export", h.export)
	mux.HandleFunc("POST /v1/me/delete", h.delete)
	mux.HandleFunc("POST /v1/account-deletions", h.acceptDeletion)
}

// exportReceiptCap bounds the receipts pulled into a single export.
// Generous for v0.1 (a tenant with more than this many receipts is
// past the point where a single-shot export is the right tool);
// pagination is a follow-up if a real tenant outgrows it.
var exportReceiptCap = 10000

// ExportSchemaVersion identifies the bundle shape so a consumer can
// branch on forward-compatible additions.
const ExportSchemaVersion = "kiff.export.v1"

// exportBundle is the body of GET /v1/me/export. It carries only
// data the caller is the controller (or sole tenant admin) of. The
// tenant's RedactionSalt is excluded automatically — Tenant marshals
// it as json:"-" — and API keys carry hashes and metadata only,
// never plaintext (the plaintext is never persisted to begin with).
type exportBundle struct {
	SchemaVersion string            `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Subject       string            `json:"subject"`
	Email         string            `json:"email,omitempty"`
	Tenant        tenant.Tenant     `json:"tenant"`
	APIKeys       []exportAPIKey    `json:"api_keys"`
	Receipts      []receipt.Receipt `json:"receipts"`
	// Cards are every Card of the account, in force or not, each with
	// every version it was issued in, oldest first.
	Cards []exportCard `json:"cards"`
	// Decisions are every call KIFF decided for the account (the Record),
	// newest first, up to exportReceiptCap.
	Decisions []proposals.CallRow `json:"decisions"`
	// Truncated says which lists stopped at exportReceiptCap with more
	// left: a Data Act export says when it isn't complete (the Terms offer
	// the rest on request). Absent when both are complete.
	Truncated *exportTruncated `json:"truncated,omitempty"`
}

// exportTruncated is which lists an export cut short, and where.
type exportTruncated struct {
	Receipts  bool `json:"receipts"`
	Decisions bool `json:"decisions"`
	// Limit is how many of each the export holds at most.
	Limit int `json:"limit"`
}

// exportCard is a Card and its versions.
type exportCard struct {
	Card     mandate.Mandate    `json:"card"`
	Versions []mandate.Revision `json:"versions"`
}

// exportAPIKey is the export-safe projection of an APIKeyRecord. The
// hash is included (it is not a credential — the plaintext that
// hashes to it is never stored); the raw [20]byte array is rendered
// as hex rather than a JSON array of ints.
type exportAPIKey struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Roles      []string   `json:"roles"`
	KeyHash    string     `json:"key_hash"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// export handles GET /v1/me/export. Read-only; no state change.
// Returns 401 when the caller has no identity or no tenant, or when
// the tenant row is gone (ErrNotFound — mirrors MeHandler so the
// dashboard treats it as "sign in again"); 410 Gone when the tenant
// has been hard-deleted (row retained, schema dropped — nothing to
// export); 500 on a store error. A deleted_pending tenant can still
// export during the grace window.
func (h *AccountHandler) export(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	// The account's data is its owner's to take, not a runtime
	// or agent key's: the bundle holds every receipt and key record.
	if !can(id, membership.CapExport) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "exporting the account requires the tenant_owner or admin role, from a session or a key not bound to an agent",
		})
		return
	}

	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// A hard-deleted tenant keeps its cloud_meta.tenants row (status
	// deleted) but its per-tenant schema is dropped, so an export
	// would be a misleadingly empty bundle. Return 410 Gone — the
	// same signal the delete endpoint gives for an already-scrubbed
	// tenant. A deleted_pending tenant is still inside the grace
	// window with its data retained, so export remains available
	// (Articles 15/20 — the owner can pull their data right up to the
	// hard scrub).
	if t.Status == tenant.StatusDeleted {
		writeJSON(w, http.StatusGone, map[string]string{
			"error": "tenant has been deleted; no data to export",
		})
		return
	}

	keys := []exportAPIKey{}
	if h.keys != nil {
		records, err := h.keys.ListActive(r.Context(), id.Tenant)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		for _, rec := range records {
			keys = append(keys, exportAPIKey{
				ID:         rec.ID,
				Label:      rec.Label,
				Roles:      rec.Roles,
				KeyHash:    rec.HexHash(),
				CreatedAt:  rec.CreatedAt,
				LastUsedAt: rec.LastUsedAt,
				RevokedAt:  rec.RevokedAt,
			})
		}
	}

	receipts, receiptsCut := []receipt.Receipt{}, false
	if h.receipts != nil {
		// One past the cap tells whether more are left.
		rows, err := h.receipts.List(r.Context(), id.Tenant, exportReceiptCap+1)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if len(rows) > exportReceiptCap {
			rows, receiptsCut = rows[:exportReceiptCap], true
		}
		if rows != nil {
			receipts = rows
		}
	}

	cards := []exportCard{}
	if h.cards != nil {
		ms, err := h.cards.List(r.Context(), id.Tenant)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the account's Cards could not be read"})
			return
		}
		for _, m := range ms {
			revs, err := h.cards.Revisions(r.Context(), id.Tenant, m.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "a Card's versions could not be read"})
				return
			}
			if revs == nil {
				revs = []mandate.Revision{}
			}
			cards = append(cards, exportCard{Card: m, Versions: revs})
		}
	}

	decisions, decisionsCut := []proposals.CallRow{}, false
	if h.calls != nil {
		// Page through the whole Record, never only its first page: an
		// export that stopped early would read as complete.
		for len(decisions) < exportReceiptCap {
			page, err := h.calls.ListCalls(r.Context(), id.Tenant, proposals.CallQuery{Show: proposals.CallsAll,
				Limit: proposals.MaxLimit, Offset: len(decisions)})
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the account's decisions could not be read"})
				return
			}
			decisions = append(decisions, page.Calls...)
			if len(page.Calls) < proposals.MaxLimit {
				break
			}
		}
		if len(decisions) > exportReceiptCap {
			decisions, decisionsCut = decisions[:exportReceiptCap], true
		} else if len(decisions) == exportReceiptCap {
			// Exactly at the cap: one more says whether the Record goes on.
			more, err := h.calls.ListCalls(r.Context(), id.Tenant, proposals.CallQuery{Show: proposals.CallsAll, Limit: 1, Offset: exportReceiptCap})
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the account's decisions could not be read"})
				return
			}
			decisionsCut = len(more.Calls) > 0
		}
	}

	bundle := exportBundle{
		SchemaVersion: ExportSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Subject:       id.Subject,
		Email:         id.Email,
		Tenant:        t,
		APIKeys:       keys,
		Receipts:      receipts,
		Cards:         cards,
		Decisions:     decisions,
	}
	if receiptsCut || decisionsCut {
		bundle.Truncated = &exportTruncated{Receipts: receiptsCut, Decisions: decisionsCut, Limit: exportReceiptCap}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="kiff-data-export.json"`)
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(bundle)
}

// ErrTenantErasing is returned by the decide path when the tenant
// is in StatusDeletedPending or StatusDeleted. Stable error so
// callers can detect it without parsing a message string.
var ErrTenantErasing = errors.New("tenant: deletion in progress")

// ErrTenantSuspended is returned by the decide path when the tenant
// is in StatusSuspended — an operator has held it offline.
// Stable, distinct from ErrTenantErasing so callers and the operator
// console can tell "suspended" (reversible) from "deleting".
var ErrTenantSuspended = errors.New("tenant: suspended")

// deleteResponse is the body of a successful POST /v1/me/delete.
type deleteResponse struct {
	Status          string `json:"status"`
	DeletedAt       string `json:"deleted_at"`
	GraceWindowEnds string `json:"grace_window_ends"`
	Message         string `json:"message"`
}

// delete handles POST /v1/me/delete (admin role required).
// Moves the tenant from active → deleted_pending, revokes all API
// keys, and stamps grace-window metadata. The per-tenant schema is
// retained; the hard scrub runs after the grace window via the
// operator CLI or a future automated sweep.
//
// Idempotent: a second call on a deleted_pending tenant returns
// the existing grace-window timestamps rather than resetting them.
func (h *AccountHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	// A key bound to an agent acts as that agent and never deletes the
	// account, whatever its roles.
	if !id.HasRole("admin") || id.AgentID != "" || (id.MemberRole != "" && !can(id, membership.CapDelete)) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "account deletion requires the admin role, from a session or a key not bound to an agent",
		})
		return
	}
	// From the app, the phone's signature over this request is the recent
	// sign-in: the owner's act behind Face ID (kiff/tafs#185, the governance-roles design).
	if h.stepUp != nil && !auth.DeviceSigned(r.Context()) {
		if _, err := h.stepUp.Check(r.Context(), id); err != nil {
			auth.WriteReauthRequired(w)
			return
		}
	}

	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Already soft-deleted: idempotent — return the existing grace
	// timestamps rather than resetting the clock.
	if t.Status == tenant.StatusDeletedPending {
		deletedAt, _ := t.Metadata[tenant.MetadataKeyDeletedAt].(string)
		graceEnds, _ := t.Metadata[tenant.MetadataKeyGraceWindowEnds].(string)
		writeJSON(w, http.StatusOK, deleteResponse{
			Status:          string(t.Status),
			DeletedAt:       deletedAt,
			GraceWindowEnds: graceEnds,
			Message:         "deletion already requested; schema retained until grace window expires",
		})
		return
	}
	if t.Status == tenant.StatusDeleted {
		writeJSON(w, http.StatusGone, map[string]string{
			"error": "tenant has already been hard-deleted",
		})
		return
	}

	// Where it came from decides whether it can be restored: the tafs app
	// erased its chats and connections first, so its deletion can't be.
	via := tenant.DeletedViaWeb
	if len(id.DeviceKey) > 0 || auth.DeviceSigned(r.Context()) {
		via = tenant.DeletedViaApp
	}
	resp, err := softDeleteTenant(r.Context(), h.tenants, h.keys, id.Tenant, via)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// softDeleteTenant adapts the shared tenant.SoftDelete primitive
// to this handler's HTTP response shape. The
// primitive lives in the tenant package so the self-serve handler
// and the kiff-tenant CLI cannot drift: it revokes all API
// keys, flips the status to StatusDeletedPending, and stamps
// deleted_at + grace_window_ends. The per-tenant schema is retained;
// the hard scrub runs after the grace window via Deprovision.
// Idempotent at the status level: callers check status before
// calling and handle deleted_pending themselves.
func softDeleteTenant(ctx context.Context, tenants tenant.Store, keys auth.APIKeyStore, tenantID, via string) (deleteResponse, error) {
	res, err := tenant.SoftDelete(ctx, tenants, keys, tenantID, via)
	if err != nil {
		return deleteResponse{}, err
	}
	return deleteResponse{
		Status:          string(res.Status),
		DeletedAt:       res.DeletedAt.Format(time.RFC3339),
		GraceWindowEnds: res.GraceWindowEnds.Format(time.RFC3339),
		Message:         fmt.Sprintf("account scheduled for deletion; data retained for %d days", tenant.GracePeriodDays),
	}, nil
}
