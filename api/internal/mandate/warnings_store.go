package mandate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrWarningConflict = errors.New("warning settings changed; reload the Card")

type WarningStore interface {
	GetWarningConfig(context.Context, string, string) (WarningConfig, error)
	SetWarningConfig(context.Context, string, string, string, int64, WarningSettings) (WarningConfig, error)
	CardWarnings(context.Context, string, string) ([]CardWarning, error)
}

func readWarningConfig(ctx context.Context, q querier, tenant, id string, at time.Time) (WarningConfig, error) {
	c := WarningConfig{Settings: DefaultWarningSettings()}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT revision,changed_at,changed_by,settings FROM cloud_meta.mandate_warning_settings
	WHERE tenant_id=$1 AND mandate_id=$2 AND changed_at<=$3 ORDER BY revision DESC LIMIT 1`, tenant, id, at).Scan(&c.Revision, &c.At, &c.By, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c.Settings)
	return c, err
}

func (s *PostgresStore) GetWarningConfig(ctx context.Context, tenant, id string) (WarningConfig, error) {
	return readWarningConfig(ctx, s.pool, tenant, id, time.Now().UTC())
}

func warningLock(ctx context.Context, q querier, tenant, id string) error {
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "card-warning:"+tenant+":"+id)
	return err
}

func (s *PostgresStore) SetWarningConfig(ctx context.Context, tenant, id, by string, expected int64, settings WarningSettings) (WarningConfig, error) {
	if err := settings.Validate(); err != nil {
		return WarningConfig{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WarningConfig{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = warningLock(ctx, tx, tenant, id); err != nil {
		return WarningConfig{}, err
	}
	c, err := readWarningConfig(ctx, tx, tenant, id, time.Now().UTC())
	if err != nil {
		return c, err
	}
	if c.Revision != expected {
		return c, ErrWarningConflict
	}
	c.Revision++
	c.Settings = settings
	c.By = by
	raw, err := json.Marshal(settings)
	if err != nil {
		return c, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO cloud_meta.mandate_warning_settings(tenant_id,mandate_id,revision,changed_by,settings)
	VALUES($1,$2,$3,$4,$5) RETURNING changed_at`, tenant, id, c.Revision, by, raw).Scan(&c.At)
	if err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}

func (s *PostgresStore) CardWarnings(ctx context.Context, tenant, id string) ([]CardWarning, error) {
	rows, err := s.pool.Query(ctx, `SELECT body,resolved_at FROM cloud_meta.mandate_warnings WHERE tenant_id=$1 AND mandate_id=$2 ORDER BY recorded_at DESC,id LIMIT 100`, tenant, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CardWarning{}
	for rows.Next() {
		var raw []byte
		var w CardWarning
		if err = rows.Scan(&raw, &w.ResolvedAt); err != nil {
			return nil, err
		}
		resolvedAt := w.ResolvedAt
		if err = json.Unmarshal(raw, &w); err != nil {
			return nil, err
		}
		w.ResolvedAt = resolvedAt
		w.TenantID = tenant
		out = append(out, w)
	}
	return out, rows.Err()
}

// PG card columns are snake_case; authored grants/aggregates retain their
// existing JSON representation. No public request is decoded through this.
func warningCard(raw []byte, tenant, id string) (Mandate, error) {
	var row struct {
		Subject    string
		Domain     string
		Location   string
		Aggregates []Aggregate
		Grants     []Grant
		ValidFrom  *time.Time `json:"valid_from"`
		ValidUntil *time.Time `json:"valid_until"`
		RevokedAt  *time.Time `json:"revoked_at"`
		ArchivedAt *time.Time `json:"archived_at"`
	}
	err := json.Unmarshal(raw, &row)
	m := Mandate{ID: id, TenantID: tenant, Subject: row.Subject, Domain: row.Domain, Location: row.Location, Aggregates: row.Aggregates, Grants: row.Grants, RevokedAt: row.RevokedAt, ArchivedAt: row.ArchivedAt}
	if row.ValidFrom != nil {
		m.ValidFrom = *row.ValidFrom
	}
	if row.ValidUntil != nil {
		m.ValidUntil = *row.ValidUntil
	}
	return m, err
}

// ProcessWarningWork is post-commit. Capture is atomic with a draw/release;
// each historical observation is retained, including transient exhaustion.
// Retryable jobs and persistent episode state survive process restarts.
func (s *PostgresStore) ProcessWarningWork(ctx context.Context) error {
	for i := 0; i < 20; i++ {
		done, err := s.processWarningWork(ctx)
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
	}
	return nil
}

func (s *PostgresStore) processWarningWork(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var job int64
	var tenant, id string
	var at time.Time
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT w.id,w.tenant_id,w.mandate_id,w.observed_at,w.card FROM cloud_meta.mandate_warning_work w
	WHERE w.completed_at IS NULL AND w.available_at<=now()
	AND NOT EXISTS(SELECT 1 FROM cloud_meta.mandate_warning_work old WHERE old.tenant_id=w.tenant_id AND old.mandate_id=w.mandate_id AND old.completed_at IS NULL AND old.id<w.id)
	ORDER BY w.id LIMIT 1 FOR UPDATE OF w SKIP LOCKED`).Scan(&job, &tenant, &id, &at, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = warningLock(ctx, tx, tenant, id); err != nil {
		return false, err
	}
	if err = s.evaluateWarningJob(ctx, tx, tenant, id, at, raw); err != nil {
		_ = tx.Rollback(ctx)
		// Keep every failed observation; do not mark it completed or zero usage.
		_, saveErr := s.pool.Exec(ctx, `UPDATE cloud_meta.mandate_warning_work SET attempts=attempts+1,last_error=$2,
		available_at=now()+make_interval(secs=>LEAST(300,5*(attempts+1))) WHERE id=$1`, job, err.Error())
		if saveErr != nil {
			return false, saveErr
		}
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE cloud_meta.mandate_warning_work SET completed_at=now(),last_error='' WHERE id=$1`, job)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s *PostgresStore) evaluateWarningJob(ctx context.Context, tx pgx.Tx, tenant, id string, at time.Time, raw []byte) error {
	m, err := warningCard(raw, tenant, id)
	if err != nil {
		return err
	}
	if !m.Active(at) || m.ArchivedAt != nil {
		_, err = tx.Exec(ctx, `UPDATE cloud_meta.mandate_warnings SET resolved_at=$3,attention_dirty=true WHERE tenant_id=$1 AND mandate_id=$2 AND resolved_at IS NULL`, tenant, id, at)
		return err
	}
	h, err := readWarningHistory(ctx, tx, tenant, id, at.Add(-8*24*time.Hour-time.Hour), at)
	if errors.Is(err, ErrWarningHistory) {
		// A bounded forecast can be unavailable on a busy Card; spent/burst
		// notices still use the complete authoritative SQL balance.
		h = WarningHistory{Limited: true, Balances: map[string]LimitBalance{}, FirstDraws: map[string]time.Time{}}
		if err = tx.QueryRow(ctx, `SELECT min(changed_at) FROM cloud_meta.mandate_revisions WHERE tenant_id=$1 AND mandate_id=$2`, tenant, id).Scan(&h.IssuedAt); err != nil {
			return err
		}
		for _, a := range m.Aggregates {
			b, e := readLimitBalance(ctx, tx, tenant, m, a, at, "")
			if e != nil {
				return e
			}
			h.Balances[a.Quantity.Key()+"|"+string(a.Window)+"|"+string(a.EffectiveMeasure())] = b
			start, _ := a.Window.Start(at, m.Loc())
			var first *time.Time
			if e = tx.QueryRow(ctx, `SELECT min(drawn_at) FROM cloud_meta.mandate_draws WHERE tenant_id=$1 AND mandate_id=$2 AND quantity_key=$3 AND amount>0 AND drawn_at>=$4 AND drawn_at<=$5`, tenant, id, a.Quantity.Key(), start, at).Scan(&first); e != nil {
				return e
			}
			if first != nil {
				h.FirstDraws[a.Quantity.Key()+"|"+string(a.Window)] = *first
			}
		}
	} else if err != nil {
		return err
	} else {
		// A lifetime total is never rebuilt from the bounded history: it
		// takes the complete SQL balance, as a limited history does.
		for _, a := range m.Aggregates {
			if a.Window != WindowLifetime {
				continue
			}
			b, e := readLimitBalance(ctx, tx, tenant, m, a, at, "")
			if e != nil {
				return e
			}
			h = withBalance(h, a, b)
		}
	}
	c, err := readWarningConfig(ctx, tx, tenant, id, at)
	if err != nil {
		return err
	}
	var identities []string
	for _, a := range m.Aggregates {
		identity := warningIdentity(m, a, c, at)
		identities = append(identities, identity)
		var state []byte
		var e warningEpisode
		err = tx.QueryRow(ctx, `SELECT state FROM cloud_meta.mandate_warning_episodes WHERE tenant_id=$1 AND mandate_id=$2 AND limit_key=$3`, tenant, id, identity).Scan(&state)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if err = json.Unmarshal(state, &e); err != nil {
				return err
			}
		}
		e, w, err := evaluateWarnings(h, m, a, c, e, at)
		if err != nil {
			return err
		}
		state, err = json.Marshal(e)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO cloud_meta.mandate_warning_episodes(tenant_id,mandate_id,limit_key,state,observed_at)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,mandate_id,limit_key) DO UPDATE SET state=EXCLUDED.state,observed_at=EXCLUDED.observed_at`, tenant, id, identity, state, at)
		if err != nil {
			return err
		}
		currentEpisode := fmt.Sprintf("%s:%d", identity, e.Number)
		if _, err = tx.Exec(ctx, `UPDATE cloud_meta.mandate_warnings SET resolved_at=$4,attention_dirty=true WHERE tenant_id=$1 AND mandate_id=$2 AND split_part(body->>'episode',':',1)=$3 AND body->>'episode'<>$5 AND resolved_at IS NULL`, tenant, id, identity, at, currentEpisode); err != nil {
			return err
		}
		if w != nil {
			body, err := json.Marshal(w)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO cloud_meta.mandate_warnings(tenant_id,mandate_id,id,recorded_at,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, tenant, id, w.ID, w.At, body)
			if err != nil {
				return err
			}
		}
	}
	if identities == nil {
		identities = []string{}
	}
	_, err = tx.Exec(ctx, `UPDATE cloud_meta.mandate_warnings SET resolved_at=$3,attention_dirty=true WHERE tenant_id=$1 AND mandate_id=$2 AND resolved_at IS NULL AND NOT (split_part(body->>'episode',':',1)=ANY($4::text[]))`, tenant, id, at, identities)
	return err
}

// ScheduleWarningObservations revisits committed Cards once a minute for aging,
// room expiry and recovery. Pending work is never overwritten by a newer read.
func (s *PostgresStore) ScheduleWarningObservations(ctx context.Context) error {
	// Work snapshots are operational data, not the warning evidence. Retain
	// completed snapshots seven days; failed/pending work is never pruned.
	if _, err := s.pool.Exec(ctx, `DELETE FROM cloud_meta.mandate_warning_work WHERE id IN (SELECT id FROM cloud_meta.mandate_warning_work WHERE completed_at<now()-interval '7 days' ORDER BY id LIMIT 1000)`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_warning_work(tenant_id,mandate_id,observed_at,card)
	SELECT m.tenant_id,m.id,clock_timestamp(),to_jsonb(m) FROM cloud_meta.mandates m
	WHERE m.revoked_at IS NULL AND m.archived_at IS NULL AND (m.valid_until IS NULL OR m.valid_until>now())
	AND EXISTS(SELECT 1 FROM cloud_meta.mandate_warning_episodes e WHERE e.tenant_id=m.tenant_id AND e.mandate_id=m.id AND e.observed_at<now()-interval '1 minute')
	AND NOT EXISTS(SELECT 1 FROM cloud_meta.mandate_warning_work w WHERE w.tenant_id=m.tenant_id AND w.mandate_id=m.id AND w.completed_at IS NULL)`)
	return err
}
