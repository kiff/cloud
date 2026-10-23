package approvals

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"
)

// Approval pairs a framework approval with the domain that owns it.
// The domain is carried alongside rather than inside
// kiffapproval.Approval so the framework type stays untouched — it has
// no domain concept and should not gain one for the cloud's benefit.
//
// DomainID is LegacyDomainID for rows written before approvals were domain-scoped that could
// not be attributed. Surfacing that honestly is the point: a management
// view should show "unattributed", not invent an owner. It is also what
// lets a management reviewer address the right runtime — without it the
// review would resolve the tenant's default domain and fail for an
// approval that belongs to any other.
type Approval struct {
	kiffapproval.Approval
	DomainID string `json:"domain_id"`
}

// TenantConnRunner acquires a transaction whose search_path is pinned
// to one tenant's schema. tenant.SchemaPool satisfies it structurally,
// which is deliberate: the tenant package imports this one (for
// provisioning), so depending on it by name would be a cycle.
type TenantConnRunner interface {
	WithTenantConn(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error
}

// TenantReader is the management-side view over a tenant's approvals.
// It wraps the connection runner so callers depend on a result-shaped
// method rather than on a transaction, which keeps handler tests free
// of a database.
type TenantReader struct{ runner TenantConnRunner }

// NewTenantReader wraps a runner. tenant.SchemaPool is the production
// implementation.
func NewTenantReader(runner TenantConnRunner) *TenantReader {
	return &TenantReader{runner: runner}
}

// ListAll returns every approval in the tenant, annotated with its
// owning domain.
func (r *TenantReader) ListAll(ctx context.Context, tenantID, entityID string) ([]Approval, error) {
	if r == nil {
		return nil, errors.New("approvals: nil tenant reader")
	}
	return ListAllForTenant(ctx, r.runner, tenantID, entityID)
}

// ListAllForTenant returns every approval in the tenant, across all
// domains and including unattributed legacy rows, annotated with its
// owning domain.
//
// This is the management view, and it exists because the tenant-wide
// queue must not borrow a domain runtime's store. Once that store is
// domain-scoped, a dashboard reading through it silently
// shows "approvals for whichever domain happened to resolve" rather
// than the tenant-wide queue it claims to be. That coupling is also why
// the missing domain dimension went unnoticed: the queue looked
// complete because nothing was filtering it.
//
// Access control lives at the handler: this is only ever reached behind
// a management identity. A domain-bound credential reads through its
// scoped runtime store instead.
func ListAllForTenant(ctx context.Context, runner TenantConnRunner, tenantID, entityID string) ([]Approval, error) {
	if runner == nil {
		return nil, errors.New("approvals: nil tenant conn runner")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out := []Approval{}
	err := runner.WithTenantConn(ctx, tenantID, func(tx pgx.Tx) error {
		query := `SELECT ` + columns + `, domain_id FROM kiff_approvals`
		args := []any{}
		if entityID != "" {
			query += ` WHERE entity_id = $1`
			args = append(args, entityID)
		}
		query += ` ORDER BY inserted_at, id`

		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("approvals: tenant query: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				a        Approval
				domainID string
			)
			base, err := scanRowWithExtra(rows, &domainID)
			if err != nil {
				return fmt.Errorf("approvals: tenant scan: %w", err)
			}
			a.Approval = base
			a.DomainID = domainID
			out = append(out, a)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("approvals: tenant iterate: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanRowWithExtra scans the shared column projection plus trailing
// destinations, so the annotated read reuses the same field order as
// the scoped store and the two cannot drift apart.
func scanRowWithExtra(sc scanner, extra ...any) (kiffapproval.Approval, error) {
	return scanRow(appendScanner{sc: sc, extra: extra})
}

// appendScanner adapts a row scan to append extra destinations after
// the shared projection.
type appendScanner struct {
	sc    scanner
	extra []any
}

func (a appendScanner) Scan(dest ...any) error {
	return a.sc.Scan(append(dest, a.extra...)...)
}

// DomainOf returns the domain that owns an approval, and whether the
// approval exists at all.
//
// The review routes address an approval by id alone, with no domain in
// the path. A management caller reviewing from the tenant-wide queue
// therefore has no way to say which domain's runtime should handle it,
// and resolving the tenant default would fail for every approval that
// belongs to any other domain. Looking the owner up from the stamped
// column closes that without changing the route or requiring the client
// to carry the domain back.
//
// An empty domain with ok=true is a legacy unattributed row: it exists,
// but no domain owns it, so no domain runtime can review it.
func (r *TenantReader) DomainOf(ctx context.Context, tenantID, approvalID string) (string, bool, error) {
	if r == nil {
		return "", false, errors.New("approvals: nil tenant reader")
	}
	var (
		domainID string
		found    bool
	)
	err := r.runner.WithTenantConn(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT domain_id FROM kiff_approvals WHERE id = $1`, approvalID).Scan(&domainID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("approvals: domain of %s: %w", approvalID, err)
		}
		found = true
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return domainID, found, nil
}
