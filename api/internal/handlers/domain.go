package handlers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
)

// DomainHandler exposes the cloud's tenant-managed-domain surface:
//
//	POST /v1/me/domain/validate  — parse + validate YAML without
//	                                persisting. Used by clients
//	                                that want a dry-run before
//	                                they commit.
//	PUT  /v1/me/domain           — validate + upload + activate
//	                                the YAML for the caller's tenant.
//	GET  /v1/me/domain           — return the active domain's
//	                                summary (slug-style domain name,
//	                                entity, version, action shape).
//
// This is the boundary it closes: a tenant cannot describe
// what KIFF should govern unless they can upload a domain
// definition. Templates remain the curated path for
// onboarding; this endpoint is the integrator path.
//
// Trust boundary:
//
//   - the tenant id always comes from the authenticated identity,
//     never from the body or the URL. There is no per-request
//     tenant override on either route.
//   - the YAML body is parsed and validated against the cloud's
//     executor registry before it touches storage. Unknown
//     executor keys are a hard reject. Tenants cannot upload
//     arbitrary Go code; the registry is the only set of
//     executors the cloud will honor.
//   - validation errors leave the previously-active YAML in place.
//     A bad PUT can never zero out a tenant's domain.
//   - on a successful PUT, the tenant's runtime cache is reset so
//     the next event picks up the new YAML; any cached *Runtime
//     built from the old version is dropped.
//
// What the handler does NOT do:
//
//   - it does not run anything against the new domain at apply
//     time. Validation is YAML-shape + registry membership; the
//     framework's deeper checks (state reachability, contract
//     completeness) run in domain.ParseAndBuild and surface here
//     as 422s.
//   - it does not version-history beyond the monotonic Source
//     version. Rollback is a separate slice; v0.1 is "uploads
//     replace, the version proves order."
type DomainHandler struct {
	source   domain.Source
	registry *executors.Registry
	runtimes RuntimeResetter
	controls domain.ControlStore // may be nil
	logger   *slog.Logger

	// maxYAMLBytes caps the request body the handler will read.
	// 256 KiB is generous for v0.1; the default starter templates
	// are <2 KiB each.
	maxYAMLBytes int64
}

// DomainOption configures DomainHandler.
type DomainOption func(*DomainHandler)

// WithDomainLogger substitutes the logger.
func WithDomainLogger(l *slog.Logger) DomainOption {
	return func(h *DomainHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithDomainMaxBytes overrides the per-request YAML body cap.
func WithDomainMaxBytes(n int64) DomainOption {
	return func(h *DomainHandler) {
		if n > 0 {
			h.maxYAMLBytes = n
		}
	}
}

// WithDomainControlStore wires the control store so direct PUT
// activations also create a control record.
func WithDomainControlStore(s domain.ControlStore) DomainOption {
	return func(h *DomainHandler) {
		h.controls = s
	}
}

// NewDomainHandler wires the dependencies. source is the cloud's
// domain.Source (Postgres in production, in-memory in tests).
// registry is the cloud's executor registry (typically
// executors.DefaultRegistry()). runtimes may be nil: when wired,
// successful uploads invalidate the cached runtime so the next
// event picks up the new YAML.
func NewDomainHandler(source domain.Source, registry *executors.Registry, runtimes RuntimeResetter, opts ...DomainOption) *DomainHandler {
	h := &DomainHandler{
		source:       source,
		registry:     registry,
		runtimes:     runtimes,
		logger:       slog.Default(),
		maxYAMLBytes: 256 * 1024,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register attaches the handler's routes to mux.
func (h *DomainHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/me/domain/validate", h.validate)
	mux.HandleFunc("PUT /v1/me/domain", h.upload)
	mux.HandleFunc("GET /v1/me/domain", h.get)
}

// validateResponse is the body returned by validate + upload on
// success. It echoes the parsed domain shape so callers can
// confirm the cloud read what they meant.
type validateResponse struct {
	Domain   string                `json:"domain"`
	Entity   string                `json:"entity"`
	Version  int                   `json:"version,omitempty"`
	Events   []string              `json:"events"`
	States   []string              `json:"states"`
	Actions  []validateActionShape `json:"actions"`
	Warnings []string              `json:"warnings,omitempty"`
}

// validateActionShape is the per-action summary echoed back to
// the caller. Fields stay flat so the response is easy to inspect.
type validateActionShape struct {
	Name               string   `json:"name"`
	AllowedStates      []string `json:"allowed_states"`
	RequiredParameters []string `json:"required_parameters,omitempty"`
	Risk               string   `json:"risk"`
	Approval           string   `json:"approval"`
	Executor           string   `json:"executor"`
	// RunContext echoes a declared run-context dependency.
	//
	// It is here because this response is where an author checks that
	// what they wrote took effect. Validating a domain and getting the
	// actions back with the rule silently missing is the same defect
	// this control exists to prevent: something that reads as governed
	// and is not. Omitted when the action declares none, so a domain
	// written before the trusted-run-context design validates to byte-identical output.
	RunContext *validateRunContextShape `json:"run_context,omitempty"`
	// RequiresAuthority echoes the issued-authority design precondition, for the same
	// reason RunContext is echoed: this response is where an author
	// confirms the rule took effect, and a rule that is enforced and
	// invisible here is the defect the echo exists to catch. Omitted
	// when false so a domain written before the issued-authority design validates to
	// byte-identical output.
	RequiresAuthority bool `json:"requires_authority,omitempty"`
}

// validateRunContextShape mirrors domain.RunContextSpec on the wire.
//
// Every declared condition has to appear here. A condition missing
// from this shape is invisible to the author checking that what they
// wrote took effect, which is the defect this echo exists to prevent
// — and it is silent, because the rule is still enforced. Both fields
// omit when unset so an action declaring one condition does not echo
// an empty second one as though it had declared both.
type validateRunContextShape struct {
	OnUntrustedInput         string `json:"on_untrusted_input,omitempty"`
	OnUntrustedSensitiveRead string `json:"on_untrusted_sensitive_read,omitempty"`
}

// validateErrorResponse is the body returned when the YAML fails
// validation. Errors are flattened to a list of {path, message}
// pairs so a tenant editing their YAML can locate every problem
// in one round-trip.
type validateErrorResponse struct {
	Error  string                  `json:"error"`
	Issues []validateIssueResponse `json:"issues,omitempty"`
}

type validateIssueResponse struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// validate handles POST /v1/me/domain/validate. Parses the
// request body, runs ParseAndBuild against the cloud's registry,
// and returns the parsed shape on success or a 422 with the list
// of issues on failure. The endpoint never touches storage.
func (h *DomainHandler) validate(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}

	yamlBytes, ok := h.readYAMLBody(w, r)
	if !ok {
		return
	}

	parsed, err := domain.ParseAndBuild(yamlBytes, h.registry)
	if err != nil {
		h.writeValidationError(w, err)
		return
	}

	h.logger.InfoContext(r.Context(), "domain: validate ok",
		slog.String("tenant", id.Tenant),
		slog.String("domain", parsed.Spec.Domain),
	)
	writeJSON(w, http.StatusOK, summaryFromParse(parsed, 0))
}

// upload handles PUT /v1/me/domain. Validates the YAML against
// the cloud's registry, persists it via Source.Save, resets the
// tenant's runtime cache, and returns the new version.
//
// On validation failure the storage is untouched.
func (h *DomainHandler) upload(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	if !requireManagement(w, id) {
		return
	}

	yamlBytes, ok := h.readYAMLBody(w, r)
	if !ok {
		return
	}

	parsed, err := domain.ParseAndBuild(yamlBytes, h.registry)
	if err != nil {
		h.writeValidationError(w, err)
		return
	}

	if err := h.source.Save(r.Context(), id.Tenant, yamlBytes); err != nil {
		h.logger.ErrorContext(r.Context(), "domain: save failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save tenant domain failed"})
		return
	}

	// Invalidate the cached runtime only after the save succeeds.
	// If we'd reset before the save and the save then failed, the
	// next event would rebuild from the previous YAML — fine
	// semantically, but a needless cache flush.
	if h.runtimes != nil {
		h.runtimes.ResetTenant(id.Tenant)
	}

	// Create a control record for this activation. Best-effort.
	if h.controls != nil {
		ctrl := NewControlFromParse(id.Tenant, id.Subject, parsed)
		if cerr := activateControl(r.Context(), h.controls, ctrl); cerr != nil {
			h.logger.WarnContext(r.Context(), "domain: create control record failed",
				slog.String("tenant", id.Tenant),
				slog.String("domain", parsed.Spec.Domain),
				slog.String("err", cerr.Error()),
			)
		}
	}

	td, err := h.source.Load(r.Context(), id.Tenant)
	version := 0
	if err != nil {
		h.logger.WarnContext(r.Context(), "domain: read-after-write failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
	} else {
		version = td.Version
	}

	h.logger.InfoContext(r.Context(), "domain: uploaded",
		slog.String("tenant", id.Tenant),
		slog.String("domain", parsed.Spec.Domain),
		slog.Int("version", version),
	)

	writeJSON(w, http.StatusOK, summaryFromParse(parsed, version))
}

// get handles GET /v1/me/domain. Returns the active domain's
// summary, or 404 when the tenant has no domain configured. The
// raw YAML is not exposed; callers that need it should keep a
// copy of what they uploaded.
func (h *DomainHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}

	td, err := h.source.Load(r.Context(), id.Tenant)
	if err != nil {
		if errors.Is(err, domain.ErrDomainNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no domain configured"})
			return
		}
		h.logger.ErrorContext(r.Context(), "domain: load failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load tenant domain failed"})
		return
	}

	parsed, err := domain.ParseAndBuild(td.YAML, h.registry)
	if err != nil {
		// Stored YAML failed to parse against the current
		// registry — possible when the cloud removed an executor
		// the tenant had referenced. Surface an explicit hint;
		// the tenant needs to re-upload.
		h.logger.WarnContext(r.Context(), "domain: stored yaml no longer parses; re-upload required",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusUnprocessableEntity, validateErrorResponse{
			Error:  "stored domain no longer parses against the current registry",
			Issues: issuesFromError(err),
		})
		return
	}

	writeJSON(w, http.StatusOK, summaryFromParse(parsed, td.Version))
}

// requireIdentity is the auth boundary every route in this
// handler shares. Returns the verified Identity on success; on
// failure writes a 401 and returns ok=false.
func (h *DomainHandler) requireIdentity(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
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

// readYAMLBody enforces the per-request YAML cap and reads the
// body. Returns ok=false on failure, after writing the error.
func (h *DomainHandler) readYAMLBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	limit := h.maxYAMLBytes
	if limit <= 0 {
		limit = 256 * 1024
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	defer body.Close()
	yamlBytes, err := io.ReadAll(body)
	if err != nil {
		// MaxBytesReader returns a *http.MaxBytesError on overflow;
		// other read errors fall through to a generic 400.
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

// writeValidationError renders a parse error as a 422 with the
// list of issues. Falls back to a generic 400 when the error
// shape is something other than ParseAndBuild's joined errors.
func (h *DomainHandler) writeValidationError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusUnprocessableEntity, validateErrorResponse{
		Error:  "domain validation failed",
		Issues: issuesFromError(err),
	})
}

// issuesFromError walks an errors.Join'd error and pulls out
// every domain.ValidationError. Other errors collapse into a
// single message-only issue.
func issuesFromError(err error) []validateIssueResponse {
	if err == nil {
		return nil
	}
	type unwrapper interface{ Unwrap() []error }
	if u, ok := err.(unwrapper); ok {
		errs := u.Unwrap()
		out := make([]validateIssueResponse, 0, len(errs))
		for _, e := range errs {
			out = append(out, issueFromOne(e))
		}
		return out
	}
	return []validateIssueResponse{issueFromOne(err)}
}

// issueFromOne extracts the path + message from a single error.
// domain.ValidationError carries a path; everything else lands
// as a message-only entry.
func issueFromOne(err error) validateIssueResponse {
	var ve domain.ValidationError
	if errors.As(err, &ve) {
		return validateIssueResponse{Path: ve.Path, Message: ve.Message}
	}
	return validateIssueResponse{Message: err.Error()}
}

// summaryFromParse builds the validate/upload success body from
// a ParseResult. version is 0 on validate (no persistence), and
// the persisted version on upload / get.
func summaryFromParse(p domain.ParseResult, version int) validateResponse {
	resp := validateResponse{
		Domain:  p.Spec.Domain,
		Entity:  p.Spec.Entity,
		Version: version,
		Events:  append([]string(nil), p.Spec.Events...),
		States:  append([]string(nil), p.Spec.States...),
	}
	for _, a := range p.Spec.Actions {
		shape := validateActionShape{
			Name:               a.Name,
			AllowedStates:      append([]string(nil), a.AllowedStates...),
			RequiredParameters: append([]string(nil), a.RequiredParameters...),
			Risk:               a.Risk,
			Approval:           a.Approval,
			Executor:           a.Executor,
		}
		if a.RunContext != nil {
			shape.RunContext = &validateRunContextShape{
				OnUntrustedInput:         a.RunContext.OnUntrustedInput,
				OnUntrustedSensitiveRead: a.RunContext.OnUntrustedSensitiveRead,
			}
		}
		shape.RequiresAuthority = a.RequiresAuthority
		resp.Actions = append(resp.Actions, shape)
	}
	return resp
}
