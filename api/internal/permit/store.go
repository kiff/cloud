package permit

import (
	"context"
	_ "embed"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/permits.sql
var schemaSQL string

// ApplySchema creates the permit tables. Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schemaSQL)
	return err
}

// Binding is what an operation is bound to at its first decision.
type Binding struct {
	Tool       string
	ArgsSHA256 string
	Audience   string
	// JTI and MintedAt are set once the operation's one permit has been
	// minted.
	JTI      string
	MintedAt time.Time
}

// Same reports whether b names the same call as o.
func (b Binding) Same(o Binding) bool {
	return b.Tool == o.Tool && b.ArgsSHA256 == o.ArgsSHA256 && b.Audience == o.Audience
}

// Store keeps bindings.
type Store interface {
	// Bind records b for (tenant, op) if nothing is recorded yet, and
	// returns what is recorded, which the caller compares with b, and
	// whether this call created it.
	Bind(ctx context.Context, tenantID, op string, b Binding, at time.Time) (Binding, bool, error)
	// MarkMinted records the operation's permit. False when one was
	// already minted: a second is never issued.
	MarkMinted(ctx context.Context, tenantID, op, jti string, at time.Time) (bool, error)
	// Get returns the binding recorded for (tenant, op), if any.
	Get(ctx context.Context, tenantID, op string) (Binding, bool, error)
	// Purge removes bindings created before t that never got a permit.
	Purge(ctx context.Context, before time.Time) (int64, error)
}

// PostgresStore keeps bindings in cloud_meta.permit_bindings.
type PostgresStore struct{ Pool *pgxpool.Pool }

func (s PostgresStore) Bind(ctx context.Context, tenantID, op string, b Binding, at time.Time) (Binding, bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		INSERT INTO cloud_meta.permit_bindings (tenant_id, op, tool, args_sha256, audience, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, op) DO NOTHING`, tenantID, op, b.Tool, b.ArgsSHA256, b.Audience, at.UTC())
	if err != nil {
		return Binding{}, false, err
	}
	created := tag.RowsAffected() == 1
	var got Binding
	var jti *string
	var minted *time.Time
	err = s.Pool.QueryRow(ctx, `SELECT tool, args_sha256, audience, permit_jti, minted_at FROM cloud_meta.permit_bindings
		WHERE tenant_id = $1 AND op = $2`, tenantID, op).Scan(&got.Tool, &got.ArgsSHA256, &got.Audience, &jti, &minted)
	if jti != nil {
		got.JTI = *jti
	}
	if minted != nil {
		got.MintedAt = *minted
	}
	return got, created, err
}

func (s PostgresStore) MarkMinted(ctx context.Context, tenantID, op, jti string, at time.Time) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE cloud_meta.permit_bindings SET permit_jti = $3, minted_at = $4
		WHERE tenant_id = $1 AND op = $2 AND permit_jti IS NULL`, tenantID, op, jti, at.UTC())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s PostgresStore) Get(ctx context.Context, tenantID, op string) (Binding, bool, error) {
	var got Binding
	var jti *string
	var minted *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT tool, args_sha256, audience, permit_jti, minted_at FROM cloud_meta.permit_bindings
		WHERE tenant_id = $1 AND op = $2`, tenantID, op).Scan(&got.Tool, &got.ArgsSHA256, &got.Audience, &jti, &minted)
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, false, nil
	}
	if err != nil {
		return Binding{}, false, err
	}
	if jti != nil {
		got.JTI = *jti
	}
	if minted != nil {
		got.MintedAt = *minted
	}
	return got, true, nil
}

func (s PostgresStore) Purge(ctx context.Context, before time.Time) (int64, error) {
	// A minted binding is kept: it is the guard that the operation never
	// gets a second permit, for as long as its authorization is kept.
	tag, err := s.Pool.Exec(ctx, `DELETE FROM cloud_meta.permit_bindings WHERE created_at < $1 AND permit_jti IS NULL`, before.UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MemoryStore keeps bindings in memory, for local development and tests.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[[2]string]memRow
}

type memRow struct {
	b  Binding
	at time.Time
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{rows: map[[2]string]memRow{}} }

func (s *MemoryStore) Bind(_ context.Context, tenantID, op string, b Binding, at time.Time) (Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{tenantID, op}
	if r, ok := s.rows[k]; ok {
		return r.b, false, nil
	}
	b.JTI, b.MintedAt = "", time.Time{}
	s.rows[k] = memRow{b: b, at: at}
	return b, true, nil
}

func (s *MemoryStore) MarkMinted(_ context.Context, tenantID, op, jti string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{tenantID, op}
	r, ok := s.rows[k]
	if !ok || r.b.JTI != "" {
		return false, nil
	}
	r.b.JTI, r.b.MintedAt = jti, at
	s.rows[k] = r
	return true, nil
}

func (s *MemoryStore) Get(_ context.Context, tenantID, op string) (Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[[2]string{tenantID, op}]
	return r.b, ok, nil
}

func (s *MemoryStore) Purge(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, r := range s.rows {
		if r.at.Before(before) && r.b.JTI == "" {
			delete(s.rows, k)
			n++
		}
	}
	return n, nil
}
