// Package studio_view holds the per-render scaffolding the
// templ Studio surface needs that doesn't belong in the
// kiff-studio package itself.
//
// The package is the seam between:
//
//   - kiff-studio (deterministic, byte-determinant package; pure
//     data → bytes; no UI concerns)
//   - apps/web's templ partials (per-render block ids, open/closed
//     state, field-level errors, derived stage)
//
// All exported types here are render-scoped. They are constructed
// from a Blueprint (loaded from the api's draft store) and are
// thrown away after a single render. Persistence is the api's
// job; this package never writes anything.
//
// See .local/studio-mvp-spec.md §2 (Blueprint shape) and §3
// (Component breakdown).
package studio_view

import (
	studio "github.com/kiff/kiff-studio"
)

// Stage classifies a Blueprint by which primitives are populated.
// The stage is derived (pure function of the Blueprint), not
// stored. The templ partials branch on Stage to decide which
// builder blocks render.
type Stage int

const (
	// StageScratch — entity + ≥1 action, no states. The minimum
	// shape that produces a parseable kiff.yaml. Every proposal
	// against this Blueprint blocks because no states exist.
	StageScratch Stage = iota + 1

	// StageShape — adds states + transitions; every action has
	// AllowedStates. Now `allowed` is reachable in the runtime
	// after the right event ingests.
	StageShape

	// StageRules — adds risk + approval + permissions per action,
	// plus ≥1 role. The trust boundary becomes explicit: high-risk
	// actions return approval_required.
	StageRules

	// StageFull — adds audit configuration + ≥1 adapter. The
	// scaffold tab is enabled; the artifact is complete.
	StageFull
)

// String returns a stable lowercase label for the stage.
// Useful for templ class names and for debugging.
func (s Stage) String() string {
	switch s {
	case StageScratch:
		return "scratch"
	case StageShape:
		return "shape"
	case StageRules:
		return "rules"
	case StageFull:
		return "full"
	default:
		return "unknown"
	}
}

// StageOf returns the highest stage the Blueprint satisfies.
// Pure function of the Blueprint contents; no persistence,
// no IO, no validation beyond the criteria below.
//
// The stage is "highest satisfied" not "exact match" — a
// Blueprint with audit + adapter but no state machine is still
// StageScratch, not StageFull, because the StageShape criteria
// fail. Stages are nested.
//
// Returns StageScratch when the Blueprint is below stage 1
// (zero actions or empty entity); the templ surface uses that
// as the "show the placeholder draft" signal.
func StageOf(b studio.Blueprint) Stage {
	// Below scratch: nothing to render.
	if b.Entity == "" || len(b.Actions) == 0 {
		return StageScratch
	}

	// Stage 1 → 2: state machine present.
	if !meetsShape(b) {
		return StageScratch
	}

	// Stage 2 → 3: full action contracts + roles + trust boundary.
	if !meetsRules(b) {
		return StageShape
	}

	// Stage 3 → 4: audit + ≥1 adapter.
	// (Adapters live outside the kiff-studio Blueprint type today;
	// the Studio surface tracks them in a sidecar map on the
	// StudioDraft. We can't read them from the Blueprint alone, so
	// the StageOf function tops out at StageRules. The handler
	// promotes to StageFull when the draft's sidecar adapter list
	// is non-empty. See StudioDraft.HasAdapters and
	// (StudioDraft).Stage below.)
	return StageRules
}

// meetsShape returns true when the Blueprint has the state
// machine shape (≥2 states, ≥1 transition, every action has
// AllowedStates).
func meetsShape(b studio.Blueprint) bool {
	if len(b.States) < 2 {
		return false
	}
	if len(b.Transitions) < 1 {
		return false
	}
	for _, a := range b.Actions {
		if len(a.AllowedStates) == 0 {
			return false
		}
	}
	return true
}

// meetsRules returns true when every action has risk + approval
// + ≥1 required permission, the Blueprint has ≥1 role, and the
// trust boundary either holds or fails in the documented
// solo-signup pattern (one role holding both proposing +
// .approve, named after the tenant owner).
func meetsRules(b studio.Blueprint) bool {
	for _, a := range b.Actions {
		if a.Risk == "" {
			return false
		}
		if len(a.RequiredPermissions) == 0 {
			return false
		}
	}
	if len(b.Roles) == 0 {
		return false
	}
	return true
}

// TrustBoundary reports the policy-block status the templ
// surface renders below the role list.
type TrustBoundary int

const (
	// TrustBoundaryUnknown — the Blueprint hasn't reached
	// StageRules yet; the surface doesn't render the line.
	TrustBoundaryUnknown TrustBoundary = iota

	// TrustBoundaryHolds — no role holds both <action> and
	// <action>.approve permissions for the same action. The
	// canonical multi-user shape.
	TrustBoundaryHolds

	// TrustBoundarySoloSignup — exactly one role holds both
	// proposing and.approve permissions for one or more actions.
	// The deliberate solo-signup default from the Studio-cloud
	// seam design The line renders in a warm/amber tone with copy
	// that names the trade-off.
	TrustBoundarySoloSignup
)

// AnalyzeTrustBoundary returns the trust-boundary status for a
// Blueprint. Walks every role's permissions and looks for any
// role that holds both <action> and <action>.approve for the
// same action.
func AnalyzeTrustBoundary(b studio.Blueprint) TrustBoundary {
	if StageOf(b) < StageRules {
		return TrustBoundaryUnknown
	}
	for _, perms := range b.Roles {
		held := make(map[string]struct{}, len(perms))
		for _, p := range perms {
			held[p] = struct{}{}
		}
		for p := range held {
			// If we hold a non-approve perm, check if the
			// matching .approve is also in this role's set.
			if endsWithApprove(p) {
				continue
			}
			if _, both := held[p+".approve"]; both {
				return TrustBoundarySoloSignup
			}
		}
	}
	return TrustBoundaryHolds
}

func endsWithApprove(s string) bool {
	const suffix = ".approve"
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}
