package tenant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TenantProvisioner is the subset of *Provisioner that
// EnsureForSubject needs. Pulled into an interface so unit tests
// can stub the schema-creation side without standing up Postgres.
type TenantProvisioner interface {
	Provision(ctx context.Context, tenantID string) error
	Deprovision(ctx context.Context, tenantID string) error
}

// EnsureForSubject is the idempotent provisioning entrypoint for
// the per-user-tenant flow. Called from the auth resolver on the
// first authenticated request from a given subject. The contract:
//
//   - If the subject is already mapped (cloud_meta.subject_tenants
//     row exists), return the existing tenant.
//   - Otherwise, mint a fresh tenant (id, slug, schema), provision
//     it, record the subject → tenant mapping, and return.
//
// Concurrency: two requests from the same subject racing on first
// sign-in will both attempt to provision. The SubjectStore's
// INSERT ... ON CONFLICT DO NOTHING resolves the race; the loser
// re-reads, sees the winner's tenant, and returns it. The
// "orphan" tenant (the loser's freshly-provisioned but unmapped
// schema) is detected and deprovisioned to avoid leaking schemas.
//
// The slug is derived deterministically from the subject so the
// retry path (reading after a conflict) doesn't generate a new
// slug each time. This also gives operators a hint about who
// owns the tenant when scanning rows.
func EnsureForSubject(
	ctx context.Context,
	store Store,
	prov TenantProvisioner,
	subjects SubjectStore,
	subject string,
) (Tenant, error) {
	if subject == "" {
		return Tenant{}, errors.New("tenant: subject is required")
	}
	if store == nil || prov == nil || subjects == nil {
		return Tenant{}, errors.New("tenant: EnsureForSubject requires store, provisioner, and subject store")
	}

	// Fast path: subject is already mapped.
	mapping, err := subjects.Get(ctx, subject)
	if err == nil {
		t, gerr := store.Get(ctx, mapping.TenantID)
		if gerr != nil {
			return Tenant{}, fmt.Errorf("subject %q mapped to %q but tenant lookup failed: %w", subject, mapping.TenantID, gerr)
		}
		return t, nil
	}
	if !errors.Is(err, ErrSubjectNotMapped) {
		return Tenant{}, fmt.Errorf("look up subject mapping: %w", err)
	}

	// Slow path: provision. The slug is derived deterministically
	// from the subject. Concurrent first-sign-ins for the same
	// subject can race here — both goroutines see "not mapped"
	// and both reach store.Create with the same slug. Whoever
	// loses the unique-slug constraint retries: re-reads the
	// subject store (the winner may have raced ahead and recorded
	// the mapping) and otherwise widens the slug suffix.
	tenantID := NewID()
	slug, err := slugForSubject(ctx, store, subject)
	if err != nil {
		return Tenant{}, err
	}
	t := Tenant{
		ID:          tenantID,
		Slug:        slug,
		DisplayName: defaultDisplayName(subject),
		Status:      StatusProvisioning,
	}
	if err := store.Create(ctx, t); err != nil {
		if errors.Is(err, ErrDuplicateSlug) {
			// Another goroutine reached Create first with the
			// same deterministic slug. Re-read the subject
			// store; the winner has likely also recorded the
			// mapping by now. If it has, return their tenant.
			// If not, retry with a fresh ULID + widened slug.
			if mapping, gerr := subjects.Get(ctx, subject); gerr == nil {
				winner, gerr2 := store.Get(ctx, mapping.TenantID)
				if gerr2 == nil {
					return winner, nil
				}
			}
			// Winner hasn't recorded the mapping yet (rare
			// timing window). Retry with a wider slug suffix.
			slug, err = wideSlugForSubject(ctx, store, subject, slug)
			if err != nil {
				return Tenant{}, err
			}
			t.Slug = slug
			if err := store.Create(ctx, t); err != nil {
				return Tenant{}, fmt.Errorf("create tenant after slug widen: %w", err)
			}
		} else {
			return Tenant{}, fmt.Errorf("create tenant: %w", err)
		}
	}
	if err := prov.Provision(ctx, tenantID); err != nil {
		// Best-effort cleanup: drop the schema + mark deleted so
		// a half-provisioned tenant does not stick around. If
		// cleanup itself fails, the original error is still the
		// one that matters; log-and-return.
		_ = prov.Deprovision(ctx, tenantID)
		return Tenant{}, fmt.Errorf("provision tenant: %w", err)
	}
	now := time.Now().UTC()
	if err := subjects.Set(ctx, subject, tenantID, now); err != nil {
		if errors.Is(err, ErrSubjectAlreadyMapped) {
			// Race: another goroutine won. Tear down our
			// freshly-provisioned tenant and return the winner's.
			_ = prov.Deprovision(ctx, tenantID)
			winnerMapping, gerr := subjects.Get(ctx, subject)
			if gerr != nil {
				return Tenant{}, fmt.Errorf("conflict resolution: re-read mapping: %w", gerr)
			}
			winner, gerr := store.Get(ctx, winnerMapping.TenantID)
			if gerr != nil {
				return Tenant{}, fmt.Errorf("conflict resolution: read winner tenant: %w", gerr)
			}
			return winner, nil
		}
		_ = prov.Deprovision(ctx, tenantID)
		return Tenant{}, fmt.Errorf("record subject mapping: %w", err)
	}

	// Re-read so the returned tenant has the post-provision status
	// (StatusActive + provisioned_at populated by the Provisioner).
	final, err := store.Get(ctx, tenantID)
	if err != nil {
		return Tenant{}, fmt.Errorf("re-read tenant after provision: %w", err)
	}
	return final, nil
}

// slugForSubject derives a stable, human-readable slug from the
// subject. The strategy:
//
//  1. Take a sha-256 of the subject and use the first 10 hex
//     characters as a uniqueness suffix.
//  2. Prefix with "user-" so ops queries can distinguish
//     auto-provisioned tenants from operator-created ones.
//  3. If by some unlikely chance the slug already exists in the
//     tenant store, append additional hex characters until it is
//     unique.
//
// The hash-of-subject design means the same subject always gets
// the same slug — useful when the post-provision mapping insert
// loses a race and we have to clean up.
func slugForSubject(ctx context.Context, store Store, subject string) (string, error) {
	digest := sha256.Sum256([]byte(subject))
	hex := hex.EncodeToString(digest[:])
	for n := 10; n <= len(hex); n++ {
		candidate := "user-" + strings.ToLower(hex[:n])
		_, err := store.GetBySlug(ctx, candidate)
		if errors.Is(err, ErrNotFound) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("check slug uniqueness: %w", err)
		}
		// Slug exists; widen the suffix and retry.
	}
	return "", errors.New("tenant: could not derive a unique slug for subject (sha-256 collision-style failure)")
}

// wideSlugForSubject returns a slug with a wider hex suffix than
// startingSlug. Used in the rare race where two concurrent
// EnsureForSubject calls both made it past the initial slug
// uniqueness check before either committed.
func wideSlugForSubject(ctx context.Context, store Store, subject, startingSlug string) (string, error) {
	digest := sha256.Sum256([]byte(subject))
	hex := hex.EncodeToString(digest[:])
	startWidth := len(strings.TrimPrefix(startingSlug, "user-"))
	for n := startWidth + 4; n <= len(hex); n += 4 {
		candidate := "user-" + strings.ToLower(hex[:n])
		_, err := store.GetBySlug(ctx, candidate)
		if errors.Is(err, ErrNotFound) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("widen slug uniqueness: %w", err)
		}
	}
	// Fall back to the full digest plus a short random tail.
	return "", errors.New("tenant: could not widen slug for subject (full sha-256 already taken — extreme collision)")
}

// defaultDisplayName returns a display name for a freshly-minted
// tenant. v0.1 just truncates the subject; the dashboard surface
// will let users edit this later (out of scope for this slice).
func defaultDisplayName(subject string) string {
	if len(subject) <= 32 {
		return subject
	}
	return subject[:32]
}

// Compile-time assertion that the concrete *Provisioner satisfies
// TenantProvisioner. If the Provisioner ever drops Provision /
// Deprovision the build fails here, before main.go's wiring can
// drift.
var _ TenantProvisioner = (*Provisioner)(nil)
