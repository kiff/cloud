package posture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kiff/cloud/api/internal/tenant"
)

// newPostureID returns an opaque "pchg_<hex>" id for a posture record.
func newPostureID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "pchg_" + hex.EncodeToString(b[:])
}

// ChangeMode flips a tenant's governance mode and records the
// transition in the tenant's kiff_audit trail — atomically, in one
// transaction (gap G5).
//
// The flip (cloud_meta.tenants.metadata.mode) and the tamper-evident
// posture_changed record (kiff_t_<id>.kiff_audit) are written in the
// same transaction on a tenant-scoped connection, so the gate can
// never move posture without leaving the evidence, and the evidence
// can never claim a flip the gate didn't make. Both reads of the
// timeline (Control Room) and the gate's behavior stay consistent.
//
// from is the tenant's current effective mode; to is the requested
// one. When from == to it is a no-op: nothing is written and
// (Transition{}, false, nil) is returned — the caller persists the
// mode key separately if it wants the metadata made explicit.
//
// changedBy is the operator identity for the "by whom" line; it is
// stored both as the record's actor_id and in data.changed_by.
func ChangeMode(
	ctx context.Context,
	pool *tenant.SchemaPool,
	tenantID string,
	from, to tenant.Mode,
	changedBy string,
	now func() time.Time,
) (Transition, bool, error) {
	if pool == nil {
		return Transition{}, false, fmt.Errorf("posture: nil schema pool")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if from == to {
		return Transition{}, false, nil
	}

	tr := Transition{
		From:      string(from),
		To:        string(to),
		ChangedBy: changedBy,
		ChangedAt: now().UTC(),
	}

	data := map[string]any{
		dataFrom: tr.From,
		dataTo:   tr.To,
	}
	if changedBy != "" {
		data[dataChangedBy] = changedBy
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return Transition{}, false, fmt.Errorf("marshal posture data: %w", err)
	}

	modeJSON, err := json.Marshal(string(to))
	if err != nil {
		return Transition{}, false, fmt.Errorf("marshal mode: %w", err)
	}

	recID := newPostureID()
	message := fmt.Sprintf("governance posture changed from %s to %s", tr.From, tr.To)

	err = pool.WithTenantConn(ctx, tenantID, func(tx pgx.Tx) error {
		// 1. Flip the mode in cloud_meta.tenants. Schema-qualified, so
		//    it resolves regardless of the connection's pinned
		//    search_path. NULLIF guards the jsonb-null case: a freshly
		//    created tenant's metadata is the JSON value `null` (not
		//    SQL NULL), and `null || {...}` yields a JSON *array*, not a
		//    merged object — so coerce jsonb-null to '{}' before merging.
		const flipSQL = `
			UPDATE ` + tenant.MetaSchema + `.tenants
			SET metadata = COALESCE(NULLIF(metadata, 'null'::jsonb), '{}'::jsonb) || jsonb_build_object($1::text, $2::jsonb)
			WHERE id = $3
		`
		tag, err := tx.Exec(ctx, flipSQL, tenant.MetadataKeyMode, string(modeJSON), tenantID)
		if err != nil {
			return fmt.Errorf("flip tenant mode: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return tenant.ErrNotFound
		}

		// 2. Record the transition on the tenant's tamper-evident
		//    trail. Unqualified kiff_audit resolves to the tenant's
		//    schema via the pinned search_path. trace/correlation/
		//    causation are left NULL (a posture change is its own
		//    root event, not part of a decision trace).
		const recordSQL = `
			INSERT INTO kiff_audit
				(id, kind, entity_id, entity_type, actor_id, message, data, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`
		var actor any
		if changedBy != "" {
			actor = changedBy
		}
		if _, err := tx.Exec(ctx, recordSQL,
			recID, Kind, tenantID, EntityType, actor, message, dataJSON, tr.ChangedAt,
		); err != nil {
			return fmt.Errorf("record posture change: %w", err)
		}
		return nil
	})
	if err != nil {
		return Transition{}, false, err
	}
	return tr, true, nil
}
