package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/permit"
)

// CoachRuns reads an account's kept runs (meter.Postgres).
type CoachRuns interface {
	Runs(ctx context.Context, tenantID, runtimeAgent string, scope *meter.TaskScope, from, to time.Time, page *meter.RunPage, limit int) ([]meter.RunEntry, *meter.RunPage, error)
}

// coachCreditsWindow is how far back Coach's credits read looks, and
// coachCreditsMax the most runs it answers.
const (
	coachCreditsWindow = 30 * 24 * time.Hour
	coachCreditsMax    = 500
)

// CoachEstimateTool is the one Coach tool whose permit reads credits: a
// permit KIFF issued for any other tool is refused here.
const CoachEstimateTool = "taf_estimate"

// CoachCredits serves POST /v1/coach/credits {permit, agent}: one agent's
// runs that ended in the last 30 days, newest first, at most 500, in
// credits, for Coach's estimate of what a taf will likely use. Coach's
// tools run in tafs cloud, which holds no session of the person's: the
// permit KIFF issued for the Coach call is the credential, checked as
// POST /v1/coach/act checks it.
//
// The permit's person reads the agent's runs when they may use or oversee
// it, as act answers for them; otherwise, and for an agent the account
// doesn't have, the answer is no runs. A counted run gives the credits KIFF
// kept for it; a free run why it was free, and, for a trial only, the
// credits it would have used had it counted. Never the AI cost.
type CoachCredits struct {
	Grants  *RunGrants
	Targets permit.Targets
	Runs    CoachRuns
}

// Register attaches the route to the public mux: the permit is its
// credential.
func (c *CoachCredits) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/coach/credits", c.credits)
}

// coachCreditsRun is one run as Coach's credits read answers it.
type coachCreditsRun struct {
	Run     string    `json:"run"`
	Ended   time.Time `json:"ended"`
	Credits int64     `json:"credits"`
	Counted bool      `json:"counted"`
	// Free is why the run used no credits (setup, trial, failed); "" when
	// counted.
	Free string `json:"free"`
}

func (c *CoachCredits) credits(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Permit string `json:"permit"`
		Agent  string `json:"agent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil ||
		strings.TrimSpace(req.Permit) == "" || strings.TrimSpace(req.Agent) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "send the permit and the agent"})
		return
	}
	g := c.Grants
	now := g.now()
	claims, ok := coachPermit(w, r, g, c.Targets, req.Permit, now)
	if !ok {
		return
	}
	if claims.Details[0].Tool != CoachEstimateTool {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_permit", "detail": "not a permit for Coach's estimate"})
		return
	}
	ctx := r.Context()
	tenantID, person, agent := claims.Details[0].Tenant, claims.Act.Sub, strings.TrimSpace(req.Agent)
	none := map[string]any{"runs": []coachCreditsRun{}}
	rec, err := g.Access.liveRecord(ctx, tenantID, agent)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusOK, none)
		return
	}
	a, err := g.Access.Answer(ctx, tenantID, rec.ID, person)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	if !a.Use && !a.Oversee {
		writeJSON(w, http.StatusOK, none)
		return
	}
	// The scope with no subject is the agent's runs alone.
	list, _, err := c.Runs.Runs(ctx, tenantID, "", &meter.TaskScope{Agents: []string{rec.ID}}, now.Add(-coachCreditsWindow), now, nil, coachCreditsMax)
	if err != nil {
		logger := g.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.ErrorContext(ctx, "coach credits: runs failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return
	}
	runs := make([]coachCreditsRun, 0, len(list))
	for _, e := range list {
		if e.Agent != rec.ID {
			continue
		}
		run := coachCreditsRun{Run: e.Run, Ended: e.Ended.UTC(), Counted: e.Counted, Free: e.Free}
		switch {
		case e.Counted:
			run.Credits, run.Free = e.Credits, ""
		case e.Free == meter.FreeTrial:
			// What it would have used: the conversion stays in KIFF.
			run.Credits = meter.Credits(e.CostMicroUSD)
		}
		runs = append(runs, run)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}
