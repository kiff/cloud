package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/coverage"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/guard"
	"github.com/kiff/cloud/api/internal/proposals"
)

// CoverageHandler serves GET /v1/guard/coverage: the tenant's governed
// surface measured against what their connected runtimes can actually
// do.
//
// It reads three sources that already exist and were never joined —
// guard connections, the stored tool catalog each runtime pushes, and
// the tenant's activated controls — and optionally a fourth, the
// recorded decisions, to measure calls that never reached the gate.
//
// Read-only. It creates nothing and changes nothing, so it is safe to
// call on every dashboard load.
type CoverageHandler struct {
	guards   guard.Store
	controls domain.ControlStore
	// decisions is optional. Without it the response still reports
	// coverage and sets drift_measured=false rather than implying a
	// clean drift result.
	decisions proposals.Reader
	logger    *slog.Logger
}

type CoverageOption func(*CoverageHandler)

func WithCoverageLogger(l *slog.Logger) CoverageOption {
	return func(h *CoverageHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithCoverageDecisions enables the drift join. Without it the handler
// reports coverage only.
func WithCoverageDecisions(r proposals.Reader) CoverageOption {
	return func(h *CoverageHandler) {
		if r != nil {
			h.decisions = r
		}
	}
}

// NewCoverageHandler returns nil when either required store is missing,
// so Register becomes a no-op and the route is absent rather than
// serving a report built from half its inputs.
func NewCoverageHandler(guards guard.Store, controls domain.ControlStore, opts ...CoverageOption) *CoverageHandler {
	if guards == nil || controls == nil {
		return nil
	}
	h := &CoverageHandler{guards: guards, controls: controls, logger: slog.Default()}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *CoverageHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/guard/coverage", h.report)
}

// coverageResponse wraps the report with the limits a reader needs in
// order not to over-read the number. They ship in the payload rather
// than in documentation because the number is a security claim and the
// caveats travel with it or get lost.
type coverageResponse struct {
	coverage.Report
	Limits []string `json:"limits"`
}

// coverageLimits states what the report cannot see. Order is stable so
// a client can render them without sorting.
func coverageLimits(driftMeasured bool) []string {
	limits := []string{
		"Covers only tools a connected kiff-guard runtime reports. An agent that reaches a side effect by another route is not counted here.",
		"A control is matched to a tool by environment and action name. Workflow is not part of the match, so an action governed in one workflow reads as governed in all of them.",
	}
	if driftMeasured {
		limits = append(limits,
			"Observed call counts are reported by the runtime and reset when it restarts, while decisions accumulate. Non-zero undecided means calls skipped the gate; zero is not proof that none did.")
	}
	return limits
}

func (h *CoverageHandler) report(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	tenantID := strings.TrimSpace(id.Tenant)
	if tenantID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}

	conns, err := h.guards.List(r.Context(), tenantID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "coverage: list connections failed",
			slog.String("tenant", tenantID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "coverage report failed"})
		return
	}

	controls, err := h.controls.List(r.Context(), tenantID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "coverage: list controls failed",
			slog.String("tenant", tenantID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "coverage report failed"})
		return
	}

	q := r.URL.Query()
	wantProject := strings.TrimSpace(q.Get("project"))
	wantEnv := strings.TrimSpace(q.Get("environment"))
	wantAgent := strings.TrimSpace(q.Get("agent_id"))
	wantWorkflow := strings.TrimSpace(q.Get("workflow"))

	inputs := make([]coverage.RuntimeInput, 0, len(conns))
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

		obs, err := h.guards.GetObservation(r.Context(), tenantID, conn.Project, conn.Environment, conn.AgentID, conn.Workflow)
		if err != nil {
			if errors.Is(err, guard.ErrObservationNotFound) {
				// Connected but never pushed a catalog. A connection
				// with no tools says nothing about coverage either way,
				// so it is omitted rather than counted as fully covered.
				continue
			}
			h.logger.ErrorContext(r.Context(), "coverage: observation read failed",
				slog.String("tenant", tenantID), slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "coverage report failed"})
			return
		}
		inputs = append(inputs, coverage.RuntimeInput{Connection: conn, Observation: obs})
	}

	report := coverage.Build(inputs, controls, h.decisionCounts(r.Context(), tenantID))
	writeJSON(w, http.StatusOK, coverageResponse{
		Report: report,
		Limits: coverageLimits(report.DriftMeasured),
	})
}

// decisionCounts returns a memoized counter over recorded decisions, or
// nil when no reader is wired.
//
// A failed count reports ok=false for that action rather than failing
// the request: coverage is the load-bearing half of the report and is
// still correct without drift, so a slow or erroring audit read
// degrades the report instead of emptying it.
func (h *CoverageHandler) decisionCounts(ctx context.Context, tenantID string) coverage.DecisionCounts {
	if h.decisions == nil {
		return nil
	}
	type key struct{ env, action string }
	cache := map[key]int{}
	failed := map[key]bool{}

	return func(environment, action string) (int, bool) {
		action = strings.TrimSpace(action)
		if action == "" {
			return 0, false
		}
		k := key{env: strings.TrimSpace(environment), action: action}
		if failed[k] {
			return 0, false
		}
		if n, ok := cache[k]; ok {
			return n, true
		}
		_, total, err := h.decisions.CountDecisionsByOutcome(ctx, tenantID, proposals.DecisionQuery{
			Action:      k.action,
			Environment: k.env,
		})
		if err != nil {
			h.logger.ErrorContext(ctx, "coverage: decision count failed",
				slog.String("tenant", tenantID),
				slog.String("action", k.action),
				slog.String("err", err.Error()))
			failed[k] = true
			return 0, false
		}
		cache[k] = total
		return total, true
	}
}
