package domain

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Control is the product unit: one domain activation by one
// tenant. Created automatically when a domain is activated via
// Studio or PUT /v1/me/domain. The previous active control for
// the same (tenant, domain, environment) is set to "superseded"
// atomically (domain-scoped: a multi-domain tenant keeps one
// active control per domain).
//
// Status values: "active" (current) or "superseded" (historical).
//
// Environment and Workflow give a control an addressable identity
// beyond (tenant, action) so the same action can be governed
// distinctly across, e.g., staging vs prod or two workflows
// They default to the guard connection defaults
// ("dev" / "default") so single-control tenants are unchanged.
// Populating them from a live guard connection identity, and
// referencing the control from the decide path, is the deferred
// association, which builds on this key.
type Control struct {
	ID          string
	TenantID    string
	Domain      string
	Entity      string
	Actions     []string // action names from the activated domain
	Environment string   // e.g. "dev", "staging", "prod"; default "dev"
	Workflow    string   // logical workflow/agent grouping; default "default"
	// Version is a monotonic version for the (tenant, environment)
	// control lineage, assigned at Create: the first activation in an
	// environment is version 1, each re-activation increments it.
	// Decisions stamp the version in force at decide time so
	// the evidence object can state "governed by control version N";
	// historical decisions keep their original version.
	Version     int
	ActivatedAt time.Time
	ActivatedBy string // subject id of the activating user
	Status      string // "active" or "superseded"
	// Mode is the per-domain governance posture: "observe" (decisions
	// are advisory — recorded, not enforced) or "enforce" (blocks/holds
	// take effect). Empty means "not set on this control"; the decide
	// path falls back to the tenant-wide mode so pre-per-domain controls
	// keep their behavior. New activations default to observe (watch
	// real traffic first, flip to enforce deliberately). Rows written
	// before "observe" replaced "shadow" hold the legacy spelling; read
	// them through NormalizeControlMode.
	Mode string
}

// Control identity defaults. They mirror the guard connection
// defaults (guard.DefaultEnvironment / guard.DefaultWorkflow) so a
// control created without an explicit environment/workflow resolves
// the same way a default guard connection does. Duplicated as
// constants here to avoid a domain→guard import.
const (
	DefaultControlEnvironment = "dev"
	DefaultControlWorkflow    = "default"
)

// WithIdentityDefaults returns a copy of the control with empty
// Environment/Workflow filled with the package defaults. Activation
// paths call this so every persisted control has a non-empty,
// addressable identity.
func (c Control) WithIdentityDefaults() Control {
	if c.Environment == "" {
		c.Environment = DefaultControlEnvironment
	}
	if c.Workflow == "" {
		c.Workflow = DefaultControlWorkflow
	}
	return c
}

// ControlStore persists and retrieves control records.
//
// Implementations:
//   - PostgresControlStore (production)
//   - MemoryControlStore (tests)
type ControlStore interface {
	// Create inserts a new control record and atomically sets any
	// previous active control for the same (tenant, domain,
	// environment) to "superseded" (domain-scoped so a
	// multi-domain tenant keeps one active control per domain).
	// The new control always has status "active".
	Create(ctx context.Context, c Control) error

	// List returns all controls for a tenant ordered by
	// ActivatedAt descending (newest first).
	List(ctx context.Context, tenantID string) ([]Control, error)

	// SetMode updates the governance posture ("observe" | "enforce") of
	// one active control, addressed by (tenantID, controlID). Returns
	// ErrControlNotFound when no such control belongs to the tenant.
	// This is the per-domain observe↔enforce flip. Callers pass a
	// value already through NormalizeControlMode.
	SetMode(ctx context.Context, tenantID, controlID, mode string) error
}

// Governance posture values for Control.Mode. They mirror
// tenant.Mode's string values so the decide path can compare without
// importing the tenant package into domain.
const (
	ControlModeObserve = "observe"
	ControlModeEnforce = "enforce"

	// LegacyControlModeShadow is the pre-#773 spelling of
	// ControlModeObserve. Still accepted on read so controls stored
	// before the rename keep their advisory posture; never written.
	LegacyControlModeShadow = "shadow"
)

// NormalizeControlMode maps a stored or caller-supplied posture onto
// the canonical vocabulary. It exists because tenant_controls.mode
// holds "shadow" for every control activated before the rename, and
// because the HTTP surface accepted "shadow" from clients. Both must keep
// resolving to observe rather than falling through to enforce, which
// would turn an advisory control binding without anyone asking.
//
// An empty mode stays empty: it means "not set on this control", and
// the decide path reads that as "fall back to the tenant-wide mode".
// Collapsing it to observe here would override the tenant posture.
// Any other unrecognized value collapses to enforce, matching
// tenant.ModeFor's fail-closed direction.
func NormalizeControlMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return ""
	case ControlModeObserve, LegacyControlModeShadow:
		return ControlModeObserve
	default:
		return ControlModeEnforce
	}
}

// FindActiveControl selects the active control that governs an
// action for a tenant, addressing by identity rather than blind
// first-active-match. When environment is non-empty, only a
// control whose Environment matches is returned; an empty
// environment matches the single active control (back-compat for
// single-control tenants). Returns ErrControlNotFound when no active
// control declares the action under the requested environment.
//
// It is a helper over ControlStore.List so every store implementation
// gets identity-addressed selection without a new interface method.
func FindActiveControl(ctx context.Context, store ControlStore, tenantID, action, environment string) (Control, error) {
	list, err := store.List(ctx, tenantID)
	if err != nil {
		return Control{}, err
	}
	for _, c := range list {
		if c.Status != "active" {
			continue
		}
		if !containsAction(c.Actions, action) {
			continue
		}
		if environment != "" && c.Environment != environment {
			continue
		}
		return c, nil
	}
	return Control{}, ErrControlNotFound
}

func containsAction(actions []string, want string) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

// FindControlByID returns the control with the given id for a tenant,
// or ErrControlNotFound when no such control belongs to the tenant.
// Tenant-scoped over ControlStore.List, so a control_id from another
// tenant is mechanically unreachable (it isn't in this tenant's list)
// — the endpoint relies on this to 404 cross-tenant ids.
func FindControlByID(ctx context.Context, store ControlStore, tenantID, controlID string) (Control, error) {
	list, err := store.List(ctx, tenantID)
	if err != nil {
		return Control{}, err
	}
	for _, c := range list {
		if c.ID == controlID {
			return c, nil
		}
	}
	return Control{}, ErrControlNotFound
}

// ErrControlNotFound is returned when a control lookup produces
// no result.
var ErrControlNotFound = errors.New("control: not found")
