package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"
)

// KeysHandler exposes the API-key CRUD surface:
//
//	POST   /v1/keys           — issue a key (plaintext returned once)
//	GET    /v1/keys           — list active keys for the caller's tenant
//	DELETE /v1/keys/{id}      — revoke a key by id
//
// All routes require the caller to be authenticated; Wrap them
// behind Middleware.Wrap. The handler reads Identity / actor.Actor
// from the context that the middleware attached.
//
// The handler does not enforce tenant-admin role checks at the
// route level — that lives in per-route authorization which slice
// 4 will hang off the same context. v0.1 trusts any authenticated
// caller in the tenant to manage that tenant's keys; tightening
// happens once the role-to-permission mapping (out of scope for
// issue #3) lands.
type KeysHandler struct {
	store APIKeyStore
	clock func() time.Time
	// stepUp guards minting or granting an account role.
	stepUp *StepUp
	// minterRemoved reports whether a key's minter was removed from the
	// account, so the list can flag agent keys for rotation.
	minterRemoved func(ctx context.Context, tenant, subject string) bool
}

// WithKeysStepUp requires a recent sign-in to mint an account key or
// grant an account role onto one.
func WithKeysStepUp(s *StepUp) KeysHandlerOption {
	return func(h *KeysHandler) { h.stepUp = s }
}

// WithKeysMinterRemoved flags keys whose minter was removed from the
// account ("minted by a removed member"), so an admin rotates them.
func WithKeysMinterRemoved(f func(ctx context.Context, tenant, subject string) bool) KeysHandlerOption {
	return func(h *KeysHandler) { h.minterRemoved = f }
}

// requireStepUp answers false (having written the refusal) when roles
// name an account role and the caller's sign-in is not recent.
func (h *KeysHandler) requireStepUp(w http.ResponseWriter, r *http.Request, id Identity, roles []string) bool {
	if h.stepUp == nil || !hasAccountRole(roles) {
		return true
	}
	if _, err := h.stepUp.Check(r.Context(), id); err != nil {
		WriteReauthRequired(w)
		return false
	}
	return true
}

// KeysHandlerOption configures KeysHandler.
type KeysHandlerOption func(*KeysHandler)

// WithKeysClock substitutes the time source. Tests use this to
// keep CreatedAt deterministic.
func WithKeysClock(now func() time.Time) KeysHandlerOption {
	return func(h *KeysHandler) { h.clock = now }
}

// NewKeysHandler returns a KeysHandler backed by the given store.
func NewKeysHandler(store APIKeyStore, opts ...KeysHandlerOption) *KeysHandler {
	h := &KeysHandler{
		store: store,
		clock: func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register attaches the handler's routes to mux. Idempotent if mux
// has not seen the patterns before. The patterns use the Go 1.22+
// ServeMux pattern syntax.
func (h *KeysHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/keys", h.create)
	mux.HandleFunc("GET /v1/keys", h.list)
	mux.HandleFunc("DELETE /v1/keys/{id}", h.revoke)
	mux.HandleFunc("PATCH /v1/keys/{id}", h.update)
}

type createRequest struct {
	Label string   `json:"label"`
	Roles []string `json:"roles,omitempty"`
	// AgentID binds the key to one agent, the holder of a card. The
	// key may then only propose as that agent. Optional; empty mints
	// an unbound key.
	AgentID string `json:"agent_id,omitempty"`
	// Environment binds the key to one environment: its requests
	// are decided there, and one naming another environment is refused.
	// Optional; empty mints a key that may act in any environment.
	Environment string `json:"environment,omitempty"`
}

// CreateResponse is the body returned from POST /v1/keys. The
// plaintext key appears here exactly once — never again. Clients
// that lose the value must rotate the key.
type CreateResponse struct {
	ID          string    `json:"id"`
	Tenant      string    `json:"tenant"`
	Subject     string    `json:"subject"`
	Label       string    `json:"label"`
	Roles       []string  `json:"roles,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	Environment string    `json:"environment,omitempty"`
	Plaintext   string    `json:"plaintext"`
	CreatedAt   time.Time `json:"created_at"`
}

func (h *KeysHandler) create(w http.ResponseWriter, r *http.Request) {
	id, err := IdentityFrom(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err)
		return
	}
	if id.Tenant == "" {
		writeJSONError(w, http.StatusUnauthorized, errors.New("identity has no tenant"))
		return
	}
	if !isKeyManagement(id) {
		writeJSONError(w, http.StatusForbidden, errors.New(
			"minting an API key requires the tenant_owner or admin role; a runtime key, or a key bound to an agent, cannot mint keys"))
		return
	}

	var req createRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, err)
			return
		}
	}
	if len(req.Label) > 200 {
		writeJSONError(w, http.StatusBadRequest, errors.New("label exceeds 200 characters"))
		return
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	if !validAgentID(req.AgentID) {
		writeJSONError(w, http.StatusBadRequest, errors.New("agent_id must be at most 200 characters with no control characters"))
		return
	}
	req.Environment = strings.TrimSpace(req.Environment)
	if !validAgentID(req.Environment) {
		writeJSONError(w, http.StatusBadRequest, errors.New("environment must be at most 200 characters with no control characters"))
		return
	}
	connector := slices.Contains(req.Roles, RoleConnector)
	if connector && (len(req.Roles) != 1 || req.AgentID != "" || req.Environment != "") {
		// A connector key connects tools and nothing else: it holds
		// no other role and acts for no agent.
		writeJSONError(w, http.StatusBadRequest, errors.New(
			"a "+RoleConnector+" key holds only that role and is bound to no agent or environment"))
		return
	}
	if len(id.DeviceKey) > 0 && req.AgentID == "" && !connector {
		// A phone mints only a taf's own key, bound to one agent and acting
		// as it, or the connector key planque keeps.
		// Never a key for the account.
		writeJSONError(w, http.StatusForbidden, errors.New("from a phone, a key must be bound to an agent (set agent_id) or be a "+RoleConnector+" key"))
		return
	}
	for _, role := range req.Roles {
		if !validRole(role) {
			writeJSONError(w, http.StatusBadRequest, errors.New("role contains invalid characters"))
			return
		}
		if role == RoleGatewayAgent && req.AgentID == "" {
			writeJSONError(w, http.StatusBadRequest, errors.New(
				"a "+RoleGatewayAgent+" key must be bound to an agent: set agent_id"))
			return
		}
		if req.AgentID != "" && IsAccountRole(role) {
			writeJSONError(w, http.StatusBadRequest, errors.New(
				"a key bound to an agent cannot hold the "+role+" role; it acts as that agent, never for the account"))
			return
		}
	}
	// Authority subset rule. The trust boundary that lets the
	// proposal gate treat Identity.Roles as
	// authoritative depends on key role minting being non-
	// escalating. The rule:
	//
	//   - admin caller can mint a key with any valid role
	//     (including admin), because admin is what cmd/kiff-tenant
	//     stamps on the bootstrap key and every other role on the
	//     tenant flows downstream from there;
	//   - any non-admin caller can only mint a key whose roles
	//     are a subset of the caller's own roles. A caller with
	//     [ops_agent] can mint a key with no roles, or
	//     [ops_agent], but not [admin] or [billing_admin].
	//
	// See rolesAreSubsetOf below.
	if !rolesAreSubsetOf(req.Roles, id.Roles) {
		writeJSONError(w, http.StatusForbidden, errors.New(
			"requested roles exceed caller authority; minting a key with a role the caller does not hold is not permitted"))
		return
	}

	if !h.requireStepUp(w, r, id, req.Roles) {
		return
	}

	now := h.clock()
	gen, err := NewAPIKey(id.Tenant, now)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	rec := APIKeyRecord{
		ID:          ulid.MustNew(ulid.Timestamp(now), ulid.DefaultEntropy()).String(),
		Tenant:      id.Tenant,
		Subject:     id.Subject,
		Label:       strings.TrimSpace(req.Label),
		Roles:       append([]string(nil), req.Roles...),
		AgentID:     req.AgentID,
		Environment: req.Environment,
		KeyHash:     gen.Hash,
		CreatedAt:   now,
	}
	if err := h.store.Insert(r.Context(), rec); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreateResponse{
		ID:          rec.ID,
		Tenant:      rec.Tenant,
		Subject:     rec.Subject,
		Label:       rec.Label,
		Roles:       rec.Roles,
		AgentID:     rec.AgentID,
		Environment: rec.Environment,
		Plaintext:   gen.Plaintext,
		CreatedAt:   rec.CreatedAt,
	})
}

// ListItem is the per-key shape returned by GET /v1/keys. Note:
// no plaintext, no hash. The caller has the plaintext from the
// create call; the hash is implementation detail.
type ListItem struct {
	ID          string     `json:"id"`
	Subject     string     `json:"subject"`
	Label       string     `json:"label"`
	Roles       []string   `json:"roles,omitempty"`
	AgentID     string     `json:"agent_id,omitempty"`
	Environment string     `json:"environment,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	// MintedByRemovedMember: the person who minted this key was removed
	// from the account. Agent keys keep working (revoking them would
	// stop production); rotating them is an admin's decision.
	MintedByRemovedMember bool `json:"minted_by_removed_member,omitempty"`
}

func (h *KeysHandler) list(w http.ResponseWriter, r *http.Request) {
	id, err := IdentityFrom(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err)
		return
	}
	if id.Tenant == "" {
		writeJSONError(w, http.StatusUnauthorized, errors.New("identity has no tenant"))
		return
	}
	rows, err := h.store.ListActive(r.Context(), id.Tenant)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]ListItem, 0, len(rows))
	removed := map[string]bool{}
	for _, rec := range rows {
		item := listItem(rec)
		if h.minterRemoved != nil && rec.Subject != "" {
			gone, seen := removed[rec.Subject]
			if !seen {
				gone = h.minterRemoved(r.Context(), id.Tenant, rec.Subject)
				removed[rec.Subject] = gone
			}
			item.MintedByRemovedMember = gone
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// listItem shows a key's roles as it acts with them: an account role
// stored on an older agent key is not in effect, so it is not listed.
func listItem(rec APIKeyRecord) ListItem {
	return ListItem{
		ID:          rec.ID,
		Subject:     rec.Subject,
		Label:       rec.Label,
		Roles:       rolesInEffect(rec),
		AgentID:     rec.AgentID,
		Environment: rec.Environment,
		CreatedAt:   rec.CreatedAt,
		LastUsedAt:  rec.LastUsedAt,
	}
}

type updateRequest struct {
	// AddRoles are roles to add to the key. Roles are only ever added:
	// a role is taken away by revoking the key and minting another.
	AddRoles []string `json:"add_roles"`
}

// update adds roles to an existing key, so one key
// can carry an agent from observe to enforce instead of a second key
// being minted when the agent is given a card. The rules are minting's:
// only tenant_owner or admin may do it, never a runtime key, and a
// non-admin caller may add only roles it holds itself. The key keeps
// its id, plaintext, agent and environment; only its roles grow.
func (h *KeysHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := IdentityFrom(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err)
		return
	}
	if id.Tenant == "" {
		writeJSONError(w, http.StatusUnauthorized, errors.New("identity has no tenant"))
		return
	}
	if !isKeyManagement(id) {
		writeJSONError(w, http.StatusForbidden, errors.New(
			"changing an API key's roles requires the tenant_owner or admin role; a runtime key, or a key bound to an agent, cannot grant roles"))
		return
	}
	keyID := strings.TrimSpace(r.PathValue("id"))
	if keyID == "" {
		writeJSONError(w, http.StatusBadRequest, errors.New("missing id"))
		return
	}
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.AddRoles) == 0 {
		writeJSONError(w, http.StatusBadRequest, errors.New("add_roles must name at least one role"))
		return
	}
	for _, role := range req.AddRoles {
		if !validRole(role) {
			writeJSONError(w, http.StatusBadRequest, errors.New("role contains invalid characters"))
			return
		}
	}
	// The same non-escalation rule as minting: granting a role to an
	// existing key is minting that authority onto it.
	if !rolesAreSubsetOf(req.AddRoles, id.Roles) {
		writeJSONError(w, http.StatusForbidden, errors.New(
			"requested roles exceed caller authority; granting a role the caller does not hold is not permitted"))
		return
	}
	if !h.requireStepUp(w, r, id, req.AddRoles) {
		return
	}
	if slices.Contains(req.AddRoles, RoleConnector) {
		writeJSONError(w, http.StatusBadRequest, errors.New(
			"a "+RoleConnector+" key is minted as one; the role is not added to an existing key"))
		return
	}
	target, err := h.activeKey(r.Context(), id.Tenant, keyID)
	if errors.Is(err, ErrAPIKeyNotFound) {
		writeJSONError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	if slices.Contains(target.Roles, RoleConnector) {
		writeJSONError(w, http.StatusBadRequest, errors.New(
			"a "+RoleConnector+" key holds only that role; mint another key instead"))
		return
	}
	addsGateway := slices.Contains(req.AddRoles, RoleGatewayAgent)
	if hasAccountRole(req.AddRoles) || addsGateway {
		if hasAccountRole(req.AddRoles) && target.AgentID != "" {
			writeJSONError(w, http.StatusBadRequest, errors.New(
				"a key bound to an agent cannot hold the tenant_owner or admin role; it acts as that agent, never for the account"))
			return
		}
		if addsGateway && target.AgentID == "" {
			writeJSONError(w, http.StatusBadRequest, errors.New(
				"a "+RoleGatewayAgent+" key must be bound to an agent; create a new key with agent_id"))
			return
		}
	}
	rec, err := h.store.AddRoles(r.Context(), id.Tenant, keyID, req.AddRoles)
	if errors.Is(err, ErrAPIKeyNotFound) {
		writeJSONError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, listItem(rec))
}

func (h *KeysHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := IdentityFrom(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err)
		return
	}
	keyID := strings.TrimSpace(r.PathValue("id"))
	if keyID == "" {
		writeJSONError(w, http.StatusBadRequest, errors.New("missing id"))
		return
	}
	if id.Tenant == "" {
		writeJSONError(w, http.StatusUnauthorized, errors.New("identity has no tenant"))
		return
	}
	if !isKeyManagement(id) {
		writeJSONError(w, http.StatusForbidden, errors.New(
			"revoking an API key requires the tenant_owner or admin role; a runtime key, or a key bound to an agent, cannot revoke keys"))
		return
	}

	// Tenant scoping: only allow revoking keys that belong to the
	// caller's tenant.
	target, err := h.activeKey(r.Context(), id.Tenant, keyID)
	if errors.Is(err, ErrAPIKeyNotFound) {
		writeJSONError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	if len(id.DeviceKey) > 0 && target.AgentID == "" {
		// A phone revokes only an agent's key, as it mints only
		// one; a connector key is never bound to an agent. The account's
		// keys and planque's connector key stay the dashboard's to revoke.
		writeJSONError(w, http.StatusForbidden, errors.New("from a phone, only a key bound to an agent can be revoked; revoke this one in KIFF"))
		return
	}

	if err := h.store.Revoke(r.Context(), keyID, h.clock()); err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			writeJSONError(w, http.StatusNotFound, err)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// validRole returns true for the conservative role-name alphabet
// the cloud accepts: lowercase letters, digits, underscore, dash,
// dot, colon. Anything else is a 400 to keep stored values
// auditable. Tightens later if needed.
func validRole(role string) bool {
	if role == "" || len(role) > 64 {
		return false
	}
	for i := 0; i < len(role); i++ {
		c := role[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.' || c == ':':
		default:
			return false
		}
	}
	return true
}

// isKeyManagement reports whether the identity may mint or revoke a
// tenant's API keys. The management roles are `tenant_owner` — which
// the middleware appends to session identities (the dashboard owner,
// see sessionOwnerRoles) — and `admin`, the bootstrap role
// cmd/kiff-tenant stamps on a fresh tenant's first key.
//
// The point is credential separation, mirroring the domain-authoring
// gate: a plain runtime key (e.g. role `ops_agent`)
// is the agent's decide credential and carries neither management
// role, so it cannot mint sibling keys or revoke existing ones. That
// closes the "a leaked runtime key manufactures more runtime keys"
// gap — key proliferation from a single compromised credential — that
// the non-escalation rule alone did not (it only bounds which roles a
// mint may grant, not whether a runtime key may mint at all). Both
// checks stand: this gate decides *who may mint*, rolesAreSubsetOf
// decides *what roles they may grant*.
func isKeyManagement(id Identity) bool {
	return id.ManagesAccount()
}

// hasAccountRole reports whether roles names an account role.
func hasAccountRole(roles []string) bool {
	for _, r := range roles {
		if IsAccountRole(r) {
			return true
		}
	}
	return false
}

// activeKey returns the tenant's active key with the given id. A key's
// agent binding never changes, so reading it before AddRoles is not a
// race.
func (h *KeysHandler) activeKey(ctx context.Context, tenant, id string) (APIKeyRecord, error) {
	recs, err := h.store.ListActive(ctx, tenant)
	if err != nil {
		return APIKeyRecord{}, err
	}
	for _, rec := range recs {
		if rec.ID == id {
			return rec, nil
		}
	}
	return APIKeyRecord{}, ErrAPIKeyNotFound
}

// rolesAreSubsetOf reports whether `requested` is a permissible
// role set for a caller holding `held`. The cloud's role hierarchy
// has one carve-out: a caller with the "admin" role can mint a
// key with any valid role. For non-admins, the rule is strict
// set inclusion: requested ⊆ held. Empty `requested` is always
// permissible. Both inputs tolerated as nil; comparison is
// case-sensitive (validRole already constrains the alphabet to
// lowercase).
//
// Why "admin" is special: an admin role is what cmd/kiff-tenant
// stamps on the bootstrap key for a fresh tenant; every other
// role on that tenant flows downstream from someone holding it.
// Without an admin shortcut, an admin could only ever mint admin
// keys, and the cloud could never grow new role names.
func rolesAreSubsetOf(requested, held []string) bool {
	if len(requested) == 0 {
		return true
	}
	for _, h := range held {
		if h == "admin" {
			return true
		}
	}
	holdSet := make(map[string]struct{}, len(held))
	for _, r := range held {
		holdSet[r] = struct{}{}
	}
	for _, r := range requested {
		if _, ok := holdSet[r]; !ok {
			return false
		}
	}
	return true
}

// validAgentID reports whether an agent or environment binding is
// acceptable. Empty is valid (an unbound key). Otherwise it is an opaque identifier matched
// byte-for-byte against a proposal's actor_id, so it only has to be
// bounded and printable; the card's holder name is the usual value.
func validAgentID(agentID string) bool {
	if len(agentID) > 200 {
		return false
	}
	for _, r := range agentID {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
