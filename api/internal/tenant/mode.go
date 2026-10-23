package tenant

import "strings"

// Mode is the governance posture a tenant runs the proposal gate
// under. It is server-controlled — like Plan, a tenant cannot
// self-assert their mode via any HTTP surface; cmd/kiff-tenant
// (operator CLI) writes it, the cloud reads it.
//
// Mode is the "observe → enforce" on-ramp the sales motion
// describes. It does not change the decision logic:
// the gate evaluates the same contract and returns the same
// outcome in either mode. What changes is the contract with the
// caller —
//
//   - ModeEnforce (default): the outcome is binding. The caller is
//     expected to gate execution on it (allowed runs; the rest
//     withhold).
//   - ModeObserve: the outcome is advisory. The caller runs their
//     existing flow unchanged; KIFF records what it *would* have
//     decided so the tenant can see what enforcing would have
//     blocked or held before they trust the gate.
//
// Default is enforce: an unknown or missing mode label must never
// silently turn a tenant's gate into observe-only, which would be
// a fail-open governance posture. A typo on the ops side collapses
// to enforce (stricter), the same safe-by-default direction Plan
// takes (a typo collapses to free_dev, the tighter limit).
type Mode string

const (
	// ModeEnforce is the default. The decision is binding; the
	// caller gates execution on it.
	ModeEnforce Mode = "enforce"

	// ModeObserve makes the decision advisory. The caller does not
	// gate on it; KIFF records the would-be outcome so a tenant can
	// evaluate the gate against real traffic before enforcing.
	ModeObserve Mode = "observe"

	// LegacyModeShadow is the pre-#773 spelling of ModeObserve. The
	// guard runtime, the guard SDK, and the public docs all say
	// "observe"; only this field and tenant_controls.mode ever said
	// "shadow". Parsing still accepts it so tenant metadata written
	// before the rename keeps its advisory posture instead of
	// silently collapsing to enforce. Never written — canonical
	// output is always ModeObserve.
	LegacyModeShadow Mode = "shadow"
)

// ParseMode maps an operator-supplied posture label onto the
// canonical vocabulary, reporting whether it was recognized. Unlike
// ModeFor — which reads stored metadata and must fail closed on
// garbage — this is for input validation, where an unrecognized
// label should be rejected with an error rather than silently
// treated as enforce.
//
// The legacy "shadow" spelling is accepted and normalized to
// ModeObserve so operator scripts and runbooks written before the
// rename keep working.
func ParseMode(s string) (Mode, bool) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeObserve, LegacyModeShadow:
		return ModeObserve, true
	case ModeEnforce:
		return ModeEnforce, true
	default:
		return "", false
	}
}

// MetadataKeyMode is the well-known key under which the mode label
// lives on Tenant.Metadata. A constant so the handler, the CLI,
// and any dashboard surface agree on the name.
const MetadataKeyMode = "mode"

// ModeFor returns the effective Mode for a tenant. Reads
// Tenant.Metadata[MetadataKeyMode] (server-controlled), defaulting
// to ModeEnforce when the field is absent or unrecognized so the
// gate is never accidentally observe-only.
func ModeFor(t Tenant) Mode {
	return modeFromMetadata(t.Metadata)
}

// modeFromMetadata extracts the mode identifier from a metadata
// map. Tolerates string values and missing keys; any unknown value
// collapses to enforce so a typo cannot silently disable gating.
func modeFromMetadata(meta map[string]any) Mode {
	if meta == nil {
		return ModeEnforce
	}
	raw, ok := meta[MetadataKeyMode]
	if !ok {
		return ModeEnforce
	}
	s, ok := raw.(string)
	if !ok {
		return ModeEnforce
	}
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeObserve, LegacyModeShadow:
		return ModeObserve
	default:
		// enforce, unknown, or empty: enforce (fail-closed).
		return ModeEnforce
	}
}
