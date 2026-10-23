package approvals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaSQL adds the cloud's domain dimension to the framework-owned
// kiff_approvals table.
//
// Extending a framework table from the cloud is safe here for two
// reasons: the column is additive with a default, and the framework's
// own INSERT names its columns explicitly, so a framework write cannot
// break on it. In the cloud the injected DomainScopedStore replaces that
// write path entirely — this column exists for the cloud's predicate,
// and a self-hosted framework user never sees it.
//
// The default is the legacy sentinel rather than NULL so every read can
// use a plain equality predicate; "unattributed" is a value, not an
// absence.
const schemaSQL = `
ALTER TABLE kiff_approvals
    ADD COLUMN IF NOT EXISTS domain_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS kiff_approvals_domain_idx
    ON kiff_approvals (domain_id, inserted_at);

CREATE INDEX IF NOT EXISTS kiff_approvals_domain_grant_idx
    ON kiff_approvals (domain_id, entity_id, action_name, status);
` + requesterSchemaSQL

// requesterSchemaSQL records which API key asked for an approval, so
// that key can be refused when it tries to answer it. The
// approval's requested_by cannot carry it: a proposal's approval names
// the actor the caller sent, not the credential. A table of its own,
// rather than a column, so it can be created on a tenant's first use
// without the lock an ALTER TABLE takes on a live approvals table.
const requesterSchemaSQL = `
CREATE TABLE IF NOT EXISTS cloud_approval_requesters (
    approval_id TEXT PRIMARY KEY REFERENCES kiff_approvals(id) ON DELETE CASCADE,
    api_key_id  TEXT NOT NULL
);
`

// ApplyRequesterSchema creates the requesters table in the schema the
// pool's search_path points at. The resolver runs it when it opens a
// tenant's pool, so tenants provisioned before the table get it on
// first use, as the event-domain table does.
//
// A schema without kiff_approvals is left alone: the table references
// it, and failing here would fail every request for the tenant, not
// only its approvals.
func ApplyRequesterSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var hasApprovals bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('kiff_approvals') IS NOT NULL`).Scan(&hasApprovals); err != nil {
		return fmt.Errorf("approvals: check approvals table: %w", err)
	}
	if !hasApprovals {
		return nil
	}
	if _, err := pool.Exec(ctx, requesterSchemaSQL); err != nil {
		return fmt.Errorf("approvals: apply requester schema: %w", err)
	}
	return nil
}

// SchemaSQL returns the migration as raw DDL, for callers that already
// hold a transaction with search_path pinned and want to apply it
// alongside other schema work — provisioning applies it in the same
// transaction as the framework's schema, so a tenant is never left with
// one and not the other.
func SchemaSQL() string { return schemaSQL }

// Conservative timeouts for the migration transaction.
//
// lock_timeout is the important one. ADD COLUMN takes an ACCESS
// EXCLUSIVE lock; if a long-running transaction holds a conflicting
// lock, the ALTER waits — and every subsequent query on the table
// queues *behind* the waiting ALTER. That is how a metadata-only
// migration takes an application down. Failing after three seconds
// leaves the table untouched and the queue undisturbed; the operator
// retries when the table is quiet.
//
// statement_timeout bounds the backfill UPDATE, which is the only step
// whose cost scales with row count.
const (
	migrationLockTimeout      = "3s"
	migrationStatementTimeout = "30s"
)

// SetMigrationTimeouts applies the guard rails above to the current
// transaction. SET LOCAL scopes them to the transaction, so a pooled
// connection does not carry them back to the application.
//
// Call this first in any transaction that runs ApplySchema or
// BackfillSingleDomain against a live database.
func SetMigrationTimeouts(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '"+migrationLockTimeout+"'"); err != nil {
		return fmt.Errorf("approvals: set lock_timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '"+migrationStatementTimeout+"'"); err != nil {
		return fmt.Errorf("approvals: set statement_timeout: %w", err)
	}
	return nil
}

// ApplySchema applies the domain-dimension migration inside whichever
// schema the connection's search_path points at. Callers are responsible
// for pinning search_path to the tenant schema first — the provisioner
// does this inside its schema-apply transaction, and the operator
// migration command does it per tenant.
//
// Idempotent: every statement is IF NOT EXISTS, so a rerun is a no-op
// and a partially-applied schema converges.
func ApplySchema(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("approvals: apply schema: %w", err)
	}
	return nil
}

// AttributeLegacy stamps every unattributed approval in the current
// schema with domainID, and reports how many rows it changed.
//
// This is an **operator decision, not an inference**, and callers must
// treat it as one. It is deliberately not driven by "the tenant has one
// domain today": domain deletion removes the tenant_domains row and
// leaves the tenant's approvals in place, so a tenant that once had two
// domains, wrote approvals under the second, and deleted it now looks
// single-domain while holding approvals that never belonged to the
// survivor. Current domain count is not proof of historical ownership.
//
// Attributing those rows to the surviving domain would silently grant
// one domain authority over another's approval history — the same
// retroactive-reattribution defect domain scoping rejects for read-time
// resolution, arrived at by a different route.
//
// So this runs only when an operator names the domain explicitly, for
// one tenant, having established ownership from something other than
// the current domain list. Left alone, legacy rows stay unattributed
// and management-only, which is the safe default.
func AttributeLegacy(ctx context.Context, tx pgx.Tx, domainID string) (int64, error) {
	if domainID == LegacyDomainID {
		return 0, fmt.Errorf("approvals: attribution requires an explicit domain id")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE kiff_approvals
		SET domain_id = $1
		WHERE domain_id = ''
	`, domainID)
	if err != nil {
		return 0, fmt.Errorf("approvals: attribute legacy: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountUnattributed reports how many approvals in the current schema
// carry no domain, split by whether they are still pending.
//
// Pending unattributed approvals are the deployment risk of domain
// scoping: once the scoped store ships they stop satisfying their action,
// so an in-flight approval must be re-proposed. This is the measurement
// to run before deploying, not after.
func CountUnattributed(ctx context.Context, tx pgx.Tx) (total, pending int64, err error) {
	err = tx.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'pending')
		FROM kiff_approvals
		WHERE domain_id = ''
	`).Scan(&total, &pending)
	if err != nil {
		return 0, 0, fmt.Errorf("approvals: count unattributed: %w", err)
	}
	return total, pending, nil
}

// StrandedDigest returns a stable fingerprint of the unattributed
// pending approvals in the current schema, plus their ids.
//
// It exists to close the window between measuring and quiescing. The
// operator reviews a specific set of approvals and decides to strand
// them; but the api keeps accepting writes until it is stopped, so by
// the time the migration runs the set may have grown. A count alone
// catches accidental growth only when the total changes — a row added
// and another reviewed in the same interval cancels out. Hashing the
// ids catches any change of membership.
//
// The digest is sha256 over the sorted ids joined by newlines, so it is
// order-independent and safe to compare across runs.
func StrandedDigest(ctx context.Context, tx pgx.Tx) (digest string, ids []string, err error) {
	rows, err := tx.Query(ctx, `
		SELECT id FROM kiff_approvals
		WHERE domain_id = '' AND status = 'pending'
		ORDER BY id
	`)
	if err != nil {
		return "", nil, fmt.Errorf("approvals: stranded digest: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", nil, fmt.Errorf("approvals: scan stranded id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("approvals: iterate stranded: %w", err)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return hex.EncodeToString(sum[:]), ids, nil
}

// AttributeIDs stamps domainID onto exactly the named approvals, and
// only where they are still unattributed. It reports how many rows
// changed and which of the requested ids did not match.
//
// This is the precise form of AttributeLegacy, and it is the one an
// operator should normally reach for. AttributeLegacy stamps every
// unattributed approval in the tenant, which is a much larger set than
// the one usually reviewed: a tenant's pending approvals are a small
// slice of its unattributed rows, and the rest include granted and
// denied history whose ownership was never inspected. Attributing those
// on the strength of a review that did not cover them is the same
// unsound leap as inferring ownership from the current domain list,
// applied to a different axis.
//
// The domain_id = ” guard means an id that has since been attributed
// elsewhere is reported as unmatched rather than silently overwritten.
func AttributeIDs(ctx context.Context, tx pgx.Tx, domainID string, ids []string) (changed int64, unmatched []string, err error) {
	if domainID == LegacyDomainID {
		return 0, nil, fmt.Errorf("approvals: attribution requires an explicit domain id")
	}
	if len(ids) == 0 {
		return 0, nil, fmt.Errorf("approvals: no approval ids given")
	}

	rows, err := tx.Query(ctx, `
		UPDATE kiff_approvals
		SET domain_id = $1
		WHERE domain_id = '' AND id = ANY($2)
		RETURNING id
	`, domainID, ids)
	if err != nil {
		return 0, nil, fmt.Errorf("approvals: attribute ids: %w", err)
	}
	defer rows.Close()

	matched := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, nil, fmt.Errorf("approvals: scan attributed id: %w", err)
		}
		matched[id] = true
		changed++
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("approvals: iterate attributed: %w", err)
	}
	for _, id := range ids {
		if !matched[id] {
			unmatched = append(unmatched, id)
		}
	}
	sort.Strings(unmatched)
	return changed, unmatched, nil
}
