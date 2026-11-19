package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/tenant"
)

// DraftHandler exposes the cloud's tenant-managed-DRAFT surface
// for KIFF Studio (the Studio MVP design MVP-2):
//
//	GET    /v1/me/domain/draft     — return the tenant's current
//	                                  draft YAML (raw bytes), or
//	                                  404 when no draft exists.
//	PUT    /v1/me/domain/draft     — upsert a draft from the
//	                                  request body. Validation is
//	                                  best-effort: if the YAML is
//	                                  shape-correct enough to parse
//	                                  the response carries the
//	                                  parsed summary; if not, the
//	                                  draft still saves (drafts may
//	                                  be invalid mid-edit) and the
//	                                  response carries the issues.
//	DELETE /v1/me/domain/draft     — discard the tenant's draft.
//	                                  Used when a user clicks "back
//	                                  to template view" or similar.
//	POST   /v1/me/domain/draft/activate
//	                                — promote the draft to active
//	                                  in a single transaction:
//	                                  upsert tenant_domains, delete
//	                                  draft, reset runtime cache.
//	                                  Resolver never sees a missing-
//	                                  domain window.
//
// Trust boundary mirrors DomainHandler: tenant always comes from
// the authenticated identity, never from the body. The draft
// surface does NOT enforce executor-registry membership at PUT
// time — that check runs at activate. A draft can reference an
// unknown executor mid-edit; the activate path will reject it
// with the canonical 422.
//
// Why a separate handler from DomainHandler:
//
//   - DomainHandler's GET / PUT operate on the active row and
//     are authoritative for the runtime resolver. A failed PUT
//     leaves the prior YAML in place.
//   - DraftHandler operates on the auto-save row. Its semantics
//     are "always accept; the user is mid-edit." Mixing the two
//     would muddle the runtime contract with the editor UX.
//
// The two share the underlying source/registry/runtimes deps so
// activate can reuse the same primitives.
type DraftHandler struct {
	source   domain.Source
	drafts   domain.DraftSource
	registry *executors.Registry
	runtimes RuntimeResetter
	pool     *pgxpool.Pool
	controls domain.ControlStore // may be nil; control records are best-effort
	logger   *slog.Logger

	maxYAMLBytes int64
}

// DraftOption configures DraftHandler.
type DraftOption func(*DraftHandler)

// WithDraftLogger substitutes the logger.
func WithDraftLogger(l *slog.Logger) DraftOption {
	return func(h *DraftHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithDraftMaxBytes overrides the per-request YAML body cap.
func WithDraftMaxBytes(n int64) DraftOption {
	return func(h *DraftHandler) {
		if n > 0 {
			h.maxYAMLBytes = n
		}
	}
}

// WithDraftControlStore wires the control store so activations
// create a control record automatically.
func WithDraftControlStore(s domain.ControlStore) DraftOption {
	return func(h *DraftHandler) {
		h.controls = s
	}
}

// NewDraftHandler wires the dependencies. pool may be nil — the
// activate path falls back to a non-transactional save+delete
// when no pool is provided (used by tests with the in-memory
// sources). In production the pool is always set, so activate
// runs as a single transaction.
func NewDraftHandler(
	source domain.Source,
	drafts domain.DraftSource,
	registry *executors.Registry,
	runtimes RuntimeResetter,
	pool *pgxpool.Pool,
	opts ...DraftOption,
) *DraftHandler {
	h := &DraftHandler{
		source:       source,
		drafts:       drafts,
		registry:     registry,
		runtimes:     runtimes,
		pool:         pool,
		logger:       slog.Default(),
		maxYAMLBytes: 256 * 1024,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register attaches the draft routes to mux.
func (h *DraftHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/domain/draft", h.get)
	mux.HandleFunc("PUT /v1/me/domain/draft", h.put)
	mux.HandleFunc("DELETE /v1/me/domain/draft", h.delete)
	mux.HandleFunc("POST /v1/me/domain/draft/activate", h.activate)
}

// draftResponse is the body returned by GET / PUT on success.
// Mirrors validateResponse (the active-domain shape) so a client
// rendering the draft can use the same view code, plus an
// updated_at and a per-call issues list (drafts may be invalid).
type draftResponse struct {
	YAML      string                  `json:"yaml"`
	UpdatedAt int64                   `json:"updated_at"`
	Parsed    *validateResponse       `json:"parsed,omitempty"`
	Issues    []validateIssueResponse `json:"issues,omitempty"`
}

// get returns the tenant's current draft YAML.
func (h *DraftHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}

	td, err := h.drafts.Load(r.Context(), id.Tenant)
	if err != nil {
		if errors.Is(err, domain.ErrDraftNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no draft"})
			return
		}
		h.logger.ErrorContext(r.Context(), "draft: load failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load draft failed"})
		return
	}
	resp := draftResponse{
		YAML:      string(td.YAML),
		UpdatedAt: td.UpdatedAt,
	}
	if parsed, perr := domain.ParseAndBuild(td.YAML, h.registry); perr == nil {
		summary := summaryFromParse(parsed, 0)
		resp.Parsed = &summary
	} else {
		resp.Issues = issuesFromError(perr)
	}
	writeJSON(w, http.StatusOK, resp)
}

// put upserts the tenant's draft. Always saves; validation is
// best-effort and reported in the response.
func (h *DraftHandler) put(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}

	yamlBytes, ok := h.readYAMLBody(w, r)
	if !ok {
		return
	}

	if err := h.drafts.Save(r.Context(), id.Tenant, yamlBytes); err != nil {
		h.logger.ErrorContext(r.Context(), "draft: save failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save draft failed"})
		return
	}

	resp := draftResponse{
		YAML: string(yamlBytes),
	}
	td, lerr := h.drafts.Load(r.Context(), id.Tenant)
	if lerr == nil {
		resp.UpdatedAt = td.UpdatedAt
	}
	if parsed, perr := domain.ParseAndBuild(yamlBytes, h.registry); perr == nil {
		summary := summaryFromParse(parsed, 0)
		resp.Parsed = &summary
	} else {
		resp.Issues = issuesFromError(perr)
	}
	writeJSON(w, http.StatusOK, resp)
}

// delete discards the tenant's draft.
func (h *DraftHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	if err := h.drafts.Delete(r.Context(), id.Tenant); err != nil {
		h.logger.ErrorContext(r.Context(), "draft: delete failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete draft failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// activate promotes the draft to active. Validates one last time
// against the executor registry; if invalid, the draft stays put
// and the response carries the issues. On success: upsert the
// active row, delete the draft, reset the runtime cache.
//
// Transactional path (pool set): the upsert and delete share a
// single BEGIN/COMMIT so the resolver, which only reads the
// active table, never sees a missing-domain window.
//
// Non-transactional fallback (pool nil, used by in-memory tests):
// upsert active, then delete draft as separate calls. The
// resolver still sees no gap because the upsert lands first.
func (h *DraftHandler) activate(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	if !requireManagement(w, id) {
		return
	}

	td, err := h.drafts.Load(r.Context(), id.Tenant)
	if err != nil {
		if errors.Is(err, domain.ErrDraftNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no draft to activate"})
			return
		}
		h.logger.ErrorContext(r.Context(), "draft: activate-load failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load draft failed"})
		return
	}

	parsed, err := domain.ParseAndBuild(td.YAML, h.registry)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, validateErrorResponse{
			Error:  "draft fails validation; cannot activate",
			Issues: issuesFromError(err),
		})
		return
	}

	if err := h.commitActivate(r.Context(), id.Tenant, td.YAML); err != nil {
		h.logger.ErrorContext(r.Context(), "draft: activate failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "activate draft failed"})
		return
	}

	if h.runtimes != nil {
		h.runtimes.ResetTenant(id.Tenant)
	}

	// Create a control record for this activation. Best-effort:
	// a failure here does not roll back the domain activation.
	// The control record is the product audit trail; the domain
	// is the runtime contract. They are independent concerns.
	if h.controls != nil {
		ctrl := NewControlFromParse(id.Tenant, id.Subject, parsed)
		if cerr := activateControl(r.Context(), h.controls, ctrl); cerr != nil {
			h.logger.WarnContext(r.Context(), "draft: create control record failed",
				slog.String("tenant", id.Tenant),
				slog.String("domain", parsed.Spec.Domain),
				slog.String("err", cerr.Error()),
			)
		}
	}

	// Read the new version of the active row for the response.
	version := 0
	if active, lerr := h.source.Load(r.Context(), id.Tenant); lerr == nil {
		version = active.Version
	}

	h.logger.InfoContext(r.Context(), "draft: activated",
		slog.String("tenant", id.Tenant),
		slog.String("domain", parsed.Spec.Domain),
		slog.Int("version", version),
	)

	writeJSON(w, http.StatusOK, summaryFromParse(parsed, version))
}

// commitActivate runs the upsert + draft-delete. When pool is
// set, both happen in one transaction. Otherwise they run as
// separate calls (in-memory tests).
func (h *DraftHandler) commitActivate(ctx context.Context, tenantID string, yaml []byte) error {
	// The Source / DraftSource interfaces don't expose
	// transactions; the Postgres implementations write to
	// distinct tables, so the cleanest path is to run a tx
	// scoped to this handler when the pool is available.
	if h.pool != nil {
		return h.commitActivateTx(ctx, tenantID, yaml)
	}
	if err := h.source.Save(ctx, tenantID, yaml); err != nil {
		return err
	}
	return h.drafts.Delete(ctx, tenantID)
}

// commitActivateTx runs the activate as a single Postgres
// transaction. The active-row upsert reuses
// domain.TenantDomainUpsertSQL — the same statement
// PostgresSource.Save runs on the legacy active-row path — so a
// future schema change only has to land in one place. The
// transaction also deletes the draft row, so the resolver
// (which reads only the active table) never sees a missing-domain
// window.
func (h *DraftHandler) commitActivateTx(ctx context.Context, tenantID string, yaml []byte) error {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	domainID, slug := domain.DeriveStorageIdentity(yaml)
	if _, err := tx.Exec(ctx, domain.TenantDomainUpsertSQL, tenantID, domainID, slug, yaml); err != nil {
		return err
	}

	const del = `DELETE FROM ` + tenant.MetaSchema + `.tenant_domain_drafts WHERE tenant_id = $1`
	if _, err := tx.Exec(ctx, del, tenantID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *DraftHandler) requireIdentity(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return auth.Identity{}, false
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return auth.Identity{}, false
	}
	return id, true
}

func (h *DraftHandler) readYAMLBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	limit := h.maxYAMLBytes
	if limit <= 0 {
		limit = 256 * 1024
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	defer body.Close()
	yamlBytes, err := io.ReadAll(body)
	if err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": "yaml body exceeds limit",
			})
			return nil, false
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "could not read yaml body",
		})
		return nil, false
	}
	if len(strings.TrimSpace(string(yamlBytes))) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "yaml body is empty",
		})
		return nil, false
	}
	return yamlBytes, true
}
