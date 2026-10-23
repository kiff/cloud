package receipt

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/receipts.sql
var receiptsSQL string

// ReceiptsSQL returns the DDL that creates cloud_meta.receipts.
// Idempotent. Call once at startup alongside tenant.ApplyCloudMeta.
func ReceiptsSQL() string { return receiptsSQL }

// ApplySchema runs the receipts DDL against pool. Idempotent.
// Callers usually invoke this at API startup, after
// tenant.ApplyCloudMeta (the foreign key requires
// cloud_meta.tenants).
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, receiptsSQL); err != nil {
		return fmt.Errorf("apply receipts schema: %w", err)
	}
	return nil
}

// Errors returned by Store implementations.
var (
	ErrReceiptNotFound = errors.New("receipt: not found")
)

// OpsActivityLimit caps the cross-tenant activity feed and the
// overview's recent-receipts read. Bounds
// the single sanctioned cross-tenant receipt query so the operator
// feed can never pull an unbounded scan of cloud_meta.receipts.
const OpsActivityLimit = 100

// Store is the persistence boundary for receipts. The HTTP
// endpoint and the dashboard read through this interface; the
// signing/anchoring path will use the same Store to flip
// anchor_tx_hash and anchored_at after the next anchor cycle.
type Store interface {
	// Save persists a receipt. Upserts on (tenant_id, trace_id)
	// so re-saving with new attestation fields after anchoring
	// updates the row in place.
	Save(ctx context.Context, r Receipt) error

	// Get returns the persisted receipt for a (tenant, trace).
	// Returns ErrReceiptNotFound when no row exists.
	Get(ctx context.Context, tenantID, traceID string) (Receipt, error)

	// GetByProposalID returns the receipt the given proposal produced
	// — the explicit decision->receipt link. Returns
	// ErrReceiptNotFound when no receipt carries that proposal id
	// (e.g. blocked/approval-required/shadow decisions, or legacy
	// receipts written before G2 stamped proposal_id).
	GetByProposalID(ctx context.Context, tenantID, proposalID string) (Receipt, error)

	// ListUnanchored returns receipts that have been saved but
	// not yet attested (anchor_tx_hash IS NULL). Used by the
	// hourly anchoring job. Order is by issued_at ascending so
	// older receipts anchor first.
	ListUnanchored(ctx context.Context, tenantID string) ([]Receipt, error)

	// List returns the most recent receipts for a tenant in
	// descending issued_at order, capped at limit. limit <= 0
	// implementations default to 50. Used by the dashboard's
	// decisions table.
	List(ctx context.Context, tenantID string, limit int) ([]Receipt, error)

	// ListRecentAcrossTenants returns the most recent receipts
	// across ALL tenants in descending issued_at order, capped at
	// limit (implementations default to 50 when limit <= 0). This
	// is the single sanctioned cross-tenant receipt read — it
	// powers the operator console's activity feed and
	// must never be wired into a tenant-facing handler. Physically
	// one query against cloud_meta.receipts with no tenant
	// predicate.
	ListRecentAcrossTenants(ctx context.Context, limit int) ([]Receipt, error)

	// AnchorBacklog returns the count of unanchored receipts across
	// ALL tenants and the issued_at of the oldest one (zero time
	// when none). Operator-console only (anchor-health
	// signal); a cheap aggregate over the partial unanchored index.
	AnchorBacklog(ctx context.Context) (pending int, oldest time.Time, err error)
}

// MemoryStore is the in-process Store used by unit tests of
// higher layers. Concurrent-safe.
type MemoryStore struct {
	mu   sync.RWMutex
	rows map[storeKey]Receipt
}

type storeKey struct {
	TenantID string
	TraceID  string
}

// NewMemoryStore returns an empty in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[storeKey]Receipt{}}
}

// Save implements Store.
func (m *MemoryStore) Save(_ context.Context, r Receipt) error {
	if err := validateForSave(r); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[storeKey{r.TenantID, r.TraceID}] = cloneReceipt(r)
	return nil
}

// Get implements Store.
func (m *MemoryStore) Get(_ context.Context, tenantID, traceID string) (Receipt, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rows[storeKey{tenantID, traceID}]
	if !ok {
		return Receipt{}, ErrReceiptNotFound
	}
	return cloneReceipt(r), nil
}

// GetByProposalID implements Store.
func (m *MemoryStore) GetByProposalID(_ context.Context, tenantID, proposalID string) (Receipt, error) {
	if proposalID == "" {
		return Receipt{}, ErrReceiptNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	// The newest, as the Postgres store orders; at the same time a settled
	// receipt over the decision receipt it follows, then by trace,
	// so the choice never depends on map order.
	var found *Receipt
	for k, r := range m.rows {
		if k.TenantID == tenantID && r.ProposalID == proposalID && (found == nil || newerReceipt(r, *found)) {
			r := r
			found = &r
		}
	}
	if found == nil {
		return Receipt{}, ErrReceiptNotFound
	}
	return cloneReceipt(*found), nil
}

// ListUnanchored implements Store.
func (m *MemoryStore) ListUnanchored(_ context.Context, tenantID string) ([]Receipt, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Receipt, 0)
	for k, r := range m.rows {
		if k.TenantID != tenantID {
			continue
		}
		if r.Attestation != nil {
			continue
		}
		out = append(out, cloneReceipt(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssuedAt.Before(out[j].IssuedAt) })
	return out, nil
}

// List implements Store.
func (m *MemoryStore) List(_ context.Context, tenantID string, limit int) ([]Receipt, error) {
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Receipt, 0)
	for k, r := range m.rows {
		if k.TenantID != tenantID {
			continue
		}
		out = append(out, cloneReceipt(r))
	}
	// DESC by issued_at, tiebreak on trace id for stability.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].IssuedAt.Equal(out[j].IssuedAt) {
			return out[i].IssuedAt.After(out[j].IssuedAt)
		}
		return out[i].TraceID > out[j].TraceID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ListRecentAcrossTenants implements Store.
func (m *MemoryStore) ListRecentAcrossTenants(_ context.Context, limit int) ([]Receipt, error) {
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Receipt, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, cloneReceipt(r))
	}
	// DESC by issued_at, tiebreak on (tenant, trace) for stability.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].IssuedAt.Equal(out[j].IssuedAt) {
			return out[i].IssuedAt.After(out[j].IssuedAt)
		}
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID > out[j].TenantID
		}
		return out[i].TraceID > out[j].TraceID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// AnchorBacklog implements Store.
func (m *MemoryStore) AnchorBacklog(_ context.Context) (int, time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pending := 0
	var oldest time.Time
	for _, r := range m.rows {
		if r.Attestation != nil {
			continue
		}
		pending++
		if oldest.IsZero() || r.IssuedAt.Before(oldest) {
			oldest = r.IssuedAt
		}
	}
	return pending, oldest, nil
}

type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps the cloud pool. The caller is responsible
// for the pool's lifetime and for ApplySchema having been called.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const sqlSaveReceipt = `
INSERT INTO cloud_meta.receipts (
    tenant_id, trace_id, proposal_id, schema_version, entity_id, entity_type,
    action_name, issued_at, causal_chain_hash, body,
    anchor_tx_hash, anchored_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10,
    $11, $12, NOW()
)
ON CONFLICT (tenant_id, trace_id) DO UPDATE SET
    proposal_id       = EXCLUDED.proposal_id,
    schema_version    = EXCLUDED.schema_version,
    entity_id         = EXCLUDED.entity_id,
    entity_type       = EXCLUDED.entity_type,
    action_name       = EXCLUDED.action_name,
    issued_at         = EXCLUDED.issued_at,
    causal_chain_hash = EXCLUDED.causal_chain_hash,
    body              = EXCLUDED.body,
    anchor_tx_hash    = EXCLUDED.anchor_tx_hash,
    anchored_at       = EXCLUDED.anchored_at,
    updated_at        = NOW()
`

const sqlGetReceipt = `
SELECT body
FROM cloud_meta.receipts
WHERE tenant_id = $1 AND trace_id = $2
`

const sqlGetReceiptByProposal = `
SELECT body
FROM cloud_meta.receipts
WHERE tenant_id = $1 AND proposal_id = $2
ORDER BY issued_at DESC, (trace_id LIKE 'settled-%') DESC, trace_id DESC
LIMIT 1
`

const sqlListUnanchored = `
SELECT body
FROM cloud_meta.receipts
WHERE tenant_id = $1 AND anchor_tx_hash IS NULL
ORDER BY issued_at ASC
`

const sqlListReceipts = `
SELECT body
FROM cloud_meta.receipts
WHERE tenant_id = $1
ORDER BY issued_at DESC, trace_id DESC
LIMIT $2
`

const sqlListReceiptsAllTenants = `
SELECT body
FROM cloud_meta.receipts
ORDER BY issued_at DESC, tenant_id DESC, trace_id DESC
LIMIT $1
`

// Save implements Store.
func (p *PostgresStore) Save(ctx context.Context, r Receipt) error {
	if err := validateForSave(r); err != nil {
		return err
	}
	chainHash, err := r.CausalChainHash()
	if err != nil {
		return fmt.Errorf("hash chain: %w", err)
	}
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal receipt: %w", err)
	}

	var anchorTx *string
	var anchoredAt *time.Time
	if r.Attestation != nil {
		tx := r.Attestation.AnchorTxHash
		anchorTx = &tx
		at := r.Attestation.AnchoredAt
		anchoredAt = &at
	}

	if _, err := p.pool.Exec(ctx, sqlSaveReceipt,
		r.TenantID, r.TraceID, r.ProposalID, r.SchemaVersion, r.EntityID, r.EntityType,
		r.ActionName, r.IssuedAt, chainHash, body,
		anchorTx, anchoredAt,
	); err != nil {
		return fmt.Errorf("save receipt: %w", err)
	}
	return nil
}

// Get implements Store.
func (p *PostgresStore) Get(ctx context.Context, tenantID, traceID string) (Receipt, error) {
	row := p.pool.QueryRow(ctx, sqlGetReceipt, tenantID, traceID)
	var body []byte
	if err := row.Scan(&body); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Receipt{}, ErrReceiptNotFound
		}
		return Receipt{}, fmt.Errorf("get receipt: %w", err)
	}
	var r Receipt
	if err := json.Unmarshal(body, &r); err != nil {
		return Receipt{}, fmt.Errorf("unmarshal receipt body: %w", err)
	}
	return r, nil
}

// GetByProposalID implements Store. Resolves the receipt by the
// explicit proposal->receipt link, tenant-scoped.
func (p *PostgresStore) GetByProposalID(ctx context.Context, tenantID, proposalID string) (Receipt, error) {
	if proposalID == "" {
		return Receipt{}, ErrReceiptNotFound
	}
	row := p.pool.QueryRow(ctx, sqlGetReceiptByProposal, tenantID, proposalID)
	var body []byte
	if err := row.Scan(&body); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Receipt{}, ErrReceiptNotFound
		}
		return Receipt{}, fmt.Errorf("get receipt by proposal: %w", err)
	}
	var r Receipt
	if err := json.Unmarshal(body, &r); err != nil {
		return Receipt{}, fmt.Errorf("unmarshal receipt body: %w", err)
	}
	return r, nil
}

// ListUnanchored implements Store.
func (p *PostgresStore) ListUnanchored(ctx context.Context, tenantID string) ([]Receipt, error) {
	rows, err := p.pool.Query(ctx, sqlListUnanchored, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list unanchored: %w", err)
	}
	defer rows.Close()
	out := make([]Receipt, 0)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("scan unanchored: %w", err)
		}
		var r Receipt
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("unmarshal unanchored body: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// List implements Store.
func (p *PostgresStore) List(ctx context.Context, tenantID string, limit int) ([]Receipt, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := p.pool.Query(ctx, sqlListReceipts, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list receipts: %w", err)
	}
	defer rows.Close()
	out := make([]Receipt, 0, limit)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("scan receipt: %w", err)
		}
		var r Receipt
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("unmarshal receipt body: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListRecentAcrossTenants implements Store.
func (p *PostgresStore) ListRecentAcrossTenants(ctx context.Context, limit int) ([]Receipt, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := p.pool.Query(ctx, sqlListReceiptsAllTenants, limit)
	if err != nil {
		return nil, fmt.Errorf("list receipts across tenants: %w", err)
	}
	defer rows.Close()
	out := make([]Receipt, 0, limit)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("scan receipt: %w", err)
		}
		var r Receipt
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("unmarshal receipt body: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

const sqlAnchorBacklog = `
SELECT COUNT(*), COALESCE(MIN(issued_at), 'epoch'::timestamptz)
FROM cloud_meta.receipts
WHERE anchor_tx_hash IS NULL
`

// AnchorBacklog implements Store. One aggregate over the partial
// unanchored index.
func (p *PostgresStore) AnchorBacklog(ctx context.Context) (int, time.Time, error) {
	var pending int
	var oldest time.Time
	if err := p.pool.QueryRow(ctx, sqlAnchorBacklog).Scan(&pending, &oldest); err != nil {
		return 0, time.Time{}, fmt.Errorf("anchor backlog: %w", err)
	}
	if pending == 0 {
		oldest = time.Time{}
	}
	return pending, oldest, nil
}

func validateForSave(r Receipt) error {
	switch {
	case r.TenantID == "":
		return errors.New("receipt: TenantID required")
	case r.TraceID == "":
		return errors.New("receipt: TraceID required")
	case r.SchemaVersion == "":
		return errors.New("receipt: SchemaVersion required")
	case r.EntityID == "":
		return errors.New("receipt: EntityID required")
	case r.IssuedAt.IsZero():
		return errors.New("receipt: IssuedAt required")
	}
	return nil
}

// cloneReceipt returns a deep-enough copy that the caller can
// mutate the returned value without affecting the stored row:
// slices are copied, and so is each step's Data map.
func cloneReceipt(r Receipt) Receipt {
	out := r
	out.CausalChain = append([]ChainStep(nil), r.CausalChain...)
	// A step's Data is a map: copy it too, so a receipt handed to Save or
	// returned by Get never shares it with the stored one.
	// Its values are strings, bools and numbers.
	for i, st := range out.CausalChain {
		if st.Data != nil {
			d := make(map[string]any, len(st.Data))
			for k, v := range st.Data {
				d[k] = v
			}
			out.CausalChain[i].Data = d
		}
	}
	out.ComplianceMap = append([]ComplianceMapping(nil), r.ComplianceMap...)
	if r.Attestation != nil {
		att := *r.Attestation
		out.Attestation = &att
	}
	if r.RiskScore != nil {
		rs := *r.RiskScore
		out.RiskScore = &rs
	}
	if r.InsurerEnvelope != nil {
		ie := *r.InsurerEnvelope
		out.InsurerEnvelope = &ie
	}
	return out
}

// newerReceipt orders two receipts of one proposal as sqlGetReceiptByProposal
// does: later issue first, then a settled receipt, then the larger trace.
func newerReceipt(a, b Receipt) bool {
	if !a.IssuedAt.Equal(b.IssuedAt) {
		return a.IssuedAt.After(b.IssuedAt)
	}
	as, bs := strings.HasPrefix(a.TraceID, "settled-"), strings.HasPrefix(b.TraceID, "settled-")
	if as != bs {
		return as
	}
	return a.TraceID > b.TraceID
}
