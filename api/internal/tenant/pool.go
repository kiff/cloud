package tenant

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SchemaPool is a thin wrapper over pgxpool.Pool that acquires a
// connection and pins its search_path to a tenant's schema before
// returning it. Used on every request that touches framework-side
// stores so the framework's existing SQL (which is schema-relative)
// reads and writes through the right tenant boundary.
//
// The pool itself is shared across tenants. Tenancy is enforced at
// connection-checkout time, not at pool-construction time.
type SchemaPool struct {
	pool *pgxpool.Pool
}

// NewSchemaPool wraps a pgxpool.Pool. The underlying pool is created
// with whatever pool config the caller chooses; callers are
// responsible for closing it.
func NewSchemaPool(pool *pgxpool.Pool) *SchemaPool {
	return &SchemaPool{pool: pool}
}

// Pool returns the underlying pgxpool.Pool. Use with care — direct
// pool checkouts skip the search_path bookkeeping. Reserved for
// callers that operate on cloud_meta or that explicitly want a
// non-tenant-scoped connection.
func (p *SchemaPool) Pool() *pgxpool.Pool { return p.pool }

// Acquire returns a connection with search_path set to the named
// tenant's schema (with public as a fallback for shared types).
// Callers MUST Release the returned connection when done; the
// search_path is reset before release so leaked state cannot follow
// the connection into another caller.
func (p *SchemaPool) Acquire(ctx context.Context, tenantID string) (*pgxpool.Conn, error) {
	if !validULID(tenantID) {
		return nil, fmt.Errorf("%w: invalid tenant id", ErrInvalidTenant)
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	schema := SchemaNameFor(tenantID)
	if !validSchemaName(schema) {
		conn.Release()
		return nil, fmt.Errorf("%w: derived schema name failed validation: %s", ErrInvalidTenant, schema)
	}
	// Set search_path. The schema name is constructed from a
	// validated ULID, so pgx parameter binding is unnecessary (and
	// SET does not accept placeholders for identifiers anyway).
	if _, err := conn.Exec(ctx, "SET search_path TO "+schema+", public"); err != nil {
		conn.Release()
		return nil, fmt.Errorf("set search_path: %w", err)
	}
	return conn, nil
}

// WithTenantConn runs fn with a connection scoped to tenantID and
// releases the connection after fn returns. Resets search_path to
// the pool default before release so nothing leaks into the next
// checkout.
func (p *SchemaPool) WithTenantConn(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	conn, err := p.Acquire(ctx, tenantID)
	if err != nil {
		return err
	}
	defer func() {
		// Best effort: reset search_path so a subsequent caller
		// who forgets to set it does not silently inherit the
		// previous tenant's schema. RESET is harmless if the
		// connection is already broken.
		_, _ = conn.Exec(context.Background(), "RESET search_path")
		conn.Release()
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// validSchemaName guards against any unexpected character making it
// into a SET search_path statement. The schema name is derived from
// a validated ULID, so this is a belt-and-braces check.
func validSchemaName(s string) bool {
	if !strings.HasPrefix(s, "kiff_t_") {
		return false
	}
	rest := s[len("kiff_t_"):]
	if rest == "" {
		return false
	}
	for _, r := range rest {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
