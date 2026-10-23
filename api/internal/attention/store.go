package attention

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// TenantDB pins search_path inside each transaction. Callers must provide a
// tenant from verified authentication, never a tenant supplied in a request.
type TenantDB interface {
	WithTenantConn(context.Context, string, func(pgx.Tx) error) error
}
type Store struct{ db TenantDB }

func NewStore(db TenantDB) *Store { return &Store{db: db} }

// Item is work; Unread is personal delivery state and cannot resolve it.
type Item struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	SourceID  string    `json:"source_id"`
	DomainID  string    `json:"domain_id"`
	ActorID   string    `json:"actor_id"`
	Action    string    `json:"action"`
	EntityID  string    `json:"entity_id"`
	Reason    string    `json:"reason"`
	Status    string    `json:"status"`
	Assignee  string    `json:"assignee"`
	CreatedAt time.Time `json:"created_at"`
	Revision  int64     `json:"revision"`
	Unread    bool      `json:"unread"`
}
type Feed struct {
	Failed    int    `json:"failed"`
	Pending   int    `json:"pending"`
	Items     []Item `json:"items"`
	Open      int    `json:"open"`
	Unread    int    `json:"unread"`
	Truncated bool   `json:"truncated"`
}

var ErrConflict = errors.New("attention item changed, missing, or action not permitted")

func (s *Store) Migrate(ctx context.Context, tenant string) error {
	return s.db.WithTenantConn(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout='3s'; SET LOCAL statement_timeout='30s'"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, SchemaSQL())
		return err
	})
}

func (s *Store) List(ctx context.Context, tenant, subject string, limit, offset int, unreadOnly bool) (Feed, error) {
	return s.ListKind(ctx, tenant, subject, limit, offset, unreadOnly, "")
}

func (s *Store) ListKind(ctx context.Context, tenant, subject string, limit, offset int, unreadOnly bool, kind string) (Feed, error) {
	f := Feed{Items: []Item{}}
	if offset < 0 {
		offset = 0
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	err := s.db.WithTenantConn(ctx, tenant, func(tx pgx.Tx) error {
		// One snapshot: counts and rows cannot disagree because work changed midway.
		if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER(WHERE COALESCE(s.revision,0)<a.revision)
   FROM cloud_attention a LEFT JOIN cloud_attention_seen s ON s.item_id=a.id AND s.subject=$1
   WHERE a.status='open' AND ($2='' OR a.kind=$2)`, subject, kind).Scan(&f.Open, &f.Unread); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE attempts>=8),count(*) FILTER(WHERE attempts<8) FROM cloud_attention_jobs WHERE consumer='attention' AND completed_at IS NULL`).Scan(&f.Failed, &f.Pending); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT a.id,a.kind,a.source_id,a.domain_id,a.actor_id,a.action,a.entity_id,
   a.reason,a.status,a.assignee,a.created_at,a.revision,COALESCE(s.revision,0)<a.revision
   FROM cloud_attention a LEFT JOIN cloud_attention_seen s ON s.item_id=a.id AND s.subject=$1
   WHERE a.status='open' AND ($5='' OR a.kind=$5) AND (NOT $4 OR COALESCE(s.revision,0)<a.revision) ORDER BY CASE WHEN a.kind='approval' THEN 0 ELSE 1 END,a.created_at,a.id LIMIT $2 OFFSET $3`, subject, limit, offset, unreadOnly, kind)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var i Item
			if err := rows.Scan(&i.ID, &i.Kind, &i.SourceID, &i.DomainID, &i.ActorID, &i.Action, &i.EntityID, &i.Reason, &i.Status, &i.Assignee, &i.CreatedAt, &i.Revision, &i.Unread); err != nil {
				return err
			}
			f.Items = append(f.Items, i)
		}
		total := f.Open
		if unreadOnly {
			total = f.Unread
		}
		f.Truncated = total > offset+len(f.Items)
		return rows.Err()
	})
	return f, err
}

// MarkSeen acknowledges exactly the revisions the caller rendered. A concurrent
// new event or later revision stays unread. Neither assignment nor work changes.
func (s *Store) MarkSeen(ctx context.Context, tenant, subject string, items map[string]int64) error {
	if len(items) > 200 {
		return errors.New("at most 200 items may be acknowledged")
	}
	return s.db.WithTenantConn(ctx, tenant, func(tx pgx.Tx) error {
		for id, rev := range items {
			if rev < 1 {
				return errors.New("invalid revision")
			}
			_, err := tx.Exec(ctx, `INSERT INTO cloud_attention_seen(subject,item_id,revision)
    SELECT $1,id,$3 FROM cloud_attention WHERE id=$2 AND revision >= $3
    ON CONFLICT(subject,item_id) DO UPDATE SET revision=GREATEST(cloud_attention_seen.revision,EXCLUDED.revision)`, subject, id, rev)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// Act uses optimistic concurrency. Only the current assignee can release or
// resolve a blocked item. Approvals are resolved only by their canonical store.
func (s *Store) Act(ctx context.Context, tenant, subject, id, action string, revision int64) error {
	return s.db.WithTenantConn(ctx, tenant, func(tx pgx.Tx) error {
		var sql string
		switch action {
		case "claim":
			sql = `UPDATE cloud_attention SET assignee=$2,revision=revision+1,updated_at=now() WHERE id=$1 AND status='open' AND assignee='' AND revision=$3`
		case "release":
			sql = `UPDATE cloud_attention SET assignee='',revision=revision+1,updated_at=now() WHERE id=$1 AND status='open' AND assignee=$2 AND revision=$3`
		case "resolve":
			sql = `UPDATE cloud_attention SET status='resolved',resolved_by=$2,revision=revision+1,updated_at=now() WHERE id=$1 AND status='open' AND kind IN ('blocked','card_warning') AND assignee=$2 AND revision=$3`
		default:
			return ErrConflict
		}
		tag, err := tx.Exec(ctx, sql, id, subject, revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO cloud_attention_history(item_id,actor,action,revision) VALUES($1,$2,$3,$4)`, id, subject, action, revision+1)
		return err
	})
}

// Process projects a bounded batch. Each job's projection and acknowledgement
// commit together; failed jobs roll back to a savepoint and retain retry state.
// Event IDs are used for ordering, never as a high-water cursor: a transaction
// with a lower sequence can commit after another transaction has been consumed.
func (s *Store) Process(ctx context.Context, tenant string) error {
	return s.db.WithTenantConn(ctx, tenant, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "attention:"+tenant).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT event_id FROM cloud_attention_jobs WHERE consumer='attention' AND completed_at IS NULL AND attempts<8 AND available_at<=now() ORDER BY event_id LIMIT 100 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			sub, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			// A newer, materially different decision for the same proposal
			// updates the item, reopens it, and bumps the revision so anyone
			// who saw the old decision sees this one as unread. Identical
			// content or an older decision (a late retry) changes nothing.
			// An owner's answer to a hold, queued before the capture
			// trigger skipped them, opens nothing.
			_, err = sub.Exec(ctx, `INSERT INTO cloud_attention(id,kind,source_id,domain_id,actor_id,action,entity_id,reason,status,created_at,decided_at)
    SELECT 'decision:'||(payload->>'source_id'),'blocked',payload->>'source_id',payload->>'domain_id',payload->>'actor_id',payload->>'action',payload->>'entity_id',payload->>'reason','open',(payload->>'created_at')::timestamptz,(payload->>'created_at')::timestamptz
    FROM cloud_attention_events WHERE id=$1
     AND COALESCE(payload->>'reason','') NOT IN ('["mandate_owner_rejected"]','["mandate_owner_expired"]')
    ON CONFLICT(id) DO UPDATE SET domain_id=EXCLUDED.domain_id,actor_id=EXCLUDED.actor_id,action=EXCLUDED.action,
     entity_id=EXCLUDED.entity_id,reason=EXCLUDED.reason,decided_at=EXCLUDED.decided_at,
     status='open',resolved_by='',revision=cloud_attention.revision+1,updated_at=now()
    WHERE EXCLUDED.decided_at >= COALESCE(cloud_attention.decided_at,cloud_attention.created_at)
     AND (cloud_attention.domain_id,cloud_attention.actor_id,cloud_attention.action,cloud_attention.entity_id,cloud_attention.reason)
      IS DISTINCT FROM (EXCLUDED.domain_id,EXCLUDED.actor_id,EXCLUDED.action,EXCLUDED.entity_id,EXCLUDED.reason)`, id)
			if err != nil {
				if rollbackErr := sub.Rollback(ctx); rollbackErr != nil {
					return rollbackErr
				}
				_, writeErr := tx.Exec(ctx, `UPDATE cloud_attention_jobs SET attempts=attempts+1,last_error=$2,available_at=now()+make_interval(secs=>LEAST(300,power(2,attempts+1))::int) WHERE event_id=$1 AND consumer='attention'`, id, fmt.Sprintf("projection failed: %v", err))
				if writeErr != nil {
					return writeErr
				}
				continue
			}
			if err := sub.Commit(ctx); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE cloud_attention_jobs SET completed_at=now(),attempts=attempts+1,last_error='' WHERE event_id=$1 AND consumer='attention'`, id); err != nil {
				return err
			}
		}
		return nil
	})
}
