package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/templates"
)

// DomainsHandler exposes the read-only domain-apex projection:
//
//	GET /v1/me/domains            — list the tenant's domains.
//	GET /v1/me/domains/{domain}   — a single domain's actions,
//	                                lifecycle, observed agents, and
//	                                the entry point to its evidence.
//
// This is the navigational apex the authenticated surface leads
// with: a domain owns its governed actions, the agents proposing
// against it, its lifecycle, and the evidence reached from it. A
// "control" and a "runtime" become views inside a domain rather
// than top-level peers.
//
// Honest data model. Two shapes are deliberately constrained to
// what the cloud actually stores today, not to a richer model we
// have not built:
//
//   - The cloud stores one active domain per tenant
//     (domain.Source). The list therefore returns 0 or 1 item
//     today. It is shaped as a list so the surface leads with
//     domains and the contract supports many when the model grows
//     — a one-domain tenant still gets a domain-shaped surface,
//     not a collapsed single control.
//   - There is no per-domain agent registry. "Agents" are derived
//     from the proposal decisions actually observed against the
//     domain's control over a recent window — an observed list,
//     not authoritative membership. The section's status is
//     "observed" so the surface never overclaims (claim
//     boundaries). When no decisions reader is wired the status is
//     "unavailable".
//
// Read-only: this handler never touches the decide path. It
// projects over domain.Source, the control store, and the
// proposals reader. Identity-scoped like the other /v1/me/* routes
// — the tenant always comes from the authenticated identity, never
// the URL or body.
type DomainsHandler struct {
	source    domain.Source
	registry  *executors.Registry
	controls  domain.ControlStore // optional; supplies evidence control id + activation
	decisions proposals.Reader    // optional; powers the observed-agents section
	runtimes  RuntimeResetter     // optional; reset on create so the resolver sees the new domain
	// gatewayOwners: who connected each gateway tool, so a connector key
	// removes only its own. Nil: it removes and changes none.
	gatewayOwners GatewayToolOwners
	logger        *slog.Logger

	// agentsLookback bounds the window over which proposing
	// identities are observed for the agents section.
	agentsLookback time.Duration

	// maxYAMLBytes caps the create-domain request body.
	maxYAMLBytes int64

	// maxDomainsFor reports the per-tenant domain cap (the multi-domain design);
	// <= 0 means unlimited. nil disables enforcement. Kept as
	// a small function so the handler doesn't couple to tenant.Store
	// or the plan table — production wires a PlanFor-backed closure.
	maxDomainsFor func(ctx context.Context, tenantID string) int
}

// DomainsOption configures a DomainsHandler.
type DomainsOption func(*DomainsHandler)

// WithDomainsLogger substitutes the logger.
func WithDomainsLogger(l *slog.Logger) DomainsOption {
	return func(h *DomainsHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithDomainsControlStore wires the control store so the single-
// domain view can resolve the evidence entry point (control id) and
// the domain's activation metadata. When nil, the evidence id and
// activation fields are omitted.
func WithDomainsControlStore(s domain.ControlStore) DomainsOption {
	return func(h *DomainsHandler) { h.controls = s }
}

// WithDomainsDecisionsReader wires the proposals reader that powers
// the observed-agents section. When nil, the section reports
// status "unavailable".
func WithDomainsDecisionsReader(r proposals.Reader) DomainsOption {
	return func(h *DomainsHandler) { h.decisions = r }
}

// WithDomainsAgentsLookback overrides the observation window for the
// agents section (default 30 days).
func WithDomainsAgentsLookback(d time.Duration) DomainsOption {
	return func(h *DomainsHandler) {
		if d > 0 {
			h.agentsLookback = d
		}
	}
}

// WithDomainsGatewayOwners reads who connected each gateway tool, so a
// connector key removes only the tools it connected.
func WithDomainsGatewayOwners(o GatewayToolOwners) DomainsOption {
	return func(h *DomainsHandler) { h.gatewayOwners = o }
}

// WithDomainsRuntimeResetter wires the runtime cache resetter so a
// freshly-created domain is picked up by the resolver without a
// stale-cache window (the same contract the draft activate path
// honors). When nil, create still succeeds; the resolver rebuilds
// lazily on the next decide for that domain.
func WithDomainsRuntimeResetter(r RuntimeResetter) DomainsOption {
	return func(h *DomainsHandler) {
		if r != nil {
			h.runtimes = r
		}
	}
}

// WithDomainsMaxBytes overrides the create-domain YAML body cap.
func WithDomainsMaxBytes(n int64) DomainsOption {
	return func(h *DomainsHandler) {
		if n > 0 {
			h.maxYAMLBytes = n
		}
	}
}

// WithDomainsMaxResolver wires the per-tenant domain cap (the multi-domain design):
// a function returning the tenant's max domains (<= 0 =
// unlimited). When set, create rejects once the tenant is at its cap.
// nil (the default) disables enforcement.
func WithDomainsMaxResolver(fn func(ctx context.Context, tenantID string) int) DomainsOption {
	return func(h *DomainsHandler) {
		if fn != nil {
			h.maxDomainsFor = fn
		}
	}
}

// NewDomainsHandler wires the dependencies. source and registry are
// required (the projection parses the active YAML the same way the
// domain handler does). controls and decisions are optional and
// degrade gracefully.
func NewDomainsHandler(source domain.Source, registry *executors.Registry, opts ...DomainsOption) *DomainsHandler {
	h := &DomainsHandler{
		source:         source,
		registry:       registry,
		logger:         slog.Default(),
		agentsLookback: 30 * 24 * time.Hour,
		maxYAMLBytes:   256 * 1024,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register attaches the handler's routes to mux. The patterns are
// distinct from the singular /v1/me/domain routes the DomainHandler
// owns, so both can be registered.
func (h *DomainsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/domains", h.list)
	mux.HandleFunc("POST /v1/me/domains", h.create)
	mux.HandleFunc("GET /v1/me/domains/{domain}", h.get)
	mux.HandleFunc("GET /v1/me/domains/{domain}/yaml", h.yaml)
	mux.HandleFunc("PUT /v1/me/domains/{domain}", h.update)
	mux.HandleFunc("DELETE /v1/me/domains/{domain}", h.delete)
}

// --- wire shapes ---

// domainListItem is one row in the GET /v1/me/domains response.
type domainListItem struct {
	Name        string     `json:"name"`
	Entity      string     `json:"entity"`
	Version     int        `json:"version"`
	Status      string     `json:"status"`
	ActionCount int        `json:"action_count"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	// Posture is the per-domain governance mode ("observe" | "enforce")
	// read from the active control. Empty when the control predates
	// per-domain posture (the decide path then uses the tenant-wide
	// mode); the web selector renders it as the posture pill.
	Posture string `json:"posture,omitempty"`
}

// domainsListResponse is the GET /v1/me/domains body.
type domainsListResponse struct {
	Domains []domainListItem `json:"domains"`
}

// domainActionView is one governed action on the single-domain view.
type domainActionView struct {
	Name               string                `json:"name"`
	Risk               string                `json:"risk,omitempty"`
	Approval           string                `json:"approval,omitempty"`
	AllowedStates      []string              `json:"allowed_states"`
	RequiredParameters []string              `json:"required_parameters,omitempty"`
	Parameters         []domainParameterView `json:"parameters,omitempty"`
	Executor           string                `json:"executor,omitempty"`
	// RequiresAuthority: an agent holding no card for this action is
	// refused (no_mandate_issued) rather than unaffected.
	RequiresAuthority bool `json:"requires_authority,omitempty"`
}

// domainParameterView is one typed parameter contract, carried so a
// caller can tell which parameters are summable before proposing a
// mandate that sums one.
//
// Name and Type only. The constraint fields belong to the proof pack,
// whose shape is pinned by a golden test; a surface that needs to know
// whether a quantity can be counted needs the type and nothing else,
// and copying the bounds here would create a second place for them to
// drift. omitempty keeps a domain that declares no typed parameters
// byte-identical to one authored before this field existed.
type domainParameterView struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// domainLifecycle is the domain's state machine projection.
type domainLifecycle struct {
	States      []string               `json:"states"`
	Events      []string               `json:"events"`
	Transitions []domainTransitionView `json:"transitions"`
}

type domainTransitionView struct {
	On   string `json:"on"`
	From string `json:"from"`
	To   string `json:"to"`
}

// domainAgentView is one observed proposing identity. It is derived
// from decisions seen against the domain's control over the
// observation window, not from an agent registry.
type domainAgentView struct {
	// ID is the identity string observed proposing. Kind says which
	// identity dimension it is so the surface can label it without
	// guessing.
	ID string `json:"id"`
	// "agent": verified, a key bound to this agent made the calls (RFC
	// 041). "api_key": a credential not bound to the agent it named, or
	// that named none. "actor": a name the caller claimed without such
	// a key (a session, or a row recorded before verification existed).
	// "subject": neither.
	Kind string `json:"kind"`
	// Operations is how many decisions by this identity were seen in
	// the observation window's sample. It is an observed count, not a
	// billing figure.
	Operations int       `json:"operations"`
	LastSeen   time.Time `json:"last_seen"`
	// ViaKeys are the API keys an agent's requests came through (RFC
	// 041 D3): the credential, shown beside the agent rather than
	// listed as an agent of its own.
	ViaKeys []string `json:"via_keys,omitempty"`
}

// domainAgentsSection carries the observed-agents list with an
// explicit basis so the surface never reads it as authoritative
// membership (the authenticated-surface design honesty). Status is "observed" when a
// decisions reader is wired, "unavailable" otherwise.
type domainAgentsSection struct {
	Status string             `json:"status"` // "observed" | "unavailable"
	Window *domainAgentWindow `json:"window,omitempty"`
	Agents []domainAgentView  `json:"agents"`
}

type domainAgentWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// domainEvidence is the entry point from a domain to its evidence
// view (the Control Room, since reframed). ControlID is the
// in-scope active control; empty when no control store is wired or
// no active control matches the domain.
type domainEvidence struct {
	ControlID string `json:"control_id,omitempty"`
}

// domainDetailResponse is the GET /v1/me/domains/{domain} body.
type domainDetailResponse struct {
	Name        string     `json:"name"`
	Entity      string     `json:"entity"`
	Version     int        `json:"version"`
	Status      string     `json:"status"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	// Posture is the per-domain governance mode ("observe" | "enforce")
	// from the active control; empty when the control predates per-domain
	// posture. The domain home renders the flip control from it.
	Posture   string              `json:"posture,omitempty"`
	Actions   []domainActionView  `json:"actions"`
	Lifecycle domainLifecycle     `json:"lifecycle"`
	Agents    domainAgentsSection `json:"agents"`
	Evidence  domainEvidence      `json:"evidence"`
}

// --- handlers ---

// list handles GET /v1/me/domains. Returns the tenant's domains,
// default first. With the composite (tenant_id, domain_id) model a
// tenant may have many; a one-domain tenant still gets a one-element
// list, an unprovisioned tenant an empty one.
func (h *DomainsHandler) list(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}

	resp := domainsListResponse{Domains: []domainListItem{}}

	domains, err := h.source.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "domains: list failed",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load tenant domains failed"})
		return
	}

	for _, td := range domains {
		parsed, perr := domain.ParseAndBuild(td.YAML, h.registry)
		if perr != nil {
			// A stored domain that no longer parses against the current
			// registry is skipped from the index rather than 500ing the
			// whole list — the single-domain view returns the re-upload
			// hint when the user opens it.
			h.logger.WarnContext(r.Context(), "domains: stored yaml no longer parses; omitting from index",
				slog.String("tenant", id.Tenant), slog.String("domain_id", td.DomainID),
				slog.String("err", perr.Error()))
			continue
		}
		active := h.activeControlFor(r, id.Tenant, parsed.Spec.Domain)
		item := domainListItem{
			Name:        parsed.Spec.Domain,
			Entity:      parsed.Spec.Entity,
			Version:     td.Version,
			Status:      "active",
			ActionCount: len(parsed.Spec.Actions),
		}
		if active != nil {
			if active.Status != "" {
				item.Status = active.Status
			}
			at := active.ActivatedAt
			item.ActivatedAt = &at
			item.Posture = domain.NormalizeControlMode(active.Mode)
		}
		resp.Domains = append(resp.Domains, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

// create handles POST /v1/me/domains. It adds an *additional*,
// non-default domain to the tenant from a validated YAML body (RFC
// 033 Slice 2 step 4), or from a starter template with
// ?template=<slug> (which becomes the default when it is the tenant's
// first domain). The tenant's first/default domain is
// still created through the draft activate path; this endpoint is
// how a tenant grows to more than one.
//
// Validation mirrors the activate path: the YAML must parse and
// build against the executor registry, else a 422 with the issues.
// A domain whose derived id already exists for the tenant is a 409
// (create never overwrites — editing an existing domain goes through
// its own page). On success the runtime cache is reset so the
// resolver sees the new domain, a control record is created
// best-effort (the product audit trail, exactly as activate does),
// and the new domain's summary is returned with 201.
func (h *DomainsHandler) create(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	// A connector key creates only the gateway's domain, from a YAML body
	// (connectorContract).
	if id.IsConnector() && r.URL.Query().Get("template") != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connector_key_forbidden",
			"message": "a connector key changes only the gateway's domain"})
		return
	}
	if !id.IsConnector() && !requireManagement(w, id) {
		return
	}

	// Plan cap: refuse once the tenant is at its domain
	// limit, before doing any parse/validate work. <= 0 is unlimited.
	if h.maxDomainsFor != nil {
		if max := h.maxDomainsFor(r.Context(), id.Tenant); max > 0 {
			existing, lerr := h.source.List(r.Context(), id.Tenant)
			if lerr != nil {
				h.logger.ErrorContext(r.Context(), "domains: list failed on create cap check",
					slog.String("tenant", id.Tenant), slog.String("err", lerr.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create domain failed"})
				return
			}
			if len(existing) >= max {
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error":       "domain limit reached for this plan; delete a domain or upgrade",
					"max_domains": max,
					"count":       len(existing),
				})
				return
			}
		}
	}

	// ?template=<slug> creates the domain from a curated starter
	// template instead of a YAML body, so a customer can give an agent a
	// card for a familiar task (refunds, store credit, plan changes)
	// without authoring a contract first. Same validation, cap, conflict
	// and audit path as a YAML create; it never replaces a domain.
	var yamlBytes []byte
	persist := h.source.Create
	if slug := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("template"))); slug != "" {
		tmpl, terr := templates.Get(slug)
		if terr != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "template not found"})
			return
		}
		yamlBytes = tmpl.YAML
		// A tenant's first domain has to be its default: Create always
		// inserts a non-default row, and a tenant with no default has
		// nothing for the default-domain paths to resolve. A tenant with
		// no domains has nothing Save could replace.
		existing, lerr := h.source.List(r.Context(), id.Tenant)
		if lerr != nil {
			h.logger.ErrorContext(r.Context(), "domains: list failed on template create",
				slog.String("tenant", id.Tenant), slog.String("err", lerr.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create domain failed"})
			return
		}
		if len(existing) == 0 {
			persist = h.source.Save
		}
	} else {
		var ok bool
		if yamlBytes, ok = h.readCreateBody(w, r); !ok {
			return
		}
	}

	parsed, err := domain.ParseAndBuild(yamlBytes, h.registry)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, validateErrorResponse{
			Error:  "domain fails validation; cannot create",
			Issues: issuesFromError(err),
		})
		return
	}
	if id.IsConnector() && !connectorContract(w, nil, parsed, connectorScope{}) {
		return
	}

	if err := persist(r.Context(), id.Tenant, yamlBytes); err != nil {
		if errors.Is(err, domain.ErrDomainExists) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "a domain with this name already exists; edit it from its page instead",
			})
			return
		}
		h.logger.ErrorContext(r.Context(), "domains: create failed",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create domain failed"})
		return
	}

	// Reset the tenant's runtime cache so the resolver picks up the
	// new domain without a stale window (same contract as activate).
	if h.runtimes != nil {
		h.runtimes.ResetTenant(id.Tenant)
	}

	// Create a control record for the new domain. Best-effort: a
	// failure here does not undo the domain create. The control is
	// the product audit trail; the domain is the runtime contract.
	if h.controls != nil {
		ctrl := NewControlFromParse(id.Tenant, id.Subject, parsed)
		if cerr := activateControl(r.Context(), h.controls, ctrl); cerr != nil {
			h.logger.WarnContext(r.Context(), "domains: create control record failed",
				slog.String("tenant", id.Tenant),
				slog.String("domain", parsed.Spec.Domain),
				slog.String("err", cerr.Error()))
		}
	}

	h.logger.InfoContext(r.Context(), "domains: created",
		slog.String("tenant", id.Tenant), slog.String("domain", parsed.Spec.Domain))

	// A freshly-created domain is always version 1.
	writeJSON(w, http.StatusCreated, summaryFromParse(parsed, 1))
}

// readCreateBody enforces the YAML body cap and reads the create
// request body, writing the error and returning ok=false on failure.
func (h *DomainsHandler) readCreateBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	limit := h.maxYAMLBytes
	if limit <= 0 {
		limit = 256 * 1024
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	defer body.Close()
	yamlBytes, err := io.ReadAll(body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "yaml body exceeds limit"})
			return nil, false
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read yaml body"})
		return nil, false
	}
	if len(strings.TrimSpace(string(yamlBytes))) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "yaml body is empty"})
		return nil, false
	}
	return yamlBytes, true
}

// yaml handles GET /v1/me/domains/{domain}/yaml (the multi-domain design):
// returns the addressed domain's stored YAML verbatim, so the
// web edit surface can pre-fill the editor with the current contract
// before PUTting an in-place update. The tenant's own definition —
// no secrets — scoped to the authenticated identity. 404 on an
// unknown slug.
func (h *DomainsHandler) yaml(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}

	domains, err := h.source.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "domains: list failed on yaml read",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load tenant domains failed"})
		return
	}
	for i := range domains {
		if strings.EqualFold(slug, domains[i].Slug) {
			h.writeYAML(w, domains[i].YAML)
			return
		}
		if p, perr := domain.ParseAndBuild(domains[i].YAML, h.registry); perr == nil && strings.EqualFold(slug, p.Spec.Domain) {
			h.writeYAML(w, domains[i].YAML)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
}

func (h *DomainsHandler) writeYAML(w http.ResponseWriter, yaml []byte) {
	w.Header().Set("Content-Type", "application/x-yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(yaml)
}

// update handles PUT /v1/me/domains/{domain}:
// re-authors a specific domain's contract in place. It is
// identity-preserving — the domain_id and slug stay fixed, so editing
// a domain's actions/lifecycle never moves its {domain} URL. The new
// YAML must therefore keep the same domain name; a name change
// (rename) is refused here (409) and is a separate operation.
//
// Validation mirrors create/activate: the YAML must parse and build
// against the executor registry (422 with issues otherwise). On
// success the version increments, the runtime cache is reset, and a
// control record is created (re-activation) — correctly superseding
// only this domain's prior control thanks to the domain-scoped
// lineage.
func (h *DomainsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	if !id.IsConnector() && !requireManagement(w, id) {
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}
	if id.IsConnector() && !strings.EqualFold(slug, gatewayDomain) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connector_key_forbidden",
			"message": "a connector key changes only the gateway's domain"})
		return
	}

	yamlBytes, ok := h.readCreateBody(w, r)
	if !ok {
		return
	}

	parsed, err := domain.ParseAndBuild(yamlBytes, h.registry)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, validateErrorResponse{
			Error:  "domain fails validation; cannot update",
			Issues: issuesFromError(err),
		})
		return
	}
	// Resolve the addressed domain across the tenant's domains (slug
	// token first, then parsed name for legacy rows), mirroring get.
	domains, err := h.source.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "domains: list failed on update",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load tenant domains failed"})
		return
	}
	var target *domain.TenantDomain
	for i := range domains {
		if strings.EqualFold(slug, domains[i].Slug) {
			target = &domains[i]
			break
		}
		if p, perr := domain.ParseAndBuild(domains[i].YAML, h.registry); perr == nil && strings.EqualFold(slug, p.Spec.Domain) {
			target = &domains[i]
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
		return
	}

	// Identity-preserving: the new YAML must keep the domain's slug.
	// A different name (→ different slug) would be a rename, which this
	// endpoint does not do.
	if newID, _ := domain.DeriveStorageIdentity(yamlBytes); !strings.EqualFold(newID, target.Slug) && !strings.EqualFold(newID, target.DomainID) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "this endpoint edits a domain in place; the domain name can't change here (rename is a separate operation)",
		})
		return
	}
	if id.IsConnector() {
		var current *domain.ParseResult
		if p, perr := domain.ParseAndBuild(target.YAML, h.registry); perr == nil {
			current = &p
		}
		if current == nil || !connectorContract(w, current, parsed, newConnectorScope(r.Context(), h.gatewayOwners, id.Tenant, id.APIKeyID())) {
			if current == nil {
				connectorRefusal(w, "the gateway's current contract can't be read, so a connector key can't change it")
			}
			return
		}
	}

	if err := h.source.UpdateByDomain(r.Context(), id.Tenant, target.DomainID, yamlBytes); err != nil {
		if errors.Is(err, domain.ErrDomainNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "domains: update failed",
			slog.String("tenant", id.Tenant), slog.String("domain_id", target.DomainID),
			slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update domain failed"})
		return
	}

	if h.runtimes != nil {
		h.runtimes.ResetTenant(id.Tenant)
	}

	// Control record for the re-activation (best-effort; domain-scoped
	// lineage supersedes only this domain's prior active control).
	if h.controls != nil {
		ctrl := NewControlFromParse(id.Tenant, id.Subject, parsed)
		if cerr := activateControl(r.Context(), h.controls, ctrl); cerr != nil {
			h.logger.WarnContext(r.Context(), "domains: update control record failed",
				slog.String("tenant", id.Tenant),
				slog.String("domain", parsed.Spec.Domain),
				slog.String("err", cerr.Error()))
		}
	}

	// Read the new version for the response.
	version := target.Version + 1
	if td, lerr := h.source.LoadByDomain(r.Context(), id.Tenant, target.DomainID); lerr == nil {
		version = td.Version
	}

	h.logger.InfoContext(r.Context(), "domains: updated",
		slog.String("tenant", id.Tenant), slog.String("domain", parsed.Spec.Domain))
	writeJSON(w, http.StatusOK, summaryFromParse(parsed, version))
}

// delete handles DELETE /v1/me/domains/{domain} (the multi-domain design):
// removes an *additional* (non-default) domain. The default
// domain is protected — it backs the deprecated singular
// /v1/me/domain* routes (D7) — so deleting it is a 409; a tenant
// removes its other domains first. On success the runtime cache is
// reset so the resolver stops serving the gone domain. Historical
// evidence stamped with the domain_id (decisions, receipts, usage)
// is immutable audit and is deliberately left intact.
func (h *DomainsHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	if !requireManagement(w, id) {
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}

	domains, err := h.source.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "domains: list failed on delete",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load tenant domains failed"})
		return
	}

	// Resolve the slug across the tenant's domains — slug token first,
	// then the parsed name (legacy rows), mirroring get.
	var target *domain.TenantDomain
	for i := range domains {
		if strings.EqualFold(slug, domains[i].Slug) {
			target = &domains[i]
			break
		}
		if parsed, perr := domain.ParseAndBuild(domains[i].YAML, h.registry); perr == nil && strings.EqualFold(slug, parsed.Spec.Domain) {
			target = &domains[i]
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
		return
	}

	// Guard the default (the first row by is_default ordering): it
	// backs the singular routes, so it is not deletable here.
	if len(domains) > 0 && target.DomainID == domains[0].DomainID {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "cannot delete the default domain; delete the workspace's other domains first",
		})
		return
	}

	if err := h.source.DeleteByDomain(r.Context(), id.Tenant, target.DomainID); err != nil {
		if errors.Is(err, domain.ErrDomainNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "domains: delete failed",
			slog.String("tenant", id.Tenant), slog.String("domain_id", target.DomainID),
			slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete domain failed"})
		return
	}

	if h.runtimes != nil {
		h.runtimes.ResetTenant(id.Tenant)
	}

	h.logger.InfoContext(r.Context(), "domains: deleted",
		slog.String("tenant", id.Tenant), slog.String("domain_id", target.DomainID))
	w.WriteHeader(http.StatusNoContent)
}

// get handles GET /v1/me/domains/{domain}. Returns the single
// domain's actions, lifecycle, observed agents, and evidence entry
// point. 404 when the tenant has no domain or the slug does not
// match the tenant's active domain.
func (h *DomainsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}

	td, err := h.source.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "domains: list failed",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load tenant domain failed"})
		return
	}

	// Resolve the requested slug across all the tenant's domains. The
	// stored slug is the URL token, but we match on the parsed domain
	// name too so a slug that predates a backfill still resolves. A
	// row whose YAML no longer parses is the one case that returns the
	// 422 re-upload hint (only when it is the row being addressed).
	var (
		matched       *domain.TenantDomain
		matchedParsed *domain.ParseResult
		parseErr      error
	)
	for i := range td {
		row := td[i]
		if !strings.EqualFold(slug, row.Slug) {
			// Fall through to a name match below if the slug token
			// doesn't line up (e.g. legacy rows).
			parsedRow, prr := domain.ParseAndBuild(row.YAML, h.registry)
			if prr == nil && strings.EqualFold(slug, parsedRow.Spec.Domain) {
				matched = &td[i]
				matchedParsed = &parsedRow
				break
			}
			continue
		}
		parsedRow, prr := domain.ParseAndBuild(row.YAML, h.registry)
		matched = &td[i]
		if prr != nil {
			parseErr = prr
			break
		}
		matchedParsed = &parsedRow
		break
	}

	if matched == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
		return
	}
	if parseErr != nil {
		h.logger.WarnContext(r.Context(), "domains: stored yaml no longer parses; re-upload required",
			slog.String("tenant", id.Tenant), slog.String("err", parseErr.Error()))
		writeJSON(w, http.StatusUnprocessableEntity, validateErrorResponse{
			Error:  "stored domain no longer parses against the current registry",
			Issues: issuesFromError(parseErr),
		})
		return
	}
	parsed := matchedParsed

	resp := domainDetailResponse{
		Name:    parsed.Spec.Domain,
		Entity:  parsed.Spec.Entity,
		Version: matched.Version,
		Status:  "active",
		Actions: make([]domainActionView, 0, len(parsed.Spec.Actions)),
		Lifecycle: domainLifecycle{
			States:      append([]string{}, parsed.Spec.States...),
			Events:      append([]string{}, parsed.Spec.Events...),
			Transitions: make([]domainTransitionView, 0, len(parsed.Spec.Transitions)),
		},
	}
	for _, a := range parsed.Spec.Actions {
		params := make([]domainParameterView, 0, len(a.Parameters))
		for _, p := range a.Parameters {
			params = append(params, domainParameterView{Name: p.Name, Type: p.Type})
		}
		if len(params) == 0 {
			params = nil
		}
		resp.Actions = append(resp.Actions, domainActionView{
			Name:               a.Name,
			Risk:               a.Risk,
			Approval:           a.Approval,
			AllowedStates:      append([]string{}, a.AllowedStates...),
			RequiredParameters: append([]string{}, a.RequiredParameters...),
			Parameters:         params,
			Executor:           a.Executor,
			RequiresAuthority:  a.RequiresAuthority,
		})
	}
	for _, t := range parsed.Spec.Transitions {
		resp.Lifecycle.Transitions = append(resp.Lifecycle.Transitions, domainTransitionView{
			On: t.On, From: t.From, To: t.To,
		})
	}

	active := h.activeControlFor(r, id.Tenant, parsed.Spec.Domain)
	if active != nil {
		if active.Status != "" {
			resp.Status = active.Status
		}
		at := active.ActivatedAt
		resp.ActivatedAt = &at
		resp.Evidence.ControlID = active.ID
		resp.Posture = domain.NormalizeControlMode(active.Mode)
	}

	resp.Agents = h.observedAgents(r, id.Tenant, active)

	writeJSON(w, http.StatusOK, resp)
}

// activeControlFor returns the active control record matching the
// domain name, or nil when no control store is wired or none match.
func (h *DomainsHandler) activeControlFor(r *http.Request, tenantID, domainName string) *domain.Control {
	if h.controls == nil {
		return nil
	}
	list, err := h.controls.List(r.Context(), tenantID)
	if err != nil {
		h.logger.WarnContext(r.Context(), "domains: list controls failed",
			slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return nil
	}
	for i := range list {
		c := list[i]
		if c.Status == "active" && strings.EqualFold(c.Domain, domainName) {
			return &c
		}
	}
	return nil
}

// observedAgents derives the proposing identities seen against the
// domain's active control over the observation window. It is an
// observed projection (option a), not an agent registry — the
// section status records that. When no decisions reader is wired, or
// no active control resolves the scope, the section is "unavailable".
func (h *DomainsHandler) observedAgents(r *http.Request, tenantID string, active *domain.Control) domainAgentsSection {
	if h.decisions == nil || active == nil {
		return domainAgentsSection{Status: "unavailable", Agents: []domainAgentView{}}
	}
	now := time.Now().UTC()
	from := now.Add(-h.agentsLookback)
	q := proposals.DecisionQuery{
		ControlID: active.ID,
		From:      from,
		To:        now,
		Limit:     proposals.MaxLimit,
	}
	rows, err := h.decisions.ListDecisionsInWindow(r.Context(), tenantID, q)
	if err != nil {
		h.logger.WarnContext(r.Context(), "domains: list decisions failed",
			slog.String("tenant", tenantID), slog.String("control_id", active.ID), slog.String("err", err.Error()))
		return domainAgentsSection{Status: "unavailable", Agents: []domainAgentView{}}
	}

	type agg struct {
		id       string
		kind     string
		count    int
		lastSeen time.Time
		keys     []string
	}
	byKey := map[string]*agg{}
	for _, row := range rows {
		identity, kind := agentIdentity(row)
		if identity == "" {
			continue
		}
		key := kind + "\x00" + identity
		a, ok := byKey[key]
		if !ok {
			a = &agg{id: identity, kind: kind}
			byKey[key] = a
		}
		a.count++
		if row.CreatedAt.After(a.lastSeen) {
			a.lastSeen = row.CreatedAt
		}
		if k := strings.TrimSpace(row.PrincipalAPIKeyID); k != "" && kind == "agent" && !containsString(a.keys, k) {
			a.keys = append(a.keys, k)
		}
	}

	agents := make([]domainAgentView, 0, len(byKey))
	for _, a := range byKey {
		agents = append(agents, domainAgentView{
			ID:         a.id,
			Kind:       a.kind,
			Operations: a.count,
			LastSeen:   a.lastSeen,
			ViaKeys:    a.keys,
		})
	}
	// Newest-seen first, then by operations, then id — stable order.
	sort.Slice(agents, func(i, j int) bool {
		if !agents[i].LastSeen.Equal(agents[j].LastSeen) {
			return agents[i].LastSeen.After(agents[j].LastSeen)
		}
		if agents[i].Operations != agents[j].Operations {
			return agents[i].Operations > agents[j].Operations
		}
		return agents[i].ID < agents[j].ID
	})

	return domainAgentsSection{
		Status: "observed",
		Window: &domainAgentWindow{From: from, To: now},
		Agents: agents,
	}
}

// agentIdentity picks the most meaningful identity for an observed
// proposing agent, preferring the API key (the stable machine
// credential), then the actor id, then the principal subject.
// Returns ("", "") when the row carries none.
func agentIdentity(row proposals.DecisionRow) (id, kind string) {
	// The agent first, but only when it is verified: a key
	// bound to exactly this actor made the call, so the name is the
	// credential's own and the key is shown beside it as via_keys.
	// Otherwise the actor_id is the caller's claim — an unbound key may
	// name any actor on an action with no card — so the credential that
	// crossed the boundary identifies the row, as before, and a bare
	// claimed name is labelled as such rather than as the agent.
	if row.ActorVerified {
		if s := strings.TrimSpace(row.ActorID); s != "" {
			return s, "agent"
		}
	}
	if s := strings.TrimSpace(row.PrincipalAPIKeyID); s != "" {
		return s, "api_key"
	}
	if s := strings.TrimSpace(row.ActorID); s != "" {
		return s, "actor"
	}
	if s := strings.TrimSpace(row.PrincipalSubject); s != "" {
		return s, "subject"
	}
	return "", ""
}

// managementRole is the role a caller must carry to author a
// tenant's governing domain contract — create, update, or delete a
// domain; upload YAML; activate a draft; apply a template. The auth
// middleware grants it to session identities (the dashboard owner,
// see auth.sessionOwnerRoles) but never to API keys: a key minter
// chooses the key's role set explicitly.
//
// This is the separation the whole product rests on. A runtime
// decide credential proposes actions *against* a domain; it must
// not be able to rewrite the very contract that governs it.
// Authoring is an owner action, decide is a runtime action — the
// role gate keeps them on opposite sides of the line even though
// both authenticate to the same /v1/me surface.
const managementRole = "tenant_owner"

// requireManagement enforces that the identity may author domain
// contracts. Callers invoke it after the tenant/auth check has
// already passed. On failure it writes a 403 and returns false.
//
// Two roles satisfy it, matching the key-management gate
// (auth.isKeyManagement): "tenant_owner" — appended to dashboard
// session identities (auth.sessionOwnerRoles) — and "admin", the
// bootstrap/superuser role cmd/kiff-tenant stamps on a fresh tenant's
// first key and that solo Clerk users carry by default. Admin is
// strictly more privileged than tenant_owner, so refusing it here was a
// gap: it blocked the Playground's admin-role demo key (and any
// admin-keyed authoring) with a spurious 403. A plain runtime decide
// key carries neither role and is still refused — the separation the
// product rests on holds. A key bound to an agent is refused whatever
// its roles: it would be authoring the contract it acts under.
func requireManagement(w http.ResponseWriter, id auth.Identity) bool {
	if can(id, membership.CapDomainsManage) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": "authoring a domain requires an admin or editor on the account, or a key with the tenant_owner or admin role; a runtime key, or a key bound to an agent, cannot modify the domain it is governed by",
	})
	return false
}

// requireIdentity is the auth boundary every route shares. Returns
// the verified Identity on success; on failure writes a 401 and
// returns ok=false.
func (h *DomainsHandler) requireIdentity(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
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

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
