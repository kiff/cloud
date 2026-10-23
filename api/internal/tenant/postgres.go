package tenant

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MetaSchema is the Postgres schema where the cloud's bookkeeping
// tables live (currently just `tenants`; api_keys, anchor records,
// and billing data join later). It is distinct from the per-tenant
// `kiff_t_<id>` schemas that hold framework-side data.
const MetaSchema = "cloud_meta"

//go:embed sql/cloud_meta.sql
var cloudMetaSQL string

// CloudMetaSQL returns the DDL that creates `cloud_meta` and its
// tables. Idempotent — every CREATE uses IF NOT EXISTS. Exposed so
// integration tests, demos, and the kiff-tenant CLI can apply the
// schema with a single call.
func CloudMetaSQL() string { return cloudMetaSQL }

// ApplyCloudMeta runs the bookkeeping DDL against the given pool.
// Idempotent. Call once at API startup.
func ApplyCloudMeta(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, cloudMetaSQL); err != nil {
		return fmt.Errorf("apply cloud_meta schema: %w", err)
	}
	return nil
}

// PostgresStore is the production-grade Store. All queries are
// schema-qualified against MetaSchema so the pool's search_path
// does not need to point at cloud_meta.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps the given pool. The caller is responsible
// for the pool's lifetime and for ApplyCloudMeta having been called.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// Create implements Store.
func (s *PostgresStore) Create(ctx context.Context, t Tenant) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	if t.Status == "" {
		t.Status = StatusProvisioning
	}
	// Always persist a fresh salt at creation time; tests and
	// auto-provisioning paths leave RedactionSalt empty, and
	// callers that already have one (operator-led migrations,
	// rare) can pass it through unchanged.
	if len(t.RedactionSalt) == 0 {
		salt, err := NewRedactionSalt()
		if err != nil {
			return err
		}
		t.RedactionSalt = salt
	} else if len(t.RedactionSalt) != RedactionSaltSize {
		return fmt.Errorf("%w: redaction salt must be %d bytes (got %d)", ErrInvalidTenant, RedactionSaltSize, len(t.RedactionSalt))
	}
	meta, err := json.Marshal(t.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	const stmt = `
		INSERT INTO ` + MetaSchema + `.tenants
			(id, slug, display_name, status, created_at, provisioned_at, metadata, redaction_salt)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)
	`
	_, err = s.pool.Exec(ctx, stmt,
		t.ID, t.Slug, t.DisplayName, string(t.Status), t.CreatedAt, t.ProvisionedAt, string(meta), t.RedactionSalt,
	)
	if err != nil {
		if isUniqueViolation(err, "tenants_slug_key") {
			return ErrDuplicateSlug
		}
		return fmt.Errorf("insert tenant: %w", err)
	}
	return nil
}

// Get implements Store.
func (s *PostgresStore) Get(ctx context.Context, id string) (Tenant, error) {
	const stmt = `
		SELECT id, slug, display_name, status, created_at, provisioned_at, metadata, redaction_salt
		FROM ` + MetaSchema + `.tenants
		WHERE id = $1
	`
	row := s.pool.QueryRow(ctx, stmt, id)
	return scanTenant(row)
}

// GetBySlug implements Store.
func (s *PostgresStore) GetBySlug(ctx context.Context, slug string) (Tenant, error) {
	const stmt = `
		SELECT id, slug, display_name, status, created_at, provisioned_at, metadata, redaction_salt
		FROM ` + MetaSchema + `.tenants
		WHERE slug = $1 AND status NOT IN ('deleted', 'deleted_pending')
	`
	row := s.pool.QueryRow(ctx, stmt, slug)
	return scanTenant(row)
}

// List implements Store.
func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]Tenant, error) {
	args := []any{}
	stmt := `
		SELECT id, slug, display_name, status, created_at, provisioned_at, metadata, redaction_salt
		FROM ` + MetaSchema + `.tenants
	`
	if filter.Status != "" {
		stmt += " WHERE status = $1"
		args = append(args, string(filter.Status))
	}
	stmt += " ORDER BY created_at"

	rows, err := s.pool.Query(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	out := make([]Tenant, 0)
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetStatus implements Store.
func (s *PostgresStore) SetStatus(ctx context.Context, id string, status Status, provisionedAt *time.Time) error {
	return SetStatusQ(ctx, s.pool, id, status, provisionedAt)
}

// AddAIGoOn implements Store: one UPDATE sets the period and its count
// together, the count computed from the row as it is, so a failed write
// changes neither and concurrent go-ons both count.
func (s *PostgresStore) AddAIGoOn(ctx context.Context, id string, period time.Time) (int, error) {
	if strings.TrimSpace(id) == "" {
		return 0, ErrNotFound
	}
	const stmt = `
		UPDATE ` + MetaSchema + `.tenants
		SET metadata = COALESCE(NULLIF(metadata, 'null'::jsonb), '{}'::jsonb) || jsonb_build_object(
			$1::text, $3::text,
			$2::text, CASE WHEN metadata->>$1 = $3 THEN COALESCE((metadata->>$2)::int, 0) + 1 ELSE 1 END)
		WHERE id = $4
		RETURNING (metadata->>$2)::int
	`
	var n int
	err := s.pool.QueryRow(ctx, stmt, MetadataKeyAIGoOn, MetadataKeyAIGoOns, period.UTC().Format(time.RFC3339), id).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("add ai go-on: %w", err)
	}
	return n, nil
}

// SetMetadata implements Store. Merges one key into the existing
// metadata JSONB column so unrelated keys are preserved. Passing
// value=nil deletes the key. The query is parameterized; both key
// and value are bound through pgx's parameter substitution to
// keep operator strings out of the SQL text.
//
// The NULLIF(metadata, 'null'::jsonb) guard matters: a freshly
// created tenant whose Metadata map was nil is stored as the JSON
// value `null` (not SQL NULL), and plain COALESCE does not catch it.
// `'null'::jsonb || {...}` yields a JSON *array*, and `'null'::jsonb
// - key` errors on a scalar — either way the metadata is corrupted.
// Coercing jsonb-null to '{}' first keeps the merge/delete object-safe.
func (s *PostgresStore) SetMetadata(ctx context.Context, id, key string, value any) error {
	if strings.TrimSpace(id) == "" {
		return ErrNotFound
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("tenant: SetMetadata requires a non-empty key")
	}
	if value == nil {
		const stmt = `
			UPDATE ` + MetaSchema + `.tenants
			SET metadata = COALESCE(NULLIF(metadata, 'null'::jsonb), '{}'::jsonb) - $1
			WHERE id = $2
		`
		tag, err := s.pool.Exec(ctx, stmt, key, id)
		if err != nil {
			return fmt.Errorf("delete tenant metadata key: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode metadata value: %w", err)
	}
	const stmt = `
		UPDATE ` + MetaSchema + `.tenants
		SET metadata = COALESCE(NULLIF(metadata, 'null'::jsonb), '{}'::jsonb) || jsonb_build_object($1::text, $2::jsonb)
		WHERE id = $3
	`
	tag, err := s.pool.Exec(ctx, stmt, key, string(encoded), id)
	if err != nil {
		return fmt.Errorf("set tenant metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete implements Store.
func (s *PostgresStore) Delete(ctx context.Context, id string) error {
	const stmt = `DELETE FROM ` + MetaSchema + `.tenants WHERE id = $1`
	tag, err := s.pool.Exec(ctx, stmt, id)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// NullRedactionSalt implements Store. Zeroes the stored salt so
// historical on-chain hashes for this tenant are permanently
// unreconstructable. Called by Deprovision after the schema is
// dropped. Idempotent — nulling an already-null salt is a no-op.
func (s *PostgresStore) NullRedactionSalt(ctx context.Context, id string) error {
	const stmt = `UPDATE ` + MetaSchema + `.tenants SET redaction_salt = NULL WHERE id = $1`
	tag, err := s.pool.Exec(ctx, stmt, id)
	if err != nil {
		return fmt.Errorf("null redaction salt: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// scanTenant abstracts row scanning so QueryRow and Query share
// columns. The interface accepts both *pgx.Row and pgx.Rows because
// they share Scan.
type scannable interface {
	Scan(dest ...any) error
}

func scanTenant(row scannable) (Tenant, error) {
	var (
		t           Tenant
		statusText  string
		provisioned *time.Time
		metaBytes   []byte
		salt        []byte
	)
	err := row.Scan(&t.ID, &t.Slug, &t.DisplayName, &statusText, &t.CreatedAt, &provisioned, &metaBytes, &salt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Tenant{}, ErrNotFound
		}
		return Tenant{}, fmt.Errorf("scan tenant: %w", err)
	}
	t.Status = Status(statusText)
	t.ProvisionedAt = provisioned
	if len(metaBytes) > 0 {
		var meta map[string]any
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			return Tenant{}, fmt.Errorf("decode metadata: %w", err)
		}
		t.Metadata = meta
	}
	// salt is nil for legacy rows pending backfill; pass through
	// unchanged so callers can use Tenant.HasRedactionSalt.
	t.RedactionSalt = salt
	return t, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation on the named constraint. Used by Create to translate the
// driver error into ErrDuplicateSlug.
func isUniqueViolation(err error, constraint string) bool {
	type pgErrCode interface {
		SQLState() string
	}
	type pgErrName interface {
		ConstraintName() string
	}
	var coder pgErrCode
	if !errors.As(err, &coder) || coder.SQLState() != "23505" {
		return false
	}
	if namer, ok := err.(pgErrName); ok {
		return namer.ConstraintName() == constraint
	}
	return constraint == "" || true
}
