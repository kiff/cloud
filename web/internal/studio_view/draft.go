package studio_view

import (
	"strconv"
	"time"

	studio "github.com/kiff/kiff-studio"
)

// StudioDraft is what the templ Studio page renders. The
// Blueprint is the canonical authoring shape (the value the api's
// draft store persists between sessions); UI is the per-render
// scaffolding the page needs that doesn't belong on the package
// type.
//
// One StudioDraft per render. Constructed by the handler from:
//
//   - the api's GET /v1/me/domain/draft response (yaml + parsed
//     summary or issues), or
//   - the api's GET /v1/me/domain response (loaded as a fresh
//     draft when no draft exists but an active domain does), or
//   - a placeholder default (when neither exists — first visit).
type StudioDraft struct {
	// Blueprint is the canonical authoring shape. Never nil-zero
	// after the handler constructs the draft (the placeholder
	// default has Entity + one Action populated).
	Blueprint studio.Blueprint

	// Adapters is the sidecar adapter list — Blueprint.Adapters
	// doesn't exist in kiff-studio v0.1.0, so the Studio surface
	// tracks adapters here and the YAML rendering composes them
	// at activate time. v0.2 of the package may absorb this back
	// into Blueprint; until then, the wrapper carries it.
	Adapters []AdapterConfig

	// Audit is the sidecar audit configuration. Same posture as
	// Adapters — kiff-studio v0.1.0 doesn't carry it; the Studio
	// surface tracks it here.
	Audit AuditConfig

	// UpdatedAt is the unix-second timestamp of the last
	// auto-save. Drives the "auto-saved · 14s ago" indicator.
	// Zero when the draft has never been saved (first render
	// of the placeholder default).
	UpdatedAt int64

	// Issues are the validation messages the api side returned
	// from its best-effort ParseAndBuild on the last PUT. Empty
	// slice on a clean Blueprint. Parsed once at handler time
	// into FieldErrors for the templ partials.
	Issues []FieldError

	// FieldErrors is Issues indexed by (BlockID, Field). Cheap
	// lookup for the templ partials when rendering inline error
	// rows.
	FieldErrors map[string]map[string]string

	// OpenBlocks remembers which blocks were expanded on the
	// last render. Persisted alongside the Blueprint so a reload
	// keeps the user's open state. Map keyed on block id.
	OpenBlocks map[string]bool
}

// AdapterConfig carries the sidecar adapter rows. Each entry
// renders one card under the "audit & adapter" prim-label
// section in stage 4.
type AdapterConfig struct {
	Name   string
	Source string
	Kind   string // "webhook" (v1 only); "polling" reserved for M2.
}

// AuditConfig is the sidecar audit shape. Defaults match the
// framework's in-memory store + trace propagation on. These are
// the values the placeholder draft uses; tenants override at
// stage 4.
type AuditConfig struct {
	Store            string // "in-memory" / "file" / "postgres" (custom)
	TracePropagation bool
}

// Stage returns the StudioDraft's effective stage. Combines the
// Blueprint-derived stage from StageOf with the sidecar adapter
// list — a Blueprint that meets StageRules + has ≥1 adapter is
// StageFull.
func (d StudioDraft) Stage() Stage {
	bpStage := StageOf(d.Blueprint)
	if bpStage == StageRules && len(d.Adapters) > 0 {
		return StageFull
	}
	return bpStage
}

// HasAdapters reports whether the draft has any sidecar adapter
// entries. Used by the templ partials to decide whether to
// render the "+ add adapter" affordance vs the existing list.
func (d StudioDraft) HasAdapters() bool {
	return len(d.Adapters) > 0
}

// TrustBoundary returns the policy-block status. Cheap pass-through
// to AnalyzeTrustBoundary so the templ partials can call it
// directly on the draft without importing the sentinel.
func (d StudioDraft) TrustBoundary() TrustBoundary {
	return AnalyzeTrustBoundary(d.Blueprint)
}

// AutoSaveLabel returns the human-readable auto-save indicator
// the studio bar renders. Branches on UpdatedAt:
//
//   - zero (never saved): "ready"
//   - within 5 seconds: "auto-saved · just now"
//   - older: "auto-saved · <relative>"
//
// Pure function; takes the current time as a parameter so
// tests can pin the clock.
func (d StudioDraft) AutoSaveLabel(now time.Time) string {
	if d.UpdatedAt == 0 {
		return "ready"
	}
	delta := now.Unix() - d.UpdatedAt
	if delta < 0 {
		// Clock skew between the api and the web binary; render
		// "just now" rather than a negative figure.
		return "auto-saved · just now"
	}
	if delta < 5 {
		return "auto-saved · just now"
	}
	return "auto-saved · " + humanizeAge(delta)
}

// ScaffoldEnabled reports whether the studio bar's scaffold tab
// should be active. True at StageFull only — the artifact is
// only complete once audit + adapter join the Blueprint.
func (d StudioDraft) ScaffoldEnabled() bool {
	return d.Stage() == StageFull
}

// ActivateEnabled reports whether the studio bar's
// "Save and activate" button is enabled. True at StageShape and
// above — a Blueprint with no states is invalid against
// domain.ParseAndBuild and would fail the activate request.
func (d StudioDraft) ActivateEnabled() bool {
	return d.Stage() >= StageShape
}

// IndexErrors populates FieldErrors from Issues. Called by the
// handler after the api response carries the per-PUT validation
// payload.
func (d *StudioDraft) IndexErrors() {
	d.FieldErrors = make(map[string]map[string]string, len(d.Issues))
	for _, ie := range d.Issues {
		bucket, ok := d.FieldErrors[ie.BlockID]
		if !ok {
			bucket = make(map[string]string)
			d.FieldErrors[ie.BlockID] = bucket
		}
		bucket[ie.Field] = ie.Message
	}
}

// FieldError returns the error message for one block's field, or
// "" when none. Templ partials call this directly to decide
// whether to render the red treatment on a row.
func (d StudioDraft) FieldError(blockID, field string) string {
	if d.FieldErrors == nil {
		return ""
	}
	if bucket, ok := d.FieldErrors[blockID]; ok {
		return bucket[field]
	}
	return ""
}

// IsBlockOpen reports whether a block should render expanded.
func (d StudioDraft) IsBlockOpen(blockID string) bool {
	if d.OpenBlocks == nil {
		return false
	}
	return d.OpenBlocks[blockID]
}

// YAMLPreview returns the rendered YAML for the studio's middle
// pane. Calls studio.BuildYAML when the Blueprint validates;
// falls back to a best-effort hand-rolled rendering when it
// doesn't (so the pane never goes blank during mid-edit).
//
// The fallback uses the same shape studio.BuildYAML emits but
// skips the package's strict Validate gate. Drafts mid-edit
// (e.g. an action with no allowed_states yet) would otherwise
// have an empty middle pane, which feels broken to the user
// even though the auto-save is working correctly.
//
// Approval-required actions get a reviewer permission here, so what
// Studio hands the codebase can be reviewed by more than the owner's
// session.
func (d StudioDraft) YAMLPreview() string {
	b := WithReviewerPermissions(d.Blueprint)
	if yaml, err := studio.BuildYAML(b); err == nil {
		return string(yaml)
	}
	return renderPartialYAML(b)
}

// humanizeAge turns a non-negative seconds count into a short
// relative string ("14s ago", "4m ago", "2h ago", "3d ago").
// Stops at days; older values are deliberately a coarse signal.
func humanizeAge(seconds int64) string {
	switch {
	case seconds < 60:
		return strconv.FormatInt(seconds, 10) + "s ago"
	case seconds < 3600:
		return strconv.FormatInt(seconds/60, 10) + "m ago"
	case seconds < 86400:
		return strconv.FormatInt(seconds/3600, 10) + "h ago"
	default:
		return strconv.FormatInt(seconds/86400, 10) + "d ago"
	}
}
