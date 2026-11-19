// Package operatoralerts is the persisted operator review queue
// It turns the command center's computed risk
// signals into a worked queue: alerts are upserted (deduped by
// tenant+kind), and an operator can acknowledge or resolve them.
//
// This is distinct from internal/alert, which delivers tenant-facing
// webhooks on decisions. operatoralerts is internal: it is what KIFF
// staff triage.
package operatoralerts

import (
	"context"
	"time"
)

// Status of an alert in the review queue.
const (
	StatusOpen     = "open"
	StatusAcked    = "acked"
	StatusResolved = "resolved"
)

// Severity mirrors the risk level that raised the alert.
const (
	SeverityWarn = "warn"
	SeverityHigh = "high"
)

// Alert is one row in the operator review queue. Deduped by
// (tenant_id, kind): re-syncing the same condition bumps Count and
// LastSeenAt rather than creating a new row.
type Alert struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	TenantSlug string    `json:"tenant_slug,omitempty"`
	Kind       string    `json:"kind"`
	Severity   string    `json:"severity"`
	Detail     string    `json:"detail"`
	Status     string    `json:"status"`
	Count      int       `json:"count"`
	FirstSeen  time.Time `json:"first_seen_at"`
	LastSeen   time.Time `json:"last_seen_at"`
	AckedBy    string    `json:"acked_by,omitempty"`
	Note       string    `json:"note,omitempty"`
}

// Upsert is the input to Store.Upsert — one observed risk condition.
type Upsert struct {
	TenantID   string
	TenantSlug string
	Kind       string
	Severity   string
	Detail     string
}

// Store persists and triages operator alerts.
type Store interface {
	// Upsert records an observed condition. If an open/acked alert
	// already exists for (tenant_id, kind), it bumps Count + LastSeen
	// + refreshes Detail/Severity; otherwise it opens a new alert.
	// A resolved alert with the same key re-opens. Returns the alert.
	Upsert(ctx context.Context, u Upsert, now time.Time) (Alert, error)

	// List returns alerts, newest-activity first. When openOnly is
	// true, resolved alerts are excluded. Capped at limit (default
	// 200 when <= 0).
	List(ctx context.Context, openOnly bool, limit int) ([]Alert, error)

	// SetStatus transitions one alert to acked/resolved, stamping the
	// operator and an optional note. Returns ErrNotFound when missing.
	SetStatus(ctx context.Context, id, status, by, note string) (Alert, error)
}
