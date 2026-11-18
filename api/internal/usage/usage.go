// Package usage implements the cloud-side runtime.MetricsRecorder.
// The framework's runtime fires Inc calls on six
// well-defined operational events; the Aggregator buckets them
// in-memory by (tenant_id, counter_name, entity_type, hour) and
// flushes to cloud_meta.tenant_usage periodically.
//
// What this package is not:
//
//   - A billing engine. It counts; billing reads.
//   - A tenant-facing API. Ops queries via kiff-tenant usage.
//   - An anchoring loop, in production form, reads
//     hourly aggregates from the same table.
package usage

import (
	"context"
	"errors"
	"time"
)

// Counter is one row in cloud_meta.tenant_usage. Tests and the
// flush helpers traffic in this shape; the table's primary key
// matches the (TenantID, CounterName, EntityType, DomainID,
// HourBucket) tuple.
type Counter struct {
	TenantID    string
	CounterName string
	EntityType  string
	// DomainID attributes the counter to the operational domain it
	// was emitted under. Empty for tenant-scoped
	// activity with no domain (and for rows written before the
	// dimension existed). Replaces an earlier trick of overloading
	// EntityType with the domain name.
	DomainID   string
	HourBucket time.Time
	Count      uint64
}

// Filter narrows a Snapshot read.
type Filter struct {
	TenantID    string
	CounterName string
	DomainID    string
	HourFrom    time.Time
	HourTo      time.Time
}

// Store is the persistence boundary for usage counters.
//
// Implementations:
//   - PostgresStore (production) writes to cloud_meta.tenant_usage.
//   - MemoryStore (tests) holds rows in memory.
type Store interface {
	// Flush persists the given counters. Idempotent: re-flushing the
	// same counter increments the row by Count. Used by Aggregator
	// to drain its in-memory state.
	Flush(ctx context.Context, counters []Counter) error

	// Snapshot returns counters matching the filter. Empty filter
	// returns every row (used by ops tooling).
	Snapshot(ctx context.Context, filter Filter) ([]Counter, error)
}

// HourBucket truncates t to the start of its UTC hour. Stable across
// processes and across machines so flushes from different replicas
// land in the same row.
func HourBucket(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
}

// ErrNotConfigured is returned by helpers when a required dependency
// is missing. Most production wiring panics on misconfiguration; this
// error exists for tests that want to assert the surface explicitly.
var ErrNotConfigured = errors.New("usage: not configured")
