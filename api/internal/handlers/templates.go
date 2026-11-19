package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/templates"
)

// TemplatesHandler exposes the cloud's curated starter-domain
// surface:
//
//	GET  /v1/templates                — list available templates
//	POST /v1/me/domain/template       — apply a template to the
//	                                    caller's tenant
//
// The list endpoint sits behind the auth middleware (it returns
// the same data to every caller, but we don't expose the catalog
// to anonymous traffic in v0.1; if a public unauthenticated
// listing is ever wanted, the dashboard's onboarding panel can
// consume that via a different route).
//
// Apply takes the tenant from the authenticated identity, never
// from the request body. There is deliberately no
// `--tenant=<id>` style override: a tenant can only apply
// templates against its own domain.
type TemplatesHandler struct {
	source   domain.Source
	controls domain.ControlStore
	registry *executors.Registry
	runtimes RuntimeResetter
	logger   *slog.Logger
}

// RuntimeResetter is the subset of runtime.TenantRuntime the
// handler needs: invalidate the cached runtime so the next
// resolution picks up the freshly-applied YAML. Pulled into its
// own interface so tests can stub it without dragging in the
// pgxpool internals.
type RuntimeResetter interface {
	ResetTenant(tenantID string)
}

// TemplatesOption configures TemplatesHandler.
type TemplatesOption func(*TemplatesHandler)

// WithTemplatesLogger substitutes the logger.
func WithTemplatesLogger(l *slog.Logger) TemplatesOption {
	return func(h *TemplatesHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithTemplatesControlStore records a normal control activation when a
// starter template becomes the tenant's active domain. Without this,
// the runtime changes but the domain evidence surface still points at
// the control for the domain that the template replaced.
func WithTemplatesControlStore(s domain.ControlStore) TemplatesOption {
	return func(h *TemplatesHandler) { h.controls = s }
}

// NewTemplatesHandler returns a handler ready to register.
// source is the cloud's domain.Source (Postgres in production,
// in-memory in tests). registry is the executors registry the
// apply path validates against (typically executors.DefaultRegistry()).
// runtimes is optional: when nil, applying a template still
// upserts the YAML but does not invalidate cached runtimes;
// production callers wire the cloud's tenant runtime cache.
func NewTemplatesHandler(source domain.Source, registry *executors.Registry, runtimes RuntimeResetter, opts ...TemplatesOption) *TemplatesHandler {
	h := &TemplatesHandler{
		source:   source,
		registry: registry,
		runtimes: runtimes,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register attaches the handler's routes to mux. Idempotent if
// mux has not seen the patterns before.
func (h *TemplatesHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/templates", h.list)
	mux.HandleFunc("POST /v1/me/domain/template", h.apply)
}

// listResponse mirrors the response body shape the dashboard
// consumes. Defined here (not in the templates package) so the
// templates package stays free of HTTP concerns.
type listResponse struct {
	Templates []templates.Template `json:"templates"`
}

// list handles GET /v1/templates. The response includes every
// template's slug, name, description, and sample event payload.
// The YAML body is not exposed by this endpoint — fetching the
// raw YAML for a template is a follow-up if any caller asks.
//
// Authentication is required (cheaper than carrying a public
// /v1/templates surface), but the response is the same for every
// caller.
func (h *TemplatesHandler) list(w http.ResponseWriter, r *http.Request) {
	if _, err := auth.IdentityFrom(r.Context()); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	all := templates.List()
	writeJSON(w, http.StatusOK, listResponse{Templates: all})
}

// applyRequest is the body of POST /v1/me/domain/template.
type applyRequest struct {
	Slug string `json:"slug"`
}

// applyResponse is the body returned on a successful apply.
type applyResponse struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Tenant  string `json:"tenant"`
	Version int    `json:"version"`
}

// apply handles POST /v1/me/domain/template. Resolves the
// tenant from the auth context, validates the template's YAML
// against the cloud's executor registry (defence in depth — the
// templates package's tests already enforce this at build time,
// but we don't trust the catalog blindly at runtime), upserts
// the YAML into cloud_meta.tenant_domains, records the activation
// control, and resets the tenant's cached runtime so the next event
// picks up the new domain.
//
// Failure modes:
//   - identity missing or empty tenant: 401
//   - body is not valid JSON or slug is empty: 400
//   - slug does not match a registered template: 404
//   - YAML somehow no longer parses against the cloud's registry:
//     500 (this is a build-time invariant violation — the catalog
//     must always parse — and surfaces in logs as a serious bug,
//     not a user error)
//   - upsert into cloud_meta.tenant_domains fails: 500
//
// The endpoint is idempotent: applying the same template twice
// upserts the same YAML and bumps the version twice. That's the
// existing domain.Source contract; the runtime cache invalidation
// is correct for both the first apply and subsequent re-applies.
func (h *TemplatesHandler) apply(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	if !requireManagement(w, id) {
		return
	}

	var req applyRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
	}
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug is required"})
		return
	}

	tmpl, err := templates.Get(slug)
	if err != nil {
		if errors.Is(err, templates.ErrTemplateNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "template not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "template lookup failed",
			slog.String("slug", slug),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "template lookup failed"})
		return
	}

	// Defence in depth. The templates_test enforces this at build
	// time, but a fresh registry with custom executors removed
	// could be wired into the cloud — better to fail at apply
	// than silently persist an invalid YAML.
	parsed, err := domain.ParseAndBuild(tmpl.YAML, h.registry)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "template YAML failed validation against cloud registry",
			slog.String("slug", slug),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "template invalid against cloud registry"})
		return
	}

	if err := h.source.Save(r.Context(), id.Tenant, tmpl.YAML); err != nil {
		h.logger.ErrorContext(r.Context(), "save tenant domain failed",
			slog.String("tenant", id.Tenant),
			slog.String("slug", slug),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save tenant domain failed"})
		return
	}

	// Invalidate the cached runtime so the next event picks up
	// the new domain. Skipping when no resetter is wired keeps
	// the handler usable in tests with the bare in-memory source.
	if h.runtimes != nil {
		h.runtimes.ResetTenant(id.Tenant)
	}

	// A template is another way to activate a domain. Record the same
	// control lineage as direct upload and Studio activation so the
	// domain's Activity link resolves to the contract now in force.
	if h.controls != nil {
		ctrl := NewControlFromParse(id.Tenant, id.Subject, parsed)
		if cerr := activateControl(r.Context(), h.controls, ctrl); cerr != nil {
			h.logger.WarnContext(r.Context(), "template: create control record failed",
				slog.String("tenant", id.Tenant),
				slog.String("domain", parsed.Spec.Domain),
				slog.String("err", cerr.Error()),
			)
		}
	}

	td, err := h.source.Load(r.Context(), id.Tenant)
	if err != nil {
		// Read-after-write failed: the upsert succeeded but we
		// can't echo the version. The apply is durable, so
		// surface a 200 with version 0 rather than a misleading
		// 500.
		h.logger.WarnContext(r.Context(), "template applied but version read-back failed",
			slog.String("tenant", id.Tenant),
			slog.String("slug", slug),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusOK, applyResponse{
			Slug:   tmpl.Slug,
			Name:   tmpl.Name,
			Tenant: id.Tenant,
		})
		return
	}

	writeJSON(w, http.StatusOK, applyResponse{
		Slug:    tmpl.Slug,
		Name:    tmpl.Name,
		Tenant:  id.Tenant,
		Version: td.Version,
	})
}

// writeJSON is the local helper for the templates handler.
// Mirrors the auth package's writeJSON (we don't import that one
// because it's unexported there). Sets Content-Type, writes the
// status, and JSON-encodes the body. Encoding errors are
// swallowed because we've already written the status; logging
// would just clutter the path.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
