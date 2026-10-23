package mandate

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) PendingWarnings(ctx context.Context) ([]CardWarning, error) {
	rows, err := s.pool.Query(ctx, `SELECT w.tenant_id,w.body,w.resolved_at FROM cloud_meta.mandate_warnings w
 WHERE w.delivery_available_at<=now() AND (w.delivery_lease_until IS NULL OR w.delivery_lease_until<=now()) AND (
 w.attention_dirty OR w.recipients IS NULL OR EXISTS(
 SELECT 1 FROM cloud_meta.mandate_warning_deliveries d WHERE d.tenant_id=w.tenant_id AND d.event_id=w.id AND d.delivered_at IS NULL AND d.cancelled_at IS NULL AND d.available_at<=now()))
 ORDER BY w.recorded_at,w.id LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CardWarning{}
	for rows.Next() {
		var tenant string
		var raw []byte
		var w CardWarning
		if err = rows.Scan(&tenant, &raw, &w.ResolvedAt); err != nil {
			return nil, err
		}
		resolvedAt := w.ResolvedAt
		if err = json.Unmarshal(raw, &w); err != nil {
			return nil, err
		}
		w.ResolvedAt, w.TenantID = resolvedAt, tenant
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeliverWarning claims an event in one committed statement. Third-party
// calls hold no transaction, row lock or connection. Each result is committed
// independently, so a later failure cannot roll back an earlier successful send.
// The lease is longer than the worker's bounded pass; a crash permits replay.
func (s *PostgresStore) DeliverWarning(ctx context.Context, w CardWarning,
	attention func(context.Context, CardWarning) error,
	recipients func(context.Context, CardWarning) ([]string, error),
	send func(context.Context, CardWarning, string) error) (returnErr error) {
	ctx, cancelEvent := context.WithTimeout(ctx, 30*time.Second)
	defer cancelEvent()
	var raw []byte
	var dirty bool
	var attempt int64
	err := s.pool.QueryRow(ctx, `UPDATE cloud_meta.mandate_warnings
 SET delivery_lease_until=now()+interval '2 minutes',delivery_attempts=delivery_attempts+1
 WHERE tenant_id=$1 AND id=$2 AND delivery_available_at<=now()
 AND (delivery_lease_until IS NULL OR delivery_lease_until<=now())
 RETURNING recipients,attention_dirty,resolved_at,delivery_attempts`, w.TenantID, w.ID).Scan(&raw, &dirty, &w.ResolvedAt, &attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		// Persist backoff/release even when a send used up the pass deadline.
		finishCtx, cancel := warningRecordContext(ctx)
		defer cancel()
		message := ""
		if returnErr != nil {
			message = returnErr.Error()
		}
		_, err := s.pool.Exec(finishCtx, `UPDATE cloud_meta.mandate_warnings
   SET delivery_lease_until=NULL,delivery_error=$4,
   delivery_available_at=CASE WHEN $4='' THEN now() ELSE now()+make_interval(secs=>LEAST(300,30*delivery_attempts)::integer) END
   WHERE tenant_id=$1 AND id=$2 AND delivery_attempts=$3`, w.TenantID, w.ID, attempt, message)
		returnErr = errors.Join(returnErr, err)
	}()
	if dirty {
		if err = attention(ctx, w); err != nil {
			return err
		}
		// Recovery may have happened while the projection ran. Leave it dirty in
		// that case, so its resolved projection is delivered on the next pass.
		if _, err = s.pool.Exec(ctx, `UPDATE cloud_meta.mandate_warnings
   SET attention_delivered_at=now(),attention_dirty=false
   WHERE tenant_id=$1 AND id=$2 AND resolved_at IS NOT DISTINCT FROM $3::timestamptz`, w.TenantID, w.ID, w.ResolvedAt); err != nil {
			return err
		}
	}
	if w.ResolvedAt != nil {
		if _, err = s.pool.Exec(ctx, `UPDATE cloud_meta.mandate_warning_deliveries SET cancelled_at=now(),last_error='warning recovered before delivery' WHERE tenant_id=$1 AND event_id=$2 AND delivered_at IS NULL AND cancelled_at IS NULL`, w.TenantID, w.ID); err != nil {
			return err
		}
		_, err = s.pool.Exec(ctx, `UPDATE cloud_meta.mandate_warnings SET recipients=COALESCE(recipients,'[]'::jsonb) WHERE tenant_id=$1 AND id=$2`, w.TenantID, w.ID)
		return err
	}
	if raw == nil {
		destinations, err := recipients(ctx, w)
		if err != nil {
			return err
		}
		if destinations == nil {
			destinations = []string{}
		}
		raw, err = json.Marshal(destinations)
		if err != nil {
			return err
		}
		// Only the snapshot and its destination rows need to commit together.
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err = tx.Exec(ctx, `UPDATE cloud_meta.mandate_warnings SET recipients=$3 WHERE tenant_id=$1 AND id=$2`, w.TenantID, w.ID, raw); err != nil {
			return err
		}
		for _, destination := range destinations {
			if _, err = tx.Exec(ctx, `INSERT INTO cloud_meta.mandate_warning_deliveries(tenant_id,event_id,destination) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, w.TenantID, w.ID, destination); err != nil {
				return err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT destination FROM cloud_meta.mandate_warning_deliveries WHERE tenant_id=$1 AND event_id=$2 AND delivered_at IS NULL AND cancelled_at IS NULL AND available_at<=now() ORDER BY destination`, w.TenantID, w.ID)
	if err != nil {
		return err
	}
	var destinations []string
	for rows.Next() {
		var d string
		if err = rows.Scan(&d); err != nil {
			rows.Close()
			return err
		}
		destinations = append(destinations, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, destination := range destinations {
		if err = ctx.Err(); err != nil {
			return err
		}
		// Do not start a new send for a warning that recovered during this pass.
		var resolved bool
		if err = s.pool.QueryRow(ctx, `SELECT resolved_at IS NOT NULL FROM cloud_meta.mandate_warnings WHERE tenant_id=$1 AND id=$2`, w.TenantID, w.ID).Scan(&resolved); err != nil {
			return err
		}
		if resolved {
			return nil
		}
		sendCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		sendErr := send(sendCtx, w, destination)
		cancel()
		recordCtx, recordCancel := warningRecordContext(ctx)
		if sendErr != nil {
			_, err = s.pool.Exec(recordCtx, `UPDATE cloud_meta.mandate_warning_deliveries SET attempts=attempts+1,last_error=$4,available_at=now()+make_interval(secs=>LEAST(3600,30*(attempts+1))) WHERE tenant_id=$1 AND event_id=$2 AND destination=$3`, w.TenantID, w.ID, destination, sendErr.Error())
		} else {
			_, err = s.pool.Exec(recordCtx, `UPDATE cloud_meta.mandate_warning_deliveries SET delivered_at=now(),last_error='' WHERE tenant_id=$1 AND event_id=$2 AND destination=$3`, w.TenantID, w.ID, destination)
		}
		recordCancel()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func warningRecordContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}
