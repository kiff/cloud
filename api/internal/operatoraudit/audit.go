// Package operatoraudit is the append-only record of mutating
// operator actions on the control plane.
//
// Every /v1/ops/* mutation (suspend, unsuspend, plan change, alert
// ack/resolve) writes exactly one Entry here, in the same transaction
// as the change where possible. The table is append-only: the
// application role has INSERT + SELECT, never UPDATE or DELETE, so the
// trail is tamper-evident enough for internal accountability.
//
// This is distinct from the per-tenant kiff_audit trail,
// which records governed-action decisions inside a tenant's schema.
// operator_audit records what KIFF STAFF did across tenants.
package operatoraudit

import (
	"context"
	"time"
)

// Action names the mutating operator actions. Kept as constants so
// the console and the audit view agree on the vocabulary.
const (
	ActionSuspend    = "tenant.suspend"
	ActionUnsuspend  = "tenant.unsuspend"
	ActionRestore    = "tenant.restore"
	ActionPlanChange = "tenant.plan_change"
	// ActionStoreBillingReset puts a review or test account back to no plan
	// after a store purchase (Reset store billing): plan trial, no billing
	// source, the App Store's and Google Play's subscriptions cleared.
	ActionStoreBillingReset = "tenant.store_billing_reset"
	// ActionBonusTasks is the old bonus of tafs tasks, kept so past
	// entries read; new bonuses are credits.
	ActionBonusTasks   = "tenant.bonus_tasks"
	ActionBonusCredits = "tenant.bonus_credits"
	// ActionLabel sets the operators' own label on an account (internal,
	// eval, e2e, demo, or none). It changes nothing the account sees or
	// pays.
	ActionLabel        = "tenant.label"
	ActionAlertAck     = "alert.ack"
	ActionAlertResolve = "alert.resolve"
	// The integration proofs' overrides of one app's write hold (proofs):
	// hold it whatever its state, release a red one (with the owner's
	// yes), or end the override in force.
	ActionProofHold    = "proof.hold"
	ActionProofRelease = "proof.release"
	ActionProofClear   = "proof.clear"
)

// Entry is one operator action. Immutable once written.
type Entry struct {
	ID              string         `json:"id"`
	OperatorSubject string         `json:"operator_subject"`
	Action          string         `json:"action"`
	TargetTenantID  string         `json:"target_tenant_id,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

// Store persists and reads operator audit entries. Append-only at the
// call boundary: there is no Update or Delete.
type Store interface {
	// Record appends one entry. The store stamps ID and CreatedAt
	// when they are zero.
	Record(ctx context.Context, e Entry) error

	// List returns the most recent entries, newest first, capped at
	// limit (implementations default to 100 when limit <= 0).
	// targetTenantID filters to one tenant when non-empty.
	List(ctx context.Context, targetTenantID string, limit int) ([]Entry, error)
}
