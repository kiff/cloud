package tenant

import (
	"context"
	"fmt"
	"time"
)

// KeyRevoker is the narrow slice of the API-key store that
// SoftDelete needs: revoke every active key for a tenant so a
// deletion-pending tenant can no longer authenticate. The auth
// package's APIKeyStore satisfies it. Declared here (rather than
// importing auth) so the tenant package stays free of an auth
// dependency and no import cycle forms.
type KeyRevoker interface {
	RevokeAllForTenant(ctx context.Context, tenantID string, at time.Time) error
}

// SoftDeleteResult is the outcome of a successful SoftDelete. The
// timestamps are UTC; callers format them (RFC 3339 on the wire and
// in metadata) at their boundary.
type SoftDeleteResult struct {
	Status          Status
	DeletedAt       time.Time
	GraceWindowEnds time.Time
}

// SoftDelete is the shared primitive that both the self-serve HTTP
// handler and the kiff-tenant
// CLI call to move a tenant from active → deleted_pending.
// Keeping it here means the two erasure entry points cannot drift.
// It:
//
//  1. Revokes all API keys (KeyRevoker.RevokeAllForTenant) so there
//     is no window where the status is deleted_pending but keys
//     still authenticate. Skipped when keys is nil.
//  2. Flips the tenant status to StatusDeletedPending.
//  3. Stamps deleted_at and grace_window_ends in tenant metadata.
//
// The per-tenant schema is retained. The hard scrub (schema drop +
// salt destruction) runs after the grace window via
// Provisioner.Deprovision.
//
// SoftDelete does not itself check the current status — it always
// re-stamps the grace window. Callers that need idempotency (the
// HTTP handler returns the existing timestamps; the CLI errors)
// inspect the tenant's status before calling.
func SoftDelete(ctx context.Context, store Store, keys KeyRevoker, tenantID string) (SoftDeleteResult, error) {
	now := time.Now().UTC()
	graceEnds := now.AddDate(0, 0, GracePeriodDays)

	if keys != nil {
		if err := keys.RevokeAllForTenant(ctx, tenantID, now); err != nil {
			return SoftDeleteResult{}, fmt.Errorf("revoke keys: %w", err)
		}
	}

	if err := store.SetStatus(ctx, tenantID, StatusDeletedPending, nil); err != nil {
		return SoftDeleteResult{}, fmt.Errorf("set deleted_pending: %w", err)
	}
	if err := store.SetMetadata(ctx, tenantID, MetadataKeyDeletedAt, now.Format(time.RFC3339)); err != nil {
		return SoftDeleteResult{}, fmt.Errorf("stamp deleted_at: %w", err)
	}
	if err := store.SetMetadata(ctx, tenantID, MetadataKeyGraceWindowEnds, graceEnds.Format(time.RFC3339)); err != nil {
		return SoftDeleteResult{}, fmt.Errorf("stamp grace_window_ends: %w", err)
	}

	return SoftDeleteResult{
		Status:          StatusDeletedPending,
		DeletedAt:       now,
		GraceWindowEnds: graceEnds,
	}, nil
}
