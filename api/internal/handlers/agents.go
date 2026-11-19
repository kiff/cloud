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

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/guard"
	"github.com/kiff/cloud/api/internal/mandate"
)

// AgentsHandler serves the tenant's agents:
//
//	GET  /v1/me/agents               — every agent, recorded or implicit
//	POST /v1/me/agents               — add an agent (management)
//	POST /v1/me/agents/{id}/archive  — archive an agent (management)
//
// An agent is one name. Most exist today only implicitly: as a card's
// holder, a bound key's agent_id or a guard runtime's agent_id. The
// list joins the records with those names, each marked with where it
// was seen, so nothing needs migrating and nothing is hidden.
type AgentsHandler struct {
	store    agents.Store
	mandates mandate.Store
	keys     auth.APIKeyStore
	runtimes guard.Store
	access   access.Store
	// unshare removes an archived agent's grants, recorded in the trail
	// (AccessHandler.RemoveAgentGrants). Nil: grants stay.
	unshare func(r *http.Request, id auth.Identity, tenantID, agentID string) error
	logger  *slog.Logger
	now     func() time.Time
}

// AgentsOption configures AgentsHandler.
type AgentsOption func(*AgentsHandler)

// WithAgentsAccess gives a taf the app starts to the person who started
// it, until they share it.
func WithAgentsAccess(s access.Store) AgentsOption {
	return func(h *AgentsHandler) { h.access = s }
}

// WithAgentsUnshare removes an agent's grants when it's archived, so a
// share never outlives its agent.
func WithAgentsUnshare(f func(r *http.Request, id auth.Identity, tenantID, agentID string) error) AgentsOption {
	return func(h *AgentsHandler) { h.unshare = f }
}

// WithAgentsSources supplies the stores implicit agents are read from.
// Any may be nil; that source is then skipped.
func WithAgentsSources(m mandate.Store, k auth.APIKeyStore, g guard.Store) AgentsOption {
	return func(h *AgentsHandler) { h.mandates, h.keys, h.runtimes = m, k, g }
}

// WithAgentsLogger sets the logger.
func WithAgentsLogger(l *slog.Logger) AgentsOption {
	return func(h *AgentsHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithAgentsClock sets the clock.
func WithAgentsClock(now func() time.Time) AgentsOption {
	return func(h *AgentsHandler) { h.now = now }
}

// NewAgentsHandler returns nil when store is nil.
func NewAgentsHandler(store agents.Store, opts ...AgentsOption) *AgentsHandler {
	if store == nil {
		return nil
	}
	h := &AgentsHandler{store: store, logger: slog.Default(), now: func() time.Time { return time.Now().UTC() }}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Register mounts the routes. A nil handler registers nothing.
func (h *AgentsHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/me/agents", h.list)
	mux.HandleFunc("POST /v1/me/agents", h.create)
	mux.HandleFunc("POST /v1/me/agents/{id}/archive", h.archive)
	mux.HandleFunc("POST /v1/agents/self/archive", h.archiveSelf)
}

// agentView is one agent on the wire. Sources says where the name was
// seen: "record" (added as an agent), "card", "key", "runtime".
type agentView struct {
	ID         string     `json:"id"`
	Recorded   bool       `json:"recorded"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	Sources    []string   `json:"sources"`
	// OutOfRule marks an implicit name that breaks the agent id rules —
	// a card holder issued before the rule applied to cards. It is
	// listed so nothing is hidden, but it has no record and cannot be
	// added or bound to a key; reissuing its cards under a valid name
	// is the way forward.
	OutOfRule bool `json:"out_of_rule,omitempty"`
}

func (h *AgentsHandler) list(w http.ResponseWriter, r *http.Request) {
	_, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	byID, err := h.collect(ctx, tenantID)
	if err != nil {
		h.logger.ErrorContext(ctx, "agents: list failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list agents failed"})
		return
	}
	out := make([]agentView, 0, len(byID))
	for _, v := range byID {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

// Known reports whether id is one of the tenant's agents, recorded or
// implicit, and not archived or out of rule: the agents the list shows
// as usable. Connecting an app as an agent requires it.
func (h *AgentsHandler) Known(ctx context.Context, tenantID, id string) (bool, error) {
	if h == nil {
		return false, errors.New("agents: not configured")
	}
	byID, err := h.collect(ctx, tenantID)
	if err != nil {
		return false, err
	}
	v, ok := byID[id]
	return ok && v.ArchivedAt == nil && !v.OutOfRule, nil
}

// collect gathers the tenant's agents: the records, and the names seen
// implicitly on Cards, bound keys and guard runtimes.
func (h *AgentsHandler) collect(ctx context.Context, tenantID string) (map[string]*agentView, error) {
	byID := map[string]*agentView{}
	see := func(id, source string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		v, ok := byID[id]
		if !ok {
			v = &agentView{ID: id}
			byID[id] = v
		}
		for _, s := range v.Sources {
			if s == source {
				return
			}
		}
		v.Sources = append(v.Sources, source)
	}

	records, err := h.store.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, a := range records {
		see(a.ID, "record")
		v := byID[a.ID]
		created := a.CreatedAt
		v.Recorded, v.CreatedAt, v.ArchivedAt = true, &created, a.ArchivedAt
	}
	// Implicit sources are best effort: a store that cannot answer
	// leaves its names out rather than failing the list, and says so in
	// the log. The records above are the authoritative part.
	if h.mandates != nil {
		if ms, err := h.mandates.List(ctx, tenantID); err == nil {
			for _, m := range ms {
				if m.ArchivedAt == nil {
					see(m.Subject, "card")
				}
			}
		} else {
			h.logger.WarnContext(ctx, "agents: card holders unavailable", slog.String("err", err.Error()))
		}
	}
	if h.keys != nil {
		if ks, err := h.keys.ListActive(ctx, tenantID); err == nil {
			for _, k := range ks {
				see(k.AgentID, "key")
			}
		} else {
			h.logger.WarnContext(ctx, "agents: bound keys unavailable", slog.String("err", err.Error()))
		}
	}
	if h.runtimes != nil {
		if cs, err := h.runtimes.List(ctx, tenantID); err == nil {
			for _, c := range cs {
				see(c.AgentID, "runtime")
			}
		} else {
			h.logger.WarnContext(ctx, "agents: guard runtimes unavailable", slog.String("err", err.Error()))
		}
	}
	for _, v := range byID {
		if _, err := agents.NormalizeID(v.ID); err != nil || v.ID != strings.TrimSpace(v.ID) {
			v.OutOfRule = true
		}
	}
	return byID, nil
}

type createAgentRequest struct {
	ID string `json:"id"`
}

func (h *AgentsHandler) create(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "adding an agent is a management action"})
		return
	}
	var req createAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	agentID, err := agents.NormalizeID(req.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a := agents.Agent{TenantID: tenantID, ID: agentID, CreatedAt: h.now(), CreatedBy: id.Subject}
	// The tafs app starts a taf with a phone's signed request: its agent's
	// calls are tafs tasks.
	if auth.DeviceSigned(r.Context()) {
		a.Kind = agents.KindTaf
	}
	if err := h.store.Create(r.Context(), a); err != nil {
		if errors.Is(err, agents.ErrExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "an agent named " + agentID + " already exists"})
			return
		}
		h.logger.ErrorContext(r.Context(), "agents: create failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "add agent failed"})
		return
	}
	if a.Kind == agents.KindTaf && id.IsPerson() {
		if err := access.GrantCreator(r.Context(), h.access, tenantID, a.ID, id.Subject, a.CreatedAt); err != nil {
			h.logger.ErrorContext(r.Context(), "agents: grant to its creator failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		}
	}
	created := a.CreatedAt
	writeJSON(w, http.StatusCreated, agentView{ID: a.ID, Recorded: true, CreatedAt: &created, Sources: []string{"record"}})
}

func (h *AgentsHandler) archive(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "archiving an agent is a management action"})
		return
	}
	agentID := strings.TrimSpace(r.PathValue("id"))
	if ok, err := mayChangeTafs(r.Context(), h.store, id, tenantID, agentID); err != nil || !ok {
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "agent lookup failed"})
			return
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": editorsOwnTafs})
		return
	}
	h.archiveAgent(w, r, id, tenantID, agentID)
}

// archiveSelf serves POST /v1/agents/self/archive: an agent's own key
// archives its own agent, and can reach no other. It's how a removed taf's
// runtime gives up its key: archiving revokes the agent's keys, this one
// included. A leaked key can so remove its own taf for good, a loss of
// service but never more authority, which is accepted. Setup's key can't.
func (h *AgentsHandler) archiveSelf(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.APIKey || id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only an agent's own key archives itself"})
		return
	}
	// Not Setup's: it acts as Coach, the agent most exposed to
	// prompt-driven calls, and losing it takes setup away from the
	// account. The owner removes Setup, through the signed route.
	if id.HasRole(auth.RoleSetupReader) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Setup's key doesn't archive Setup; its owner does"})
		return
	}
	h.archiveAgent(w, r, id, tenantID, id.AgentID)
}

// archiveAgent archives tenantID's agentID, once the caller may, and takes
// its keys and shares with it.
func (h *AgentsHandler) archiveAgent(w http.ResponseWriter, r *http.Request, id auth.Identity, tenantID, agentID string) {
	if err := h.store.Archive(r.Context(), tenantID, agentID, h.now()); err != nil {
		if errors.Is(err, agents.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no agent record named " + agentID})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "archive agent failed"})
		return
	}
	// Its keys go with it, so an archived agent's key reaches nothing and
	// the account's active keys don't pile up with each removed taf. A
	// failure is logged, not answered: the agent is archived either way.
	var revoked []string
	if h.keys != nil {
		var err error
		if revoked, err = revokeAgentKeys(r.Context(), h.keys, tenantID, agentID, h.now()); err != nil {
			h.logger.ErrorContext(r.Context(), "agents: revoking an archived agent's keys failed",
				slog.String("tenant", tenantID), slog.String("agent", agentID), slog.String("err", err.Error()))
		}
	}
	// Who archived it, and which keys went with it: the owner's phone, or
	// the agent's own key (a removed taf's runtime, or a leaked key).
	h.logger.InfoContext(r.Context(), "agents: archived",
		slog.String("tenant", tenantID), slog.String("agent", agentID), slog.String("by", id.ActorID()),
		slog.Bool("by_own_key", id.APIKey && id.AgentID == agentID), slog.Any("revoked_keys", revoked))
	// Its shares go with it. A failure is logged, not answered: the agent is
	// archived, and its grants give no one its use.
	if h.unshare != nil {
		if err := h.unshare(r, id, tenantID, agentID); err != nil {
			h.logger.ErrorContext(r.Context(), "agents: removing an archived agent's grants failed",
				slog.String("tenant", tenantID), slog.String("err", err.Error()))
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": agentID, "status": "archived"})
}

// revokeAgentKeys revokes tenantID's active keys bound to agentID, and
// answers the ids it revoked.
func revokeAgentKeys(ctx context.Context, keys auth.APIKeyStore, tenantID, agentID string, at time.Time) ([]string, error) {
	active, err := keys.ListActive(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var (
		revoked []string
		errs    []error
	)
	for _, k := range active {
		if k.AgentID != agentID {
			continue
		}
		if err := keys.Revoke(ctx, k.ID, at); err != nil && !errors.Is(err, auth.ErrAPIKeyRevoked) {
			errs = append(errs, err)
			continue
		}
		revoked = append(revoked, k.ID)
	}
	return revoked, errors.Join(errs...)
}

func agentsIdentity(w http.ResponseWriter, r *http.Request) (auth.Identity, string, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return auth.Identity{}, "", false
	}
	tenantID := strings.TrimSpace(id.Tenant)
	if tenantID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return auth.Identity{}, "", false
	}
	return id, tenantID, true
}
