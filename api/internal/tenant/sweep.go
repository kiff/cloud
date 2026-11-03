package tenant

import (
	"context"
	"fmt"
	"time"
)

// Deprovisioner scrubs a tenant: drops its schema, nulls its salt and marks
// it StatusDeleted (Provisioner.Deprovision).
type Deprovisioner interface {
	Deprovision(ctx context.Context, tenantID string) error
}

// SweepEntry is what a sweep did with one deleted_pending tenant.
type SweepEntry struct {
	TenantID        string `json:"tenant_id"`
	Slug            string `json:"slug"`
	DeletedAt       string `json:"deleted_at,omitempty"`
	GraceWindowEnds string `json:"grace_window_ends,omitempty"`
	// Outcome is one of: scrubbed, dry-run, skipped-future,
	// skipped-no-window, error.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// SweepSummary is a sweep's result.
type SweepSummary struct {
	Candidates int          `json:"candidates"`
	Scrubbed   int          `json:"scrubbed"`
	Skipped    int          `json:"skipped"`
	Failed     int          `json:"failed"`
	DryRun     bool         `json:"dry_run"`
	Entries    []SweepEntry `json:"entries"`
}

// SweepExpired scrubs every deleted_pending tenant whose grace window ended
// before now: the erasure the privacy policy promises 30 days after
// an account is deleted. A tenant still inside its window, or with no
// parseable window, is left untouched: never scrub on a guess. With dryRun
// nothing is scrubbed. A scrubbed tenant is stamped MetadataKeyErasedAt, a
// record that outlives the logs. audit, when not nil, is called for each
// tenant that was scrubbed, failed or had no window.
// A failed scrub is counted and the sweep goes on; it is tried again on the
// next sweep, since the tenant is still deleted_pending.
func SweepExpired(ctx context.Context, store Store, prov Deprovisioner, now time.Time, dryRun bool, audit func(SweepEntry)) (SweepSummary, error) {
	pending, err := store.List(ctx, ListFilter{Status: StatusDeletedPending})
	if err != nil {
		return SweepSummary{}, fmt.Errorf("list deleted_pending tenants: %w", err)
	}
	summary := SweepSummary{Candidates: len(pending), DryRun: dryRun}
	note := func(e SweepEntry) {
		if audit != nil {
			audit(e)
		}
	}
	for _, t := range pending {
		deletedAt, _ := t.Metadata[MetadataKeyDeletedAt].(string)
		graceStr, _ := t.Metadata[MetadataKeyGraceWindowEnds].(string)
		e := SweepEntry{TenantID: t.ID, Slug: t.Slug, DeletedAt: deletedAt, GraceWindowEnds: graceStr}

		graceEnds, perr := time.Parse(time.RFC3339, graceStr)
		switch {
		case graceStr == "" || perr != nil:
			// A deleted_pending row with no parseable window is a
			// data anomaly — never scrub on a guess. Skip loudly.
			e.Outcome = "skipped-no-window"
			summary.Skipped++
			note(e)
		case graceEnds.After(now):
			e.Outcome = "skipped-future"
			summary.Skipped++
		case dryRun:
			e.Outcome = "dry-run"
		default:
			if err := prov.Deprovision(ctx, t.ID); err != nil {
				e.Outcome = "error"
				e.Error = err.Error()
				summary.Failed++
			} else {
				e.Outcome = "scrubbed"
				summary.Scrubbed++
				// Erased whatever the stamp does: a failed stamp is said, not undone.
				if err := store.SetMetadata(ctx, t.ID, MetadataKeyErasedAt, now.UTC().Format(time.RFC3339)); err != nil {
					e.Error = "erased; its erased_at stamp failed: " + err.Error()
				}
			}
			note(e)
		}
		summary.Entries = append(summary.Entries, e)
	}
	return summary, nil
}
