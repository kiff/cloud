package attention

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// CardWarning creates informational work once. Marking it seen or resolved
// cannot change the Card, approve a hold, or authorize an action.
func (s *Store) CardWarning(ctx context.Context, tenant, eventID, cardID, reason string, at time.Time, resolved ...bool) error {
	return s.db.WithTenantConn(ctx, tenant, func(tx pgx.Tx) error {
		status := "open"
		if len(resolved) > 0 && resolved[0] {
			status = "resolved"
		}
		_, err := tx.Exec(ctx, `INSERT INTO cloud_attention(id,kind,source_id,action,entity_id,reason,status,created_at)
		VALUES($1,'card_warning',$2,'card_budget',$2,$3,$5,$4) ON CONFLICT(id) DO UPDATE SET status='resolved',revision=cloud_attention.revision+1,updated_at=now() WHERE EXCLUDED.status='resolved' AND cloud_attention.status<>'resolved'`, "card-warning:"+eventID, cardID, reason, at, status)
		return err
	})
}
