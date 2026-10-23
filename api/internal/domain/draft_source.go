package domain

import (
	"context"
	"errors"
)

// draft_source.go — the auto-save store for KIFF Studio drafts.
// Drafts hold a tenant's
// in-progress domain YAML between explicit "Save and activate"
// clicks. The runtime resolver does NOT read drafts; only the
// active domain (Source above) reaches a per-tenant Runtime.
//
// The split between Source (active) and DraftSource (in-progress)
// matters for two properties:
//
//  1. Save is transactional. Activating a draft upserts the
//     active row and deletes the draft inside one BEGIN/COMMIT,
//     so the resolver never sees a missing-domain window.
//  2. Drafts can be invalid. A user mid-edit may have an action
//     with no states declared yet (Stage 1 → Stage 2 promotion);
//     domain.ParseAndBuild would refuse it, but the user expects
//     to keep typing. DraftSource stores raw YAML; the *handler*
//     validates as a best-effort pass for the response payload
//     (populating the response's `parsed` / `issues` fields), but
//     never blocks the save.
//
// MemoryDraftSource is the test-time implementation.
// PostgresDraftSource is the production implementation.

// DraftSource persists per-tenant draft YAML for the Studio
// authoring surface. One draft per tenant; concurrent writers
// resolve via last-write-wins at the storage layer.
type DraftSource interface {
	// Load returns the tenant's current draft YAML, or
	// ErrDraftNotFound when no draft exists.
	Load(ctx context.Context, tenantID string) (TenantDraft, error)

	// Save upserts the draft. The yaml may be invalid mid-edit;
	// callers are responsible for validating before promoting to
	// the active Source.
	Save(ctx context.Context, tenantID string, yaml []byte) error

	// Delete removes the draft. Used inside the activate
	// transaction so the runtime resolver only ever sees one row
	// per tenant in the active Source.
	Delete(ctx context.Context, tenantID string) error
}

// TenantDraft is what DraftSource.Load returns.
type TenantDraft struct {
	TenantID  string
	YAML      []byte
	UpdatedAt int64 // Unix seconds; used by the auto-saved indicator.
}

// ErrDraftNotFound is returned when a tenant has no draft.
// The Studio handler treats this as "render the placeholder
// Stage 1 draft" rather than as an error.
var ErrDraftNotFound = errors.New("domain: tenant has no draft")
