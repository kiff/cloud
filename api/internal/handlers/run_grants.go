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

	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/permit"
)

// RunGrants mints and checks run grants: KIFF's signed word that a person
// may use an agent, carried by the run they asked for. The person's app
// asks for one with their own session; the runtime that runs the agent
// resolves it with the agent's own key; and decide checks it on each of
// the run's calls, so every call, and any hold it opens, is that
// person's. Each use checks again, live, that they may use the agent.
//
//	POST /v1/me/run-grants          {agent}: a person's own grant
//	POST /v1/me/run-grants/resolve  {grant}: the agent's key, for its own agent
//
// Grants are signed with the permit key, and verified with it and the keys
// KIFF still publishes, so rotating that key rotates them too.
type RunGrants struct {
	Key       permit.Key
	Published []permit.PublicKey
	Iss       string
	Access    *AccessHandler
	Now       func() time.Time
	Logger    *slog.Logger
}

// NewRunGrants wires run grants to the permit issuer's key and to the
// permission answer. Nil when permits are off: no grant can be minted,
// and a call carrying one is decided as before.
func NewRunGrants(issuer *permit.Issuer, a *AccessHandler, logger *slog.Logger) *RunGrants {
	if issuer == nil || a == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RunGrants{Key: issuer.Key, Published: append([]permit.PublicKey{issuer.Key.Public()}, issuer.Others...),
		Iss: issuer.Iss, Access: a, Now: time.Now, Logger: logger}
}

func (g *RunGrants) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Register attaches the routes to the authenticated mux.
func (g *RunGrants) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/me/run-grants", g.mint)
	mux.HandleFunc("POST /v1/me/run-grants/resolve", g.resolve)
}

// errNoUse is a grant for someone who may not use its agent now.
var errNoUse = errors.New("run grant: this person may not use this agent")

// Check verifies a run grant for agent of tenant, and that the person it
// names may use the agent now, and returns who they are.
func (g *RunGrants) Check(ctx context.Context, tenantID, agent, token string) (string, error) {
	rg, err := permit.VerifyRunGrant(strings.TrimSpace(token), g.Published, g.now())
	if err != nil {
		return "", err
	}
	if rg.Tenant != tenantID || rg.Agent != agent {
		return "", permit.ErrInvalidToken
	}
	a, err := g.Access.Answer(ctx, tenantID, agent, rg.Sub)
	if err != nil {
		return "", err
	}
	if !a.Use {
		return "", errNoUse
	}
	return rg.Sub, nil
}

func (g *RunGrants) mint(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.IsPerson() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "a person asks for their own run grant"})
		return
	}
	var req struct {
		Agent string `json:"agent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Agent) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "name the agent"})
		return
	}
	agent := strings.TrimSpace(req.Agent)
	live, err := g.Access.liveAgent(r.Context(), tenantID, agent)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	if !live {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "detail": "no agent of the account by that id"})
		return
	}
	a, err := g.Access.Answer(r.Context(), tenantID, agent, id.Subject)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	if !a.Use {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no_access", "need": "use"})
		return
	}
	tok, rg, err := g.Key.SignRunGrant(g.Iss, tenantID, agent, id.Subject, g.now())
	if err != nil {
		g.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grant": tok, "subject": rg.Sub, "agent": rg.Agent,
		"expires_at": time.Unix(rg.Exp, 0).UTC()})
}

func (g *RunGrants) resolve(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.APIKey || id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "an agent's key resolves its own runs' grants"})
		return
	}
	var req struct {
		Grant string `json:"grant"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Grant) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "name the grant"})
		return
	}
	sub, err := g.Check(r.Context(), tenantID, id.AgentID, req.Grant)
	switch {
	case errors.Is(err, permit.ErrInvalidToken):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "run_grant", "detail": "not a valid run grant for this agent"})
	case errors.Is(err, errNoUse):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no_access", "need": "use"})
	case err != nil:
		g.fail(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"subject": sub})
	}
}

func (g *RunGrants) fail(w http.ResponseWriter, r *http.Request, err error) {
	g.Logger.ErrorContext(r.Context(), "run grants failed", slog.String("err", err.Error()))
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "run grant failed"})
}

type runGrantSubjectKey struct{}

// withRunGrantSubject marks ctx as deciding a call of subject's run, as its
// run grant says.
func withRunGrantSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, runGrantSubjectKey{}, subject)
}

// runGrantSubjectOf is the person whose run ctx decides a call of, or "".
func runGrantSubjectOf(ctx context.Context) string {
	s, _ := ctx.Value(runGrantSubjectKey{}).(string)
	return s
}

// Answerers are the account's people who may answer a hold that asked
// opened, by sign-in subject: active members whose role answers holds
// under the account's separation of duties. With it on, never asked; off,
// asked answers their own run's holds like anyone else (HoldGate.Check).
// A hold reaches them alone.
func (g *RunGrants) Answerers(ctx context.Context, tenantID, asked string) ([]string, error) {
	out := []string{}
	if g.Access.members == nil {
		return out, nil
	}
	gov, err := g.Access.members.Governance(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	people, err := g.Access.members.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, m := range people {
		if m.Active() && (m.Subject != asked || !gov.SeparationOfDuties) && membership.AnswersHolds(m.Role, gov.SeparationOfDuties) {
			out = append(out, m.Subject)
		}
	}
	sort.Strings(out)
	return out, nil
}
