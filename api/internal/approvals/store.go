// Package approvals provides the cloud's domain-scoped approval store.
//
// The framework's approval store has no domain dimension, because in the
// framework one runtime *is* one domain. The cloud is different: a
// multi-domain tenant keeps every domain's approvals in one
// per-tenant schema, so without a domain predicate an approval granted
// in one domain satisfies the same (entity_id, action_name) in another.
// That is an authorization boundary, not only a scoping nicety.
//
// This package closes it by owning the approval table's reads and
// writes cloud-side and binding every statement to (tenant, domain):
// tenant via the pool's pinned search_path, domain via the store's
// constructor argument. Ownership is stamped at INSERT — in the same
// statement, so there is no window in which an approval exists without
// a domain — rather than derived at read time, which would let a later
// domain activation retroactively reattribute an existing approval.
//
// The framework needs no change to support this: runtime.Config's
// ApprovalStore field is injectable and takes precedence over the store
// bundle, and the runtime delegates every approval write to it.
package approvals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"

	"github.com/kiff/cloud/api/internal/auth"
)

// LegacyDomainID is the domain of approvals written before approvals were domain-scoped.
// Those rows cannot be attributed safely — guessing would reintroduce
// the retroactive-reattribution defect the design rejects — so they are
// invisible to every domain-scoped store and reachable only through the
// tenant-wide management reader.
const LegacyDomainID = ""

// ErrCrossDomain is returned when a write would touch an approval owned
// by another domain. It means the caller resolved an approval id that
// exists but is not theirs; surfacing it as an error rather than a
// silent no-op keeps a cross-domain review from looking like success.
var ErrCrossDomain = errors.New("approvals: approval belongs to another domain")

// columns is the projection every read shares. domain_id is deliberately
// absent: it is storage, and kiffapproval.Approval has no field for it
// (the framework type stays untouched).
const columns = `id, entity_id, entity_type, action_name, requested_by,
	              reviewed_by, status, reason, created_at, reviewed_at`

// DomainScopedStore implements kiffapproval.Store for one (tenant,
// domain). The tenant comes from the pool's search_path; the domain is
// applied as a predicate on every statement.
type DomainScopedStore struct {
	pool     *pgxpool.Pool
	domainID string
}

// NewDomainScoped binds a store to one domain. An empty domainID is
// rejected: it would silently produce a store scoped to the legacy
// partition, which reads as "sees only unattributed rows" rather than
// the intended "sees everything" and would be a confusing failure. Use
// NewTenantReader for the unscoped management view.
func NewDomainScoped(pool *pgxpool.Pool, domainID string) (*DomainScopedStore, error) {
	if pool == nil {
		return nil, errors.New("approvals: nil pool")
	}
	if domainID == LegacyDomainID {
		return nil, errors.New("approvals: domain id is required for a domain-scoped store")
	}
	return &DomainScopedStore{pool: pool, domainID: domainID}, nil
}

// Save inserts or updates an approval, stamping this store's domain.
//
// The framework's contract for Save is an upsert: a review is persisted
// by saving the same approval id with a new status. That makes the
// conflict branch a security boundary as much as the insert is — without
// a predicate, a domain-bound caller could overwrite another domain's
// approval by reusing its id. The DO UPDATE therefore carries a domain
// guard, and a conflict that fails it reports ErrCrossDomain instead of
// silently affecting no rows.
func (s *DomainScopedStore) Save(ctx context.Context, a kiffapproval.Approval) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}

	var reviewedAt any
	if !a.ReviewedAt.IsZero() {
		reviewedAt = a.ReviewedAt.UTC()
	}

	// The approval and who asked for it are written together, so an
	// approval a key asked for never exists without the record that
	// refuses that key its review.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("approvals: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit

	tag, err := tx.Exec(ctx, `
		INSERT INTO kiff_approvals (
			id, entity_id, entity_type, action_name, requested_by,
			reviewed_by, status, reason, created_at, reviewed_at, domain_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO UPDATE SET
			entity_id     = EXCLUDED.entity_id,
			entity_type   = EXCLUDED.entity_type,
			action_name   = EXCLUDED.action_name,
			requested_by  = EXCLUDED.requested_by,
			reviewed_by   = EXCLUDED.reviewed_by,
			status        = EXCLUDED.status,
			reason        = EXCLUDED.reason,
			created_at    = EXCLUDED.created_at,
			reviewed_at   = EXCLUDED.reviewed_at
		WHERE kiff_approvals.domain_id = $11
	`,
		a.ID, a.EntityID, a.EntityType, a.ActionName, a.RequestedBy,
		nullable(a.ReviewedBy), string(a.Status), nullable(a.Reason),
		a.CreatedAt.UTC(), reviewedAt, s.domainID,
	)
	if err != nil {
		return fmt.Errorf("approvals: upsert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The insert conflicted and the domain guard rejected the
		// update: the id exists under another domain (or is a legacy
		// unattributed row).
		return ErrCrossDomain
	}
	// A pending approval saved with an API key was asked for by that
	// key. The first key to ask is kept; a review (saved as granted or
	// denied, by the reviewer's credential) records nothing.
	caller, _ := auth.IdentityFrom(ctx)
	if keyID := caller.APIKeyID(); keyID != "" && a.Status == kiffapproval.StatusPending {
		if _, err := tx.Exec(ctx, `
			INSERT INTO cloud_approval_requesters (approval_id, api_key_id)
			VALUES ($1, $2)
			ON CONFLICT (approval_id) DO NOTHING
		`, a.ID, keyID); err != nil {
			return fmt.Errorf("approvals: record requester: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("approvals: commit: %w", err)
	}
	return nil
}

// RequesterKey returns the id of the API key that asked for the
// approval, or "" when a person asked, or nobody recorded it (an
// approval saved before the requesters table). Scoped to this domain.
func (s *DomainScopedStore) RequesterKey(ctx context.Context, id string) (string, error) {
	var keyID string
	err := s.pool.QueryRow(ctx, `
		SELECT r.api_key_id
		FROM cloud_approval_requesters r
		JOIN kiff_approvals a ON a.id = r.approval_id
		WHERE r.approval_id = $1 AND a.domain_id = $2
	`, id, s.domainID).Scan(&keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("approvals: requester: %w", err)
	}
	return keyID, nil
}

// Get returns an approval by id, scoped to this domain. An approval that
// exists under another domain reports not-found rather than an error:
// existence outside the caller's domain is itself information, and the
// framework's callers treat the boolean as "no such approval".
func (s *DomainScopedStore) Get(ctx context.Context, id string) (kiffapproval.Approval, bool, error) {
	if err := ctx.Err(); err != nil {
		return kiffapproval.Approval{}, false, err
	}
	row := s.pool.QueryRow(ctx, `
		SELECT `+columns+`
		FROM kiff_approvals
		WHERE id = $1 AND domain_id = $2
	`, id, s.domainID)

	a, err := scanRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return kiffapproval.Approval{}, false, nil
		}
		return kiffapproval.Approval{}, false, fmt.Errorf("approvals: get: %w", err)
	}
	return a, true, nil
}

// List returns this domain's approvals for an entity, or all of this
// domain's approvals when entityID is empty — matching the framework
// store's contract, minus every other domain's rows.
func (s *DomainScopedStore) List(ctx context.Context, entityID string) ([]kiffapproval.Approval, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	query := `SELECT ` + columns + ` FROM kiff_approvals WHERE domain_id = $1`
	args := []any{s.domainID}
	if entityID != "" {
		query += ` AND entity_id = $2`
		args = append(args, entityID)
	}
	query += ` ORDER BY inserted_at, id`

	return queryList(ctx, s.pool, query, args...)
}

// IsGranted reports whether a granted approval matches the id, entity,
// and action *within this domain*.
//
// This is the authorization fix domain scoping exists for. The framework's
// query matches on (id, entity_id, action_name, status) alone, which in
// a shared tenant schema lets an approval granted under one domain
// authorize the same action name on the same entity id under another —
// reachable precisely because generic action names legitimately repeat
// across domains. The domain predicate closes that.
func (s *DomainScopedStore) IsGranted(ctx context.Context, id, entityID, actionName string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var found bool
	err := s.pool.QueryRow(ctx, `
		SELECT TRUE
		FROM kiff_approvals
		WHERE id = $1
		  AND entity_id = $2
		  AND action_name = $3
		  AND status = $4
		  AND domain_id = $5
	`, id, entityID, actionName, string(kiffapproval.StatusGranted), s.domainID).Scan(&found)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("approvals: check granted: %w", err)
	}
	return found, nil
}

// scanner is the shared shape of pgx.Row and pgx.Rows for scanning.
type scanner interface {
	Scan(dest ...any) error
}

func scanRow(sc scanner) (kiffapproval.Approval, error) {
	var (
		a          kiffapproval.Approval
		reviewedBy *string
		reason     *string
		reviewedAt *time.Time
		status     string
	)
	if err := sc.Scan(
		&a.ID, &a.EntityID, &a.EntityType, &a.ActionName, &a.RequestedBy,
		&reviewedBy, &status, &reason, &a.CreatedAt, &reviewedAt,
	); err != nil {
		return kiffapproval.Approval{}, err
	}
	a.Status = kiffapproval.Status(status)
	if reviewedBy != nil {
		a.ReviewedBy = *reviewedBy
	}
	if reason != nil {
		a.Reason = *reason
	}
	if reviewedAt != nil {
		a.ReviewedAt = reviewedAt.UTC()
	}
	a.CreatedAt = a.CreatedAt.UTC()
	return a, nil
}

func queryList(ctx context.Context, pool *pgxpool.Pool, query string, args ...any) ([]kiffapproval.Approval, error) {
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("approvals: query: %w", err)
	}
	defer rows.Close()

	out := []kiffapproval.Approval{}
	for rows.Next() {
		a, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("approvals: scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("approvals: iterate: %w", err)
	}
	return out, nil
}

// nullable maps "" to SQL NULL so optional columns stay null rather than
// empty-string, matching what the framework store writes.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
