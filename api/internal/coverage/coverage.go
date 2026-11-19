// Package coverage measures a tenant's governed surface against the
// surface their connected agent runtimes actually have.
//
// The question it answers is "what can your agents do that no control
// governs, and how would you find out?" Both halves of the answer are
// already in the database and were, until this package, never joined:
//
//   - cloud_meta.guard_observations holds the tool catalog each
//     connected kiff-guard runtime pushes to POST /v1/guard/observations,
//     including a per-tool observed_call_count. Its only consumer was
//     GET /v1/guard/candidates, which reframes it as Studio authoring
//     suggestions.
//   - domain.ControlStore holds the controls a tenant has activated,
//     each naming the action names it governs.
//
// Coverage is the join: an observed tool whose derived action is named
// by an active control is governed; everything else is capability the
// tenant has not governed. Drift is the second join, against recorded
// decisions: a governed tool whose observed call count exceeds the
// decisions the gate recorded for its action was invoked without the
// gate being consulted.
//
// # What this cannot see
//
// The guard reports the tools it wraps. An agent with a shell that
// reaches a side effect by some other route is invisible here, exactly
// as it is invisible to the gate itself. This measures the instrumented
// surface, never the total surface, and no number it produces should be
// read as "nothing else happened".
//
// Every function here is pure: no database, no clock, no network. The
// handler fetches; this package decides what the fetched rows mean.
package coverage

import (
	"sort"
	"strings"

	"github.com/kiff/cloud/api/internal/candidate"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/guard"
)

// Tool is one observed tool measured against the tenant's controls.
type Tool struct {
	Tool   string `json:"tool"`
	Action string `json:"action"`
	Risk   string `json:"risk"`

	// Governed reports whether an active control names this tool's
	// derived action in the runtime's environment.
	Governed bool `json:"governed"`

	// ControlID is the control that governs the action, empty when
	// Governed is false. When more than one active control names the
	// action, this is the most recently activated one.
	ControlID string `json:"control_id,omitempty"`

	// Mode is the governing control's posture ("observe" | "enforce"),
	// empty when ungoverned. A tool governed by an observe-mode control
	// is recorded but not withheld, so it is covered for evidence and
	// uncovered for enforcement. Callers that report an enforcement
	// number must not count these.
	Mode string `json:"mode,omitempty"`

	// ObservedCallCount is what the runtime reported. See Drift for why
	// it is a floor rather than a measurement.
	ObservedCallCount uint64 `json:"observed_call_count"`

	// Decisions is how many decisions the gate recorded for this
	// action. Only populated for governed tools when drift was
	// measured; nil otherwise, so "not measured" is distinguishable
	// from "measured zero".
	Decisions *int `json:"decisions,omitempty"`

	// Undecided is ObservedCallCount minus Decisions, floored at zero.
	// Non-zero means the tool ran more often than the gate was asked.
	// Nil when drift was not measured.
	Undecided *uint64 `json:"undecided,omitempty"`
}

// Runtime is the coverage report for one connected guard runtime.
type Runtime struct {
	Runtime candidate.RuntimeRef `json:"runtime"`
	Tools   []Tool               `json:"tools"`
	Summary Summary              `json:"summary"`
}

// Summary counts a set of tools. Ungoverned is the number that matters:
// it is capability with no control in front of it.
type Summary struct {
	Tools      int `json:"tools"`
	Governed   int `json:"governed"`
	Ungoverned int `json:"ungoverned"`

	// Enforced counts governed tools whose control is explicitly in
	// enforce mode. A control with an empty mode inherits the
	// tenant-wide posture, which this package does not resolve, so such
	// tools count as governed and not as enforced. That under-reports
	// enforcement rather than over-reporting it, which is the direction
	// this number should err in.
	Enforced int `json:"enforced"`

	// UngovernedHighRisk counts ungoverned tools the candidate deriver
	// ranked high risk. This is the number worth alerting on.
	UngovernedHighRisk int `json:"ungoverned_high_risk"`

	// Undecided totals per-tool Undecided across governed tools where
	// drift was measured. Nil when drift was not measured anywhere in
	// the set.
	Undecided *uint64 `json:"undecided,omitempty"`
}

// Report is the whole-tenant view: one entry per connected runtime that
// has pushed an observation, plus a tenant-wide summary.
type Report struct {
	// Source names where the observed surface came from, so a reader
	// knows this is grounded in a live runtime rather than an uploaded
	// manifest.
	Source   string    `json:"source"`
	Runtimes []Runtime `json:"runtimes"`
	Summary  Summary   `json:"summary"`

	// DriftMeasured reports whether decision counts were joined. False
	// when the caller did not supply them, in which case every
	// Decisions/Undecided field is nil and no drift claim is being made.
	DriftMeasured bool `json:"drift_measured"`
}

// DecisionCounts reports how many decisions the gate recorded for an
// action in an environment. The handler supplies it; keeping it a
// function rather than a map lets the caller decide whether to hit the
// database once per action or precompute.
//
// Returning ok=false means "not measured" and leaves the tool's drift
// fields nil, which is different from returning 0, true.
type DecisionCounts func(environment, action string) (count int, ok bool)

// ForRuntime measures one runtime's observed tools against the tenant's
// controls.
//
// Matching is on (environment, action name). Workflow is deliberately
// not part of the match: associating a control with a specific guard
// runtime workflow is deferred, so requiring it would report
// almost every tool as ungoverned for any tenant using a non-default
// workflow. The cost is the opposite error — an action governed in one
// workflow but not another reads as governed here. That over-reports
// coverage, which is the direction a security number should not err in,
// so it is stated on the wire via Report.Source and in the handler's
// response rather than left for a reader to discover.
//
// controls may contain superseded rows; they are ignored. counts may be
// nil, in which case no drift is measured.
func ForRuntime(conn guard.Connection, obs guard.Observation, controls []domain.Control, counts DecisionCounts) Runtime {
	derived := candidate.DeriveForObservation(conn, obs)
	governing := governingControls(controls, conn.Environment)

	tools := make([]Tool, 0, len(derived.Candidates))
	for _, c := range derived.Candidates {
		t := Tool{
			Tool:              c.Tool,
			Action:            c.Action,
			Risk:              c.Risk,
			ObservedCallCount: c.ObservedCallCount,
		}
		if ctrl, ok := governing[strings.ToUpper(strings.TrimSpace(c.Action))]; ok {
			t.Governed = true
			t.ControlID = ctrl.ID
			t.Mode = domain.NormalizeControlMode(ctrl.Mode)
			if counts != nil {
				if n, measured := counts(conn.Environment, c.Action); measured {
					decisions := n
					t.Decisions = &decisions
					t.Undecided = undecided(c.ObservedCallCount, n)
				}
			}
		}
		tools = append(tools, t)
	}

	return Runtime{
		Runtime: derived.Runtime,
		Tools:   tools,
		Summary: summarize(tools),
	}
}

// Build assembles the tenant-wide report. runtimes pairs each connected
// runtime with its stored observation; a runtime that has never pushed
// an observation is omitted by the caller, because a connection with no
// catalog says nothing about coverage either way.
func Build(runtimes []RuntimeInput, controls []domain.Control, counts DecisionCounts) Report {
	out := make([]Runtime, 0, len(runtimes))
	all := make([]Tool, 0, len(runtimes)*4)
	for _, in := range runtimes {
		rc := ForRuntime(in.Connection, in.Observation, controls, counts)
		out = append(out, rc)
		all = append(all, rc.Tools...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Summary.UngovernedHighRisk != out[j].Summary.UngovernedHighRisk {
			return out[i].Summary.UngovernedHighRisk > out[j].Summary.UngovernedHighRisk
		}
		return out[i].Summary.Ungoverned > out[j].Summary.Ungoverned
	})
	return Report{
		Source:        candidate.SourceRuntime,
		Runtimes:      out,
		Summary:       summarize(all),
		DriftMeasured: counts != nil,
	}
}

// RuntimeInput pairs a connection with the observation stored for it.
type RuntimeInput struct {
	Connection  guard.Connection
	Observation guard.Observation
}

// governingControls indexes active controls for an environment by the
// action names they govern. Later-activated controls win, so the map
// holds the control a decision would actually be stamped with.
func governingControls(controls []domain.Control, environment string) map[string]domain.Control {
	env := strings.TrimSpace(environment)
	if env == "" {
		env = domain.DefaultControlEnvironment
	}

	ordered := make([]domain.Control, 0, len(controls))
	for _, c := range controls {
		// domain stores the status as a bare string; "active" is the
		// current row and "superseded" the historical ones.
		if !strings.EqualFold(strings.TrimSpace(c.Status), "active") {
			continue
		}
		ctrlEnv := strings.TrimSpace(c.Environment)
		if ctrlEnv == "" {
			ctrlEnv = domain.DefaultControlEnvironment
		}
		if !strings.EqualFold(ctrlEnv, env) {
			continue
		}
		ordered = append(ordered, c)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ActivatedAt.Before(ordered[j].ActivatedAt)
	})

	out := map[string]domain.Control{}
	for _, c := range ordered {
		for _, action := range c.Actions {
			key := strings.ToUpper(strings.TrimSpace(action))
			if key == "" {
				continue
			}
			out[key] = c
		}
	}
	return out
}

// undecided returns observed minus decisions, floored at zero.
//
// This is a floor, not a measurement. The observed counter is reported
// by the runtime and resets when the process restarts, while decisions
// accumulate in the audit trail, so the difference under-reports
// whenever a runtime has been restarted. A non-zero result means calls
// happened without a decision. A zero result is not evidence that none
// did.
func undecided(observed uint64, decisions int) *uint64 {
	var n uint64
	if decisions < 0 {
		decisions = 0
	}
	if observed > uint64(decisions) {
		n = observed - uint64(decisions)
	}
	return &n
}

func summarize(tools []Tool) Summary {
	s := Summary{Tools: len(tools)}
	var total uint64
	measured := false
	for _, t := range tools {
		if t.Governed {
			s.Governed++
			if domain.NormalizeControlMode(t.Mode) == domain.ControlModeEnforce {
				s.Enforced++
			}
		} else {
			s.Ungoverned++
			if candidate.RiskRank(t.Risk) == candidate.RiskRank("high") {
				s.UngovernedHighRisk++
			}
		}
		if t.Undecided != nil {
			measured = true
			total += *t.Undecided
		}
	}
	if measured {
		s.Undecided = &total
	}
	return s
}
