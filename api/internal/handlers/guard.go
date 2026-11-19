package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/candidate"
	"github.com/kiff/cloud/api/internal/guard"
)

type GuardHandler struct {
	store  guard.Store
	logger *slog.Logger
	now    func() time.Time
}

type GuardOption func(*GuardHandler)

func WithGuardLogger(l *slog.Logger) GuardOption {
	return func(h *GuardHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

func WithGuardClock(now func() time.Time) GuardOption {
	return func(h *GuardHandler) {
		if now != nil {
			h.now = now
		}
	}
}

func NewGuardHandler(store guard.Store, opts ...GuardOption) *GuardHandler {
	if store == nil {
		return nil
	}
	h := &GuardHandler{
		store:  store,
		logger: slog.Default(),
		now:    func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *GuardHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/guard/connect", h.connect)
	mux.HandleFunc("GET /v1/guard/connections", h.list)
	mux.HandleFunc("GET /v1/guard/runtimes", h.runtimes)
	mux.HandleFunc("POST /v1/guard/observations", h.saveObservation)
	mux.HandleFunc("GET /v1/guard/observations", h.getObservation)
	mux.HandleFunc("GET /v1/guard/candidates", h.candidates)
}

type guardConnectRequest struct {
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	AgentID     string `json:"agent_id"`
	Workflow    string `json:"workflow,omitempty"`
	Adapter     string `json:"adapter"`
	SDKVersion  string `json:"sdk_version,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

type guardConnectResponse struct {
	RuntimeID         string           `json:"runtime_id"`
	HeartbeatInterval string           `json:"heartbeat_interval"`
	TenantID          string           `json:"tenant_id"`
	Project           string           `json:"project"`
	Environment       string           `json:"environment"`
	AgentID           string           `json:"agent_id"`
	Workflow          string           `json:"workflow"`
	Adapter           string           `json:"adapter"`
	SDKVersion        string           `json:"sdk_version,omitempty"`
	Mode              string           `json:"mode"`
	KeyID             string           `json:"key_id,omitempty"`
	FirstSeenAt       time.Time        `json:"first_seen_at"`
	LastSeenAt        time.Time        `json:"last_seen_at"`
	SeenCount         uint64           `json:"seen_count"`
	Connection        guard.Connection `json:"connection"`
}

type guardListResponse struct {
	Connections []guard.Connection `json:"connections"`
}

type guardRuntimesResponse struct {
	Runtimes []guardConnectResponse `json:"runtimes"`
}

type guardObservationRequest struct {
	Project     string                  `json:"project,omitempty"`
	Environment string                  `json:"environment,omitempty"`
	AgentID     string                  `json:"agent_id"`
	Workflow    string                  `json:"workflow,omitempty"`
	Adapter     string                  `json:"adapter"`
	SDKVersion  string                  `json:"sdk_version,omitempty"`
	Mode        string                  `json:"mode,omitempty"`
	Tools       []guard.ToolObservation `json:"tools"`
}

type guardObservationResponse struct {
	Observation guard.Observation `json:"observation"`
}

// guardCandidatesResponse is the stable Studio-facing shape for
// GET /v1/guard/candidates: one entry per connected runtime that has a
// stored observation, each carrying its derived, grounded candidate
// controls. The top-level array is always present (never null) so a
// consumer can render an empty state without special-casing.
type guardCandidatesResponse struct {
	Source   string                        `json:"source"`
	Runtimes []candidate.RuntimeCandidates `json:"runtimes"`
}

func (h *GuardHandler) connect(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}

	var req guardConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	agentID, ok := runtimeAgentFor(w, id, req.AgentID)
	if !ok {
		return
	}
	req.AgentID = agentID
	row, err := h.store.Touch(r.Context(), id.Tenant, guard.TouchInput{
		Project:     req.Project,
		Environment: req.Environment,
		AgentID:     req.AgentID,
		Workflow:    req.Workflow,
		Adapter:     req.Adapter,
		SDKVersion:  req.SDKVersion,
		Mode:        req.Mode,
		KeyID:       id.Metadata["api_key_id"],
		SeenAt:      h.now(),
	})
	if err != nil {
		if errors.Is(err, guard.ErrInvalidConnection) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		h.logger.ErrorContext(r.Context(), "guard: connect failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "guard connection failed"})
		return
	}
	writeJSON(w, http.StatusOK, guardConnectResponseFrom(row))
}

func (h *GuardHandler) list(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	rows, err := h.store.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "guard: list failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list guard connections failed"})
		return
	}
	writeJSON(w, http.StatusOK, guardListResponse{Connections: rows})
}

func (h *GuardHandler) runtimes(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	rows, err := h.store.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "guard: list runtimes failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list guard runtimes failed"})
		return
	}
	out := make([]guardConnectResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, guardConnectResponseFrom(row))
	}
	writeJSON(w, http.StatusOK, guardRuntimesResponse{Runtimes: out})
}

func (h *GuardHandler) saveObservation(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}

	var req guardObservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	agentID, ok := runtimeAgentFor(w, id, req.AgentID)
	if !ok {
		return
	}
	req.AgentID = agentID
	obs, err := h.store.SaveObservation(r.Context(), id.Tenant, guard.ObservationInput{
		Project:     req.Project,
		Environment: req.Environment,
		AgentID:     req.AgentID,
		Workflow:    req.Workflow,
		Adapter:     req.Adapter,
		SDKVersion:  req.SDKVersion,
		Mode:        req.Mode,
		KeyID:       id.Metadata["api_key_id"],
		Tools:       req.Tools,
		SeenAt:      h.now(),
	})
	if err != nil {
		if errors.Is(err, guard.ErrInvalidConnection) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		h.logger.ErrorContext(r.Context(), "guard: save observation failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save guard observation failed"})
		return
	}
	writeJSON(w, http.StatusOK, guardObservationResponse{Observation: obs})
}

func (h *GuardHandler) getObservation(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	obs, err := h.store.GetObservation(
		r.Context(),
		id.Tenant,
		r.URL.Query().Get("project"),
		r.URL.Query().Get("environment"),
		r.URL.Query().Get("agent_id"),
		r.URL.Query().Get("workflow"),
	)
	if err != nil {
		if errors.Is(err, guard.ErrObservationNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "guard observation not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "guard: get observation failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "get guard observation failed"})
		return
	}
	writeJSON(w, http.StatusOK, guardObservationResponse{Observation: obs})
}

// candidates derives runtime-grounded candidate controls from the tenant's
// stored guard observations. Read-only and deterministic: it lists the
// tenant's connections, pairs each with its stored observation, and runs
// the candidate derivation + grounding gate. Optional query params
// (project, environment, agent_id, workflow) scope the result to a single
// runtime; absent params return every runtime that has an observation.
//
// Source of truth is the connected runtime, never an uploaded manifest:
// candidates carry source "kiff-guard runtime", thresholds/states stay
// null, and every set ships its grounding report.
func (h *GuardHandler) candidates(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}

	conns, err := h.store.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "guard: candidates list failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "derive guard candidates failed"})
		return
	}

	q := r.URL.Query()
	wantProject := strings.TrimSpace(q.Get("project"))
	wantEnv := strings.TrimSpace(q.Get("environment"))
	wantAgent := strings.TrimSpace(q.Get("agent_id"))
	wantWorkflow := strings.TrimSpace(q.Get("workflow"))

	out := make([]candidate.RuntimeCandidates, 0, len(conns))
	for _, conn := range conns {
		if wantProject != "" && conn.Project != wantProject {
			continue
		}
		if wantEnv != "" && conn.Environment != wantEnv {
			continue
		}
		if wantAgent != "" && conn.AgentID != wantAgent {
			continue
		}
		if wantWorkflow != "" && conn.Workflow != wantWorkflow {
			continue
		}

		obs, err := h.store.GetObservation(r.Context(), id.Tenant, conn.Project, conn.Environment, conn.AgentID, conn.Workflow)
		if err != nil {
			if errors.Is(err, guard.ErrObservationNotFound) {
				continue // connected but never pushed a catalog — nothing to derive
			}
			h.logger.ErrorContext(r.Context(), "guard: candidates observation read failed",
				slog.String("tenant", id.Tenant),
				slog.String("err", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "derive guard candidates failed"})
			return
		}
		out = append(out, candidate.DeriveForObservation(conn, obs))
	}

	writeJSON(w, http.StatusOK, guardCandidatesResponse{
		Source:   candidate.SourceRuntime,
		Runtimes: out,
	})
}

func guardConnectResponseFrom(c guard.Connection) guardConnectResponse {
	return guardConnectResponse{
		RuntimeID:         guardRuntimeID(c),
		HeartbeatInterval: "60s",
		TenantID:          c.TenantID,
		Project:           c.Project,
		Environment:       c.Environment,
		AgentID:           c.AgentID,
		Workflow:          c.Workflow,
		Adapter:           c.Adapter,
		SDKVersion:        c.SDKVersion,
		Mode:              c.Mode,
		KeyID:             c.KeyID,
		FirstSeenAt:       c.FirstSeenAt,
		LastSeenAt:        c.LastSeenAt,
		SeenCount:         c.SeenCount,
		Connection:        c,
	}
}

func guardRuntimeID(c guard.Connection) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		c.TenantID,
		c.Project,
		c.Environment,
		c.AgentID,
		c.Workflow,
	}, "\x00")))
	return "grt_" + hex.EncodeToString(h[:8])
}

// runtimeAgentFor is the agent a guard runtime registers or reports as.
// A key bound to an agent carries the agent's identity:
// an empty agent_id becomes the key's agent, and one naming another
// agent is refused, so a runtime cannot heartbeat or report under a
// name its credential does not hold. Unbound keys and sessions keep
// naming the agent themselves. On refusal it has written the response.
func runtimeAgentFor(w http.ResponseWriter, id auth.Identity, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if !id.APIKey || id.AgentID == "" {
		return requested, true
	}
	if requested == "" || requested == id.AgentID {
		return id.AgentID, true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": "this key is bound to agent " + id.AgentID + " and cannot register a runtime as " + requested,
	})
	return "", false
}
