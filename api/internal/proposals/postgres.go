package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// PostgresReader reads proposal-decision rows from the per-tenant
// kiff_t_<id>.kiff_audit table and enriches each row with the
// matching cloud_meta.receipts row when one exists.
//
// Tenant safety:
//
//   - The kiff_audit query checks out a connection from
//     tenant.SchemaPool whose AfterConnect hook pins
//     search_path = kiff_t_<tenantID>, public. A query against
//     "kiff_audit" without a schema qualifier therefore reads
//     only the caller tenant's rows. Cross-tenant reads are
//     mechanically impossible at this layer.
//   - The receipt enrichment goes through receipt.Store.Get,
//     which keys on (tenant_id, trace_id) and returns
//     ErrReceiptNotFound when the row belongs to a different
//     tenant — the lookup never returns another tenant's row.
//
// Why we don't use the framework's audit.Store.Query:
//
//   - Filter accepts only one Kind at a time, but the proposal
//     gate writes two kinds (proposal_decision /
//     proposal_rejected). Two round-trips per request is
//     wasteful when both come from the same table.
//   - Query returns ASC by inserted_at with no LIMIT. A real
//     dashboard read needs DESC + LIMIT server-side; doing it in
//     Go after a full scan defeats the indexed read.
//   - The reader's row shape is custom (it composes audit fields
//     with JSONB extraction); the framework's audit.Record
//     surface doesn't reflect that shape.
//
// Direct SQL through the per-tenant pool is the simpler, faster,
// and (because it's parameterized) equally safe path.
type PostgresReader struct {
	pool     *tenant.SchemaPool
	receipts receipt.Store
}

// NewPostgresReader wraps the per-tenant schema pool and the
// cloud-side receipt store. Both are required; nil for either
// returns nil so callers can branch on "reader available."
func NewPostgresReader(pool *tenant.SchemaPool, receipts receipt.Store) *PostgresReader {
	if pool == nil || receipts == nil {
		return nil
	}
	return &PostgresReader{pool: pool, receipts: receipts}
}

// listDecisionsSQL is the read query. Selects every column
// DecisionRow needs from kiff_audit, narrowed to the two kinds
// the proposal gate writes, ordered DESC by created_at, and
// limited so over-large result sets cannot fan out memory.
//
// Note: there is no explicit tenant_id predicate because the
// connection's search_path is already pinned to kiff_t_<id>;
// the table name "kiff_audit" resolves to that tenant's schema
// only. A second tenant's data is mechanically unreachable.
const listDecisionsSQL = `
	SELECT id, kind, entity_id, entity_type, actor_id,
	       data, trace_id, created_at
	FROM kiff_audit
	WHERE kind IN ('proposal_decision', 'proposal_rejected')
	ORDER BY created_at DESC
	LIMIT $1
`

// ListDecisions implements Reader.
func (r *PostgresReader) ListDecisions(ctx context.Context, tenantID string, limit int) ([]DecisionRow, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	conn, err := r.pool.Acquire(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("proposals: acquire tenant pool: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, listDecisionsSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("proposals: list decisions: %w", err)
	}

	out, err := scanDecisionRows(rows)
	if err != nil {
		return nil, err
	}
	if err := r.enrichReceipts(ctx, tenantID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// buildDecisionPredicates turns a DecisionQuery into the WHERE
// clause fragments + bound args shared by the windowed list and
// count queries. Every value is a bound parameter (never
// interpolated), so the query stays injection-safe. Returns the
// joined predicate string and the args in $1.. order.
func buildDecisionPredicates(q DecisionQuery) (string, []any) {
	clauses := []string{"kind IN ('proposal_decision', 'proposal_rejected')"}
	args := []any{}
	add := func(clause string, val any) {
		args = append(args, val)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if q.Action != "" {
		add("data->>'action' = $%d", q.Action)
	}
	if q.ControlID != "" {
		add("data->>'control_id' = $%d", q.ControlID)
	}
	if q.Environment != "" {
		add("data->>'environment' = $%d", q.Environment)
	}
	if q.Actor != "" {
		add("actor_id = $%d", q.Actor)
	}
	if !q.From.IsZero() {
		add("created_at >= $%d", q.From)
	}
	if !q.To.IsZero() {
		add("created_at <= $%d", q.To)
	}
	if len(q.Outcomes) > 0 {
		placeholders := make([]string, len(q.Outcomes))
		for i, o := range q.Outcomes {
			args = append(args, o)
			placeholders[i] = fmt.Sprintf("$%d", len(args))
		}
		clauses = append(clauses, "data->>'outcome' IN ("+strings.Join(placeholders, ", ")+")")
	}
	return strings.Join(clauses, " AND "), args
}

// ListDecisionsInWindow implements Reader. Builds a window-native,
// action/control/environment-scoped query so the predicates run in
// SQL, not in Go after a tenant-wide page. Tenant safety is
// unchanged: the connection's search_path is pinned to the tenant
// schema, so "kiff_audit" resolves only to that tenant's rows.
func (r *PostgresReader) ListDecisionsInWindow(ctx context.Context, tenantID string, q DecisionQuery) ([]DecisionRow, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	where, args := buildDecisionPredicates(q)
	args = append(args, limit)
	limitPlaceholder := len(args)
	offsetClause := ""
	if q.Offset > 0 {
		args = append(args, q.Offset)
		offsetClause = fmt.Sprintf(" OFFSET $%d", len(args))
	}
	query := fmt.Sprintf(`
		SELECT id, kind, entity_id, entity_type, actor_id,
		       data, trace_id, created_at
		FROM kiff_audit
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d%s
	`, where, limitPlaceholder, offsetClause)

	conn, err := r.pool.Acquire(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("proposals: acquire tenant pool: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("proposals: list decisions in window: %w", err)
	}
	out, err := scanDecisionRows(rows)
	if err != nil {
		return nil, err
	}
	if err := r.enrichReceipts(ctx, tenantID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// CountDecisionsByOutcome implements Reader. Counts over the full
// window+control via COUNT(*) ... GROUP BY data->>'outcome',
// independent of any row limit, so the Control Room header and the
// proof pack's counts can't under-report a busy window.
func (r *PostgresReader) CountDecisionsByOutcome(ctx context.Context, tenantID string, q DecisionQuery) (map[string]int, int, error) {
	where, args := buildDecisionPredicates(q)
	query := fmt.Sprintf(`
		SELECT data->>'outcome' AS outcome, COUNT(*)
		FROM kiff_audit
		WHERE %s
		GROUP BY data->>'outcome'
	`, where)

	conn, err := r.pool.Acquire(ctx, tenantID)
	if err != nil {
		return nil, 0, fmt.Errorf("proposals: acquire tenant pool: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("proposals: count decisions by outcome: %w", err)
	}
	defer rows.Close()

	counts := map[string]int{}
	total := 0
	for rows.Next() {
		var (
			outcome *string
			n       int
		)
		if err := rows.Scan(&outcome, &n); err != nil {
			return nil, 0, fmt.Errorf("proposals: scan count: %w", err)
		}
		key := ""
		if outcome != nil {
			key = *outcome
		}
		counts[key] = n
		total += n
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("proposals: iterate counts: %w", err)
	}
	return counts, total, nil
}

// CountDecisionsOverTime implements Reader. Buckets the windowed
// decisions into fixed-width intervals via
// to_timestamp(floor(epoch/width)*width) — PG12-safe, no date_bin —
// and groups per (bucket, outcome). Returns only non-empty buckets in
// ascending time order; the caller fills gaps from From/To+bucket.
func (r *PostgresReader) CountDecisionsOverTime(ctx context.Context, tenantID string, q DecisionQuery, bucket time.Duration) ([]TimeBucket, error) {
	secs := int64(bucket / time.Second)
	if secs <= 0 {
		secs = 1
	}
	where, args := buildDecisionPredicates(q)
	args = append(args, secs)
	bucketArg := len(args)
	query := fmt.Sprintf(`
		SELECT to_timestamp(floor(extract(epoch from created_at) / $%d) * $%d) AT TIME ZONE 'UTC' AS bucket,
		       data->>'outcome' AS outcome,
		       COUNT(*)
		FROM kiff_audit
		WHERE %s
		GROUP BY bucket, data->>'outcome'
		ORDER BY bucket ASC
	`, bucketArg, bucketArg, where)

	conn, err := r.pool.Acquire(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("proposals: acquire tenant pool: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("proposals: count decisions over time: %w", err)
	}
	defer rows.Close()

	// Accumulate into an ordered slice, coalescing outcome rows that
	// share a bucket start.
	byStart := map[time.Time]*TimeBucket{}
	order := []time.Time{}
	for rows.Next() {
		var (
			start   time.Time
			outcome *string
			n       int
		)
		if err := rows.Scan(&start, &outcome, &n); err != nil {
			return nil, fmt.Errorf("proposals: scan time bucket: %w", err)
		}
		start = start.UTC()
		tb, ok := byStart[start]
		if !ok {
			tb = &TimeBucket{Start: start, Counts: map[string]int{}}
			byStart[start] = tb
			order = append(order, start)
		}
		key := ""
		if outcome != nil {
			key = *outcome
		}
		tb.Counts[key] += n
		tb.Total += n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("proposals: iterate time buckets: %w", err)
	}
	out := make([]TimeBucket, 0, len(order))
	for _, s := range order {
		out = append(out, *byStart[s])
	}
	return out, nil
}

// scanDecisionRows reads the kiff_audit result set into DecisionRows,
// decoding the JSONB data column into the gate-written fields and
// dropping everything else (the read-side gate that keeps the wire
// shape from leaking parameters/reasoning). Closes rows on error.
func scanDecisionRows(rows pgx.Rows) ([]DecisionRow, error) {
	out := make([]DecisionRow, 0)
	for rows.Next() {
		var (
			row       DecisionRow
			kind      string
			actorID   *string
			dataBytes []byte
			traceID   *string
		)
		if err := rows.Scan(
			&row.ProposalID,
			&kind,
			&row.EntityID,
			&row.EntityType,
			&actorID,
			&dataBytes,
			&traceID,
			&row.CreatedAt,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("proposals: scan row: %w", err)
		}
		row, err := decodeDecisionRow(row, kind, actorID, dataBytes, traceID)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("proposals: iterate rows: %w", err)
	}
	rows.Close()
	return out, nil
}

// decodeDecisionRow fills a scanned audit row's decision fields from its
// data column: the proposal id (data's, else the trace id), action,
// outcome, reasons and attribution.
func decodeDecisionRow(row DecisionRow, kind string, actorID *string, dataBytes []byte, traceID *string) (DecisionRow, error) {
	_ = kind
	if actorID != nil {
		row.ActorID = *actorID
	}
	if len(dataBytes) > 0 {
		var d auditDecisionData
		if err := json.Unmarshal(dataBytes, &d); err != nil {
			return row, fmt.Errorf("proposals: decode data jsonb for %s: %w", row.ProposalID, err)
		}
		if d.ProposalID != "" {
			row.ProposalID = d.ProposalID
		} else if traceID != nil && *traceID != "" {
			row.ProposalID = *traceID
		}
		row.ActionName = d.Action
		row.Outcome = d.Outcome
		row.Reasons = d.Reasons
		row.PrincipalSubject = d.PrincipalSubject
		row.PrincipalAPIKeyID = d.PrincipalAPIKeyID
		row.ActorVerified = d.ActorVerified
		row.ControlID = d.ControlID
		row.Environment = d.Environment
		row.ControlVersion = d.ControlVersion
		row.StateAtDecision = d.StateAtDecision
		row.AllowedStates = d.AllowedStates
		if d.Amount != nil && d.Amount.Value != "" {
			row.Amount = d.Amount
		}
		row.OnArg = d.OnArg
		row.OnHash = d.OnHash
		row.TrialDraft = d.TrialDraft
	} else if traceID != nil && *traceID != "" {
		row.ProposalID = *traceID
	}
	return row, nil
}

// enrichReceipts attaches receipt linkage/status to each row via the
// receipt store. It resolves by the explicit decision->receipt link
// first (GetByProposalID), falling back to the legacy
// proposal_id == trace_id coincidence (Get) for receipts written
// before G2 stamped proposal_id. One lookup per row is fine at v0.1
// limits (<= MaxLimit rows); a future slice can batch.
func (r *PostgresReader) enrichReceipts(ctx context.Context, tenantID string, out []DecisionRow) error {
	for i := range out {
		rec, err := r.receipts.GetByProposalID(ctx, tenantID, out[i].ProposalID)
		if errors.Is(err, receipt.ErrReceiptNotFound) {
			// Legacy fallback: pre-G2 receipts have no proposal_id, but
			// the gate wrote prop.ID into trace_id, so a by-trace lookup
			// still resolves them.
			rec, err = r.receipts.Get(ctx, tenantID, out[i].ProposalID)
		}
		if err != nil {
			if errors.Is(err, receipt.ErrReceiptNotFound) {
				out[i].ReceiptStatus = "none"
				continue
			}
			return fmt.Errorf("proposals: lookup receipt for %s: %w", out[i].ProposalID, err)
		}
		out[i].ReceiptTraceID = rec.TraceID
		out[i].ReceiptSigned = rec.Attestation != nil && rec.Attestation.Signature != ""
		if rec.Attestation != nil && rec.Attestation.AnchorTxHash != "" {
			out[i].ReceiptStatus = "anchored"
			out[i].AnchorTxHash = rec.Attestation.AnchorTxHash
		} else {
			out[i].ReceiptStatus = "pending"
		}
	}
	return nil
}

// auditDecisionData is the local projection of kiff_audit.data
// the proposal gate writes via attributionData(). Fields not
// listed here are intentionally dropped: this is the read-side
// gate that prevents future writes to the data column from
// silently leaking through to the wire.
//
// Specifically, neither the request `parameters` map nor the
// caller-supplied `reasoning_summary` are present in the gate's
// attributionData() today. If a future change adds either to
// kiff_audit.data, this struct's silence will keep them off the
// /v1/proposals response. A test in proposals_list_test.go
// asserts the wire body never contains either token.
type auditDecisionData struct {
	ProposalID        string   `json:"proposal_id"`
	Action            string   `json:"action"`
	Outcome           string   `json:"outcome"`
	Reasons           []string `json:"reasons"`
	PrincipalSubject  string   `json:"principal_subject"`
	PrincipalAPIKeyID string   `json:"principal_api_key_id"`
	ActorVerified     bool     `json:"actor_verified"`
	ControlID         string   `json:"control_id"`
	Environment       string   `json:"environment"`
	ControlVersion    int      `json:"control_version"`
	StateAtDecision   string   `json:"state_at_decision"`
	AllowedStates     []string `json:"allowed_states"`
	// kiff/tafs#167: what the call moves and a hash of what it is on.
	Amount *CallAmount `json:"amount"`
	OnArg  string      `json:"on_arg"`
	OnHash string      `json:"on_hash"`
	// kiff/tafs#173: the draft whose trial made the call.
	TrialDraft string `json:"trial_draft"`
}
