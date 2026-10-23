package auth

import (
	"context"
	_ "embed"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SignedRequests records the phone's signed setup calls already made,
// so none is made twice: not after a restart, and not on another
// API process.
type SignedRequests interface {
	// Use records the request with this replay ID as made, until it can no
	// longer verify. It reports false when the request was already made and
	// its record hasn't passed. An error means nothing could be decided;
	// the caller refuses the request.
	Use(ctx context.Context, replayID []byte, now, validUntil time.Time) (bool, error)
}

// MemorySignedRequests is SignedRequests for one process, without a
// database: lost on restart. Tests and local runs use it.
type MemorySignedRequests struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewMemorySignedRequests returns an empty record.
func NewMemorySignedRequests() *MemorySignedRequests {
	return &MemorySignedRequests{seen: map[string]time.Time{}}
}

func (m *MemorySignedRequests) Use(_ context.Context, replayID []byte, now, validUntil time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, until := range m.seen {
		if now.After(until) {
			delete(m.seen, k)
		}
	}
	key := string(replayID)
	if _, used := m.seen[key]; used {
		return false, nil
	}
	m.seen[key] = validUntil
	return true, nil
}

//go:embed sql/signed_requests.sql
var signedRequestsSQL string

// ApplySignedRequestsSchema runs the signed_requests DDL against pool.
// Idempotent. Call once at API startup, after ApplyCloudMeta.
func ApplySignedRequestsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, signedRequestsSQL); err != nil {
		return fmt.Errorf("apply signed_requests schema: %w", err)
	}
	return nil
}

// PostgresSignedRequests keeps the record in cloud_meta.signed_requests,
// shared by every API process.
type PostgresSignedRequests struct {
	pool *pgxpool.Pool
}

// NewPostgresSignedRequests wraps pool. The caller must have run
// ApplySignedRequestsSchema.
func NewPostgresSignedRequests(pool *pgxpool.Pool) *PostgresSignedRequests {
	return &PostgresSignedRequests{pool: pool}
}

// A row whose record has passed is taken over, so a request is refused for
// exactly as long as its record lasts, whether or not the prune has run.
const sqlUseSignedRequest = `
INSERT INTO cloud_meta.signed_requests (replay_id, valid_until)
VALUES ($1, $2)
ON CONFLICT (replay_id) DO UPDATE SET valid_until = EXCLUDED.valid_until
WHERE cloud_meta.signed_requests.valid_until < $3
`

const sqlPruneSignedRequests = `
DELETE FROM cloud_meta.signed_requests WHERE valid_until < $1
`

func (p *PostgresSignedRequests) Use(ctx context.Context, replayID []byte, now, validUntil time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, sqlUseSignedRequest, replayID, validUntil, now)
	if err != nil {
		return false, fmt.Errorf("record signed request: %w", err)
	}
	// Signed calls are a few per taf set up, so pruning on each one keeps
	// the table small without a sweeper. A failed prune leaves rows for the
	// next one.
	_, _ = p.pool.Exec(ctx, sqlPruneSignedRequests, now)
	return tag.RowsAffected() == 1, nil
}
