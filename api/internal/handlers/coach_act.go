package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/permit"
)

// CoachAct serves POST /v1/coach/act {permit, agent?}: what the person
// talking to an account's Coach may do with its tafs. Coach's tools run in
// the tafs app's backend, which holds no key of the account's and no
// person's session in a tool call: the permit KIFF issued for that very
// call is the credential, and it names the person (act.sub) when the call
// was part of their run.
//
// KIFF checks its own permit: its signature, that it hasn't expired, that
// its audience is a permit target the account registered, that it is for
// the account's Setup, and that it names a person. With agent, it answers
// what that person may do with it (an editor manages only the tafs they
// made), and, when they may use it, their run grant for it,
// at most one per permit and agent. Without, it answers for each of the
// account's agents.
type CoachAct struct {
	Grants  *RunGrants
	Targets permit.Targets
	// Used records each grant a permit got, so a permit gets at most one
	// per agent.
	Used auth.SignedRequests
}

// Register attaches the route to the public mux: the permit is its
// credential.
func (c *CoachAct) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/coach/act", c.act)
}

func (c *CoachAct) act(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Permit string `json:"permit"`
		Agent  string `json:"agent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Permit) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "send the permit"})
		return
	}
	g := c.Grants
	now := g.now()
	claims, ok := coachPermit(w, r, g, c.Targets, req.Permit, now)
	if !ok {
		return
	}
	ctx := r.Context()
	tenantID, person := claims.Details[0].Tenant, claims.Act.Sub
	agent := strings.TrimSpace(req.Agent)
	if agent == "" {
		list, err := g.Access.agents.List(ctx, tenantID)
		if err != nil {
			g.fail(w, r, err)
			return
		}
		out := map[string]access.Answer{}
		for _, a := range list {
			if a.ArchivedAt != nil {
				continue
			}
			if out[a.ID], err = g.Access.Answer(ctx, tenantID, a.ID, person); err != nil {
				g.fail(w, r, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"subject": person, "agents": out, "checked_at": now.UTC()})
		return
	}
	rec, err := g.Access.liveRecord(ctx, tenantID, agent)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "detail": "no agent of the account by that id"})
		return
	}
	a, err := g.Access.Answer(ctx, tenantID, rec.ID, person)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	out := map[string]any{"subject": person, "use": a.Use, "oversee": a.Oversee, "manage": a.Manage, "groups": a.Groups, "checked_at": now.UTC()}
	if a.Use {
		// One grant per permit and agent: a permit lives minutes, and
		// each is one Coach tool call.
		id := sha256.Sum256([]byte("coach-act\x00" + claims.Jti + "\x00" + agent))
		fresh, err := c.Used.Use(ctx, id[:], now, time.Unix(claims.Exp, 0).Add(time.Minute))
		if err != nil {
			g.fail(w, r, err)
			return
		}
		if fresh {
			tok, rg, err := g.Key.SignRunGrant(g.Iss, tenantID, agent, person, now)
			if err != nil {
				g.fail(w, r, err)
				return
			}
			out["grant"], out["expires_at"] = tok, time.Unix(rg.Exp, 0).UTC()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// coachPermit checks the permit KIFF issued for a Coach tool call: its
// signature, that it hasn't expired, that it names a person (act.sub),
// that its audience is a permit target the account registered for that
// tool, and that it is for the account's Setup. Otherwise it answers 401
// (or 500 when a store fails) and returns false. The account is the
// permit's first detail's tenant, the person act.sub.
func coachPermit(w http.ResponseWriter, r *http.Request, g *RunGrants, targets permit.Targets, raw string, now time.Time) (permit.Claims, bool) {
	claims, err := permit.VerifyPermit(strings.TrimSpace(raw), g.Published, now)
	if err != nil || claims.Act == nil || claims.Act.Sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_permit", "detail": "not a permit of KIFF's for a person's call"})
		return permit.Claims{}, false
	}
	ctx := r.Context()
	tenantID := claims.Details[0].Tenant
	// Its audience is a permit target the account itself registered.
	list, err := targets.List(ctx, tenantID)
	if err != nil {
		g.fail(w, r, err)
		return permit.Claims{}, false
	}
	registered := false
	for _, t := range list {
		if t.Audience == claims.Aud && t.Tool == claims.Details[0].Tool {
			registered = true
		}
	}
	// It is for the account's Setup, Coach.
	setup, err := g.Access.setupAgents(ctx, tenantID)
	if err != nil {
		g.fail(w, r, err)
		return permit.Claims{}, false
	}
	if !registered || !setup[claims.Sub] {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_permit", "detail": "not a permit for this account's Coach"})
		return permit.Claims{}, false
	}
	return claims, true
}
