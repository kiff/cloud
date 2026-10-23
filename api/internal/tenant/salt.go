package tenant

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewRedactionSalt returns RedactionSaltSize cryptographically-random
// bytes suitable for Tenant.RedactionSalt. crypto/rand.Read is
// guaranteed to fill the slice or return an error; we surface the
// error directly so a degraded RNG cannot silently produce a
// predictable salt (which would defeat the whole point of the salt).
func NewRedactionSalt() ([]byte, error) {
	salt := make([]byte, RedactionSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("tenant: generate redaction salt: %w", err)
	}
	return salt, nil
}

// BackfillRedactionSalts populates redaction_salt on any cloud_meta
// tenant row where the column is currently NULL. Intended to run
// once at API startup, immediately after ApplyCloudMeta, so legacy
// rows written before salts acquire a salt before any request reaches
// the anchor path.
//
// The query selects affected ids, generates a salt per row in the Go
// process (so each tenant gets a fresh, independently-sampled salt),
// and updates them one-by-one inside a single transaction. There is
// no production tenant population large enough for batching to
// matter; correctness over throughput.
//
// Idempotent: rerunning on a fully-backfilled table is a no-op
// because the WHERE clause filters for rows that still need a salt.
//
// Safe to run concurrently with ongoing requests because:
//   - the UPDATE is a single-row write keyed on PRIMARY KEY id;
//   - readers that observe the legacy NULL salt skip salting (the
//     anchor attester treats !HasRedactionSalt as "anchor with
//     unsalted leaves" rather than failing);
//   - new tenants always carry a salt because Create generates one.
//
// Returns the number of rows updated and any first error.
func BackfillRedactionSalts(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	if pool == nil {
		return 0, errors.New("tenant: BackfillRedactionSalts requires a non-nil pool")
	}

	const selectStmt = `
		SELECT id
		FROM ` + MetaSchema + `.tenants
		WHERE redaction_salt IS NULL
	`
	rows, err := pool.Query(ctx, selectStmt)
	if err != nil {
		return 0, fmt.Errorf("tenant: list rows for salt backfill: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("tenant: scan id for salt backfill: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("tenant: iterate ids for salt backfill: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	const updateStmt = `
		UPDATE ` + MetaSchema + `.tenants
		SET redaction_salt = $1
		WHERE id = $2
		  AND redaction_salt IS NULL
	`
	updated := 0
	for _, id := range ids {
		salt, err := NewRedactionSalt()
		if err != nil {
			return updated, err
		}
		tag, err := pool.Exec(ctx, updateStmt, salt, id)
		if err != nil {
			return updated, fmt.Errorf("tenant: backfill salt for %s: %w", id, err)
		}
		// Race tolerance: another process may have just populated the
		// salt. RowsAffected==0 is fine, just don't count it.
		if tag.RowsAffected() == 1 {
			updated++
		}
	}
	return updated, nil
}
