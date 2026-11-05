package mandate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/testdb"
)

func TestPostgresCardWarningsCaptureReplayAndDelivery(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tenantID := "pg-card-warning"
	seedTenant(t, pool, tenantID)
	s := mandate.NewPostgresStore(pool)
	a := mandate.Aggregate{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 3, Window: mandate.WindowRollingHour}
	m := mandate.Mandate{ID: "warning-card", TenantID: tenantID, Subject: "agent-a", Holders: []string{"agent-a", "agent-b"}, Aggregates: []mandate.Aggregate{a}}
	if err := s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	settings := mandate.DefaultWarningSettings()
	settings.Burst = false
	if _, err := s.SetWarningConfig(ctx, tenantID, m.ID, "admin", 0, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWarningConfig(ctx, tenantID, m.ID, "admin", 0, settings); !errors.Is(err, mandate.ErrWarningConflict) {
		t.Fatalf("lost update: %v", err)
	}
	for i, subject := range []string{"agent-a", "agent-b", "agent-a"} {
		now := time.Now().UTC()
		start := now.Add(-time.Hour)
		d := mandate.Draw{TenantID: tenantID, MandateID: m.ID, ProposalID: subject + now.Format(time.RFC3339Nano), Subject: subject, QuantityKey: "count", Amount: 1, DrawnAt: now, AccountingVersion: 1, Measure: mandate.MeasureAttempts, ChargedLimits: []mandate.ChargedLimit{{Measure: mandate.MeasureAttempts, Window: a.Window, Location: "UTC", WindowStart: start, WindowEnd: now.Add(time.Hour)}}}
		if err := s.RecordAll(ctx, []mandate.Draw{d}); err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
	}
	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.mandate_warning_work WHERE tenant_id=$1`, tenantID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO cloud_meta.mandate_draws(tenant_id,mandate_id,proposal_id,quantity_key,amount,drawn_at,accounting_version) VALUES($1,$2,'rollback','count',1,now(),1)`, tenantID, m.ID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.mandate_warning_work WHERE tenant_id=$1`, tenantID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("rollback leaked warning work")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- mandate.NewPostgresStore(pool).ProcessWarningWork(ctx) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// A fresh process replays/reconciles without another notice.
	s = mandate.NewPostgresStore(pool)
	if err = s.ProcessWarningWork(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.CardWarnings(ctx, tenantID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "spent" || events[0].Used != 3 {
		t.Fatalf("warnings: %+v", events)
	}
	if other, err := s.CardWarnings(ctx, "another-tenant", m.ID); err != nil || len(other) != 0 {
		t.Fatalf("tenant leak: %+v %v", other, err)
	}
	attention, lookups, sends := 0, 0, 0
	deliver := func() error {
		return s.DeliverWarning(ctx, events[0], func(context.Context, mandate.CardWarning) error { attention++; return nil }, func(context.Context, mandate.CardWarning) ([]string, error) {
			lookups++
			return []string{"test@example.invalid"}, nil
		}, func(context.Context, mandate.CardWarning, string) error {
			sends++
			if sends == 1 {
				return errors.New("temporary synthetic outage")
			}
			return nil
		})
	}
	if err = deliver(); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE cloud_meta.mandate_warning_deliveries SET available_at=now() WHERE tenant_id=$1`, tenantID); err != nil {
		t.Fatal(err)
	}
	if err = deliver(); err != nil {
		t.Fatal(err)
	}
	if err = deliver(); err != nil {
		t.Fatal(err)
	}
	if attention != 1 || lookups != 1 || sends != 2 {
		t.Fatalf("delivery counts %d/%d/%d", attention, lookups, sends)
	}
	a.Limit = 10
	m.Aggregates = []mandate.Aggregate{a}
	if err = s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessWarningWork(ctx); err != nil {
		t.Fatal(err)
	}
	events, err = s.CardWarnings(ctx, tenantID, m.ID)
	if err != nil || len(events) != 1 || events[0].ResolvedAt == nil {
		t.Fatalf("recovery lost history: %+v %v", events, err)
	}
	if err = deliver(); err != nil {
		t.Fatal(err)
	}
	if sends != 2 || attention != 2 {
		t.Fatalf("recovery resend: sends %d attention %d", sends, attention)
	}
}

func TestPostgresCardWarningRecipientFailureBacksOff(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tenantID := "pg-warning-backoff"
	seedTenant(t, pool, tenantID)
	s := mandate.NewPostgresStore(pool)
	m := mandate.Mandate{ID: "card", TenantID: tenantID, Subject: "agent"}
	if err := s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	event := mandate.CardWarning{ID: "backoff", TenantID: tenantID, CardID: m.ID, Kind: "spent", At: time.Now().UTC()}
	raw, _ := json.Marshal(event)
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_warnings(tenant_id,mandate_id,id,recorded_at,body) VALUES($1,$2,$3,$4,$5)`, tenantID, m.ID, event.ID, event.At, raw); err != nil {
		t.Fatal(err)
	}
	lookupErr := errors.New("recipient service unavailable")
	err := s.DeliverWarning(ctx, event, func(context.Context, mandate.CardWarning) error { return nil }, func(context.Context, mandate.CardWarning) ([]string, error) { return nil, lookupErr }, func(context.Context, mandate.CardWarning, string) error { t.Fatal("unexpected send"); return nil })
	if !errors.Is(err, lookupErr) {
		t.Fatalf("lost error: %v", err)
	}
	var backedOff bool
	var message string
	if err := pool.QueryRow(ctx, `SELECT delivery_available_at>now() AND delivery_lease_until IS NULL,delivery_error FROM cloud_meta.mandate_warnings WHERE tenant_id=$1 AND id=$2`, tenantID, event.ID).Scan(&backedOff, &message); err != nil {
		t.Fatal(err)
	}
	if !backedOff || message == "" {
		t.Fatalf("event is not backed off: %v %s", backedOff, message)
	}
	pending, err := s.PendingWarnings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range pending {
		if e.ID == event.ID {
			t.Fatal("failing event immediately returned to front of batch")
		}
	}
}

func TestPostgresCardWarningSendsCommitIndividually(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tenantID := "pg-warning-send-commit"
	seedTenant(t, pool, tenantID)
	s := mandate.NewPostgresStore(pool)
	m := mandate.Mandate{ID: "card", TenantID: tenantID, Subject: "agent"}
	if err := s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	event := mandate.CardWarning{ID: "send-commit", TenantID: tenantID, CardID: m.ID, Kind: "spent", At: time.Now().UTC()}
	raw, _ := json.Marshal(event)
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_warnings(tenant_id,mandate_id,id,recorded_at,body) VALUES($1,$2,$3,$4,$5)`, tenantID, m.ID, event.ID, event.At, raw); err != nil {
		t.Fatal(err)
	}
	pass, cancel := context.WithCancel(ctx)
	defer cancel()
	sends := map[string]int{}
	project := func(context.Context, mandate.CardWarning) error { return nil }
	lookup := func(context.Context, mandate.CardWarning) ([]string, error) { return []string{"a", "b"}, nil }
	err := s.DeliverWarning(pass, event, project, lookup, func(_ context.Context, _ mandate.CardWarning, d string) error {
		sends[d]++
		// No delivery transaction may hold this row while a third party runs.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `SELECT id FROM cloud_meta.mandate_warnings WHERE tenant_id=$1 AND id=$2 FOR UPDATE NOWAIT`, tenantID, event.ID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal("send holds a row lock", err)
		}
		_ = tx.Rollback(ctx)
		if d == "b" {
			cancel()
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("deadline not reported: %v", err)
	}
	var delivered int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.mandate_warning_deliveries WHERE tenant_id=$1 AND event_id=$2 AND delivered_at IS NOT NULL`, tenantID, event.ID).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("earlier successful send rolled back: %d", delivered)
	}
	if _, err := pool.Exec(ctx, `UPDATE cloud_meta.mandate_warning_deliveries SET available_at=now() WHERE tenant_id=$1`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cloud_meta.mandate_warnings SET delivery_available_at=now() WHERE tenant_id=$1`, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverWarning(ctx, event, project, lookup, func(_ context.Context, _ mandate.CardWarning, d string) error { sends[d]++; return nil }); err != nil {
		t.Fatal(err)
	}
	if sends["a"] != 1 || sends["b"] != 2 {
		t.Fatalf("successful destination repeated: %v", sends)
	}
}

// A lifetime total's warnings read the whole ledger, not the bounded
// eight-day history. Two draws ten days ago and one now spend a lifetime
// count of 3; the warning says so, with the full count.
func TestPostgresLifetimeWarningCountsTheWholeLedger(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tenantID := "pg-lifetime-warning"
	seedTenant(t, pool, tenantID)
	s := mandate.NewPostgresStore(pool)
	a := mandate.Aggregate{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 3, Window: mandate.WindowLifetime}
	m := mandate.Mandate{ID: "lifetime-card", TenantID: tenantID, Subject: "agent-a", Aggregates: []mandate.Aggregate{a}}
	if err := s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	settings := mandate.DefaultWarningSettings()
	settings.Burst = false
	if _, err := s.SetWarningConfig(ctx, tenantID, m.ID, "admin", 0, settings); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, at := range []time.Time{now.Add(-10 * 24 * time.Hour), now.Add(-10*24*time.Hour + time.Minute), now} {
		d := mandate.Draw{TenantID: tenantID, MandateID: m.ID, ProposalID: "p" + at.Format(time.RFC3339Nano), Subject: "agent-a", QuantityKey: "count", Amount: 1, DrawnAt: at,
			AccountingVersion: 1, Measure: mandate.MeasureAttempts, Window: mandate.WindowLifetime,
			ChargedLimits: []mandate.ChargedLimit{{Measure: mandate.MeasureAttempts, Window: mandate.WindowLifetime, Location: "UTC", WindowEnd: time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)}}}
		if err := s.RecordAll(ctx, []mandate.Draw{d}); err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
	}
	if err := s.ProcessWarningWork(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.CardWarnings(ctx, tenantID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "spent" || events[0].Used != 3 || events[0].Window != mandate.WindowLifetime {
		t.Fatalf("warnings: %+v", events)
	}
}

// The warning worker's queries read their indexes, so each touches only
// its rows: the prune of completed work and the pick of the next pending
// job. In id order each read the whole table, over 30 s in production,
// longer than the worker's stage.
func TestPostgresWarningWorkQueriesReadTheirIndexes(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	// Twice: it is idempotent once the indexes are valid.
	for range 2 {
		if err := mandate.BuildWarningIndexes(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	for name, query := range map[string]string{
		"mandate_warning_work_completed": `SELECT id FROM cloud_meta.mandate_warning_work WHERE completed_at<now()-interval '7 days' ORDER BY completed_at LIMIT 1000`,
		"mandate_warning_work_pending_id": `SELECT w.id FROM cloud_meta.mandate_warning_work w WHERE w.completed_at IS NULL AND w.available_at<=now()
			AND NOT EXISTS(SELECT 1 FROM cloud_meta.mandate_warning_work old WHERE old.tenant_id=w.tenant_id AND old.mandate_id=w.mandate_id AND old.completed_at IS NULL AND old.id<w.id)
			ORDER BY w.id LIMIT 1`,
	} {
		if plan := explainNoSeqScan(t, pool, query); !strings.Contains(plan, name) {
			t.Fatalf("%s isn't read:\n%s", name, plan)
		}
	}
}

// Two instances starting together build the indexes one at a time: the
// second leaves it to the first instead of dropping its build in progress.
func TestPostgresWarningIndexesBuildOneAtATime(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	for _, name := range []string{"mandate_warning_work_completed", "mandate_warning_work_pending_id"} {
		if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS cloud_meta.`+name); err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- mandate.BuildWarningIndexes(ctx, pool) }()
	}
	busy := 0
	for range 2 {
		err := <-errs
		if errors.Is(err, mandate.ErrWarningIndexesBuilding) {
			busy++
		} else if err != nil {
			t.Fatalf("a build at the same time failed: %v", err)
		}
	}
	if busy > 1 {
		t.Fatal("neither instance built the indexes")
	}
	var valid int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname IN ('mandate_warning_work_completed','mandate_warning_work_pending_id') AND i.indisvalid`).Scan(&valid); err != nil || valid != 2 {
		t.Fatalf("valid indexes: %d %v", valid, err)
	}
}

func explainNoSeqScan(t *testing.T, pool *pgxpool.Pool, query string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN `+query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}

// Warning work observed more than a day ago is skipped, not sent: a backlog
// that drains late doesn't warn about windows long past.
func TestPostgresStaleWarningWorkIsSkippedNotSent(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tenantID := "pg-card-warning-stale"
	seedTenant(t, pool, tenantID)
	s := mandate.NewPostgresStore(pool)
	a := mandate.Aggregate{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 3, Window: mandate.WindowRollingHour}
	m := mandate.Mandate{ID: "stale-warning-card", TenantID: tenantID, Subject: "agent-a", Holders: []string{"agent-a"}, Aggregates: []mandate.Aggregate{a}}
	if err := s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		now := time.Now().UTC()
		d := mandate.Draw{TenantID: tenantID, MandateID: m.ID, ProposalID: fmt.Sprintf("stale-%d-%d", i, now.UnixNano()), Subject: "agent-a", QuantityKey: "count", Amount: 1, DrawnAt: now, AccountingVersion: 1, Measure: mandate.MeasureAttempts,
			ChargedLimits: []mandate.ChargedLimit{{Measure: mandate.MeasureAttempts, Window: a.Window, Location: "UTC", WindowStart: now.Add(-time.Hour), WindowEnd: now.Add(time.Hour)}}}
		if err := s.RecordAll(ctx, []mandate.Draw{d}); err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
	}
	tag, err := pool.Exec(ctx, `UPDATE cloud_meta.mandate_warning_work SET observed_at=observed_at-interval '25 hours' WHERE tenant_id=$1 AND completed_at IS NULL`, tenantID)
	if err != nil || tag.RowsAffected() == 0 {
		t.Fatalf("aging the work: %v (%d rows)", err, tag.RowsAffected())
	}
	for range 5 {
		if err := s.ProcessWarningWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var pending, skipped, warnings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE completed_at IS NULL), count(*) FILTER (WHERE last_error=$2)
		FROM cloud_meta.mandate_warning_work WHERE tenant_id=$1`, tenantID, mandate.SkippedWarningWork).Scan(&pending, &skipped); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.mandate_warnings WHERE tenant_id=$1`, tenantID).Scan(&warnings); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || skipped == 0 || warnings != 0 {
		t.Fatalf("pending %d, skipped %d, warnings %d: stale work must be skipped and send nothing", pending, skipped, warnings)
	}
}

// Stale work sends nothing, but it still resolves
// what it would resolve, so an open warning doesn't stay in attention when
// the only observation left for its Card is old.
func TestPostgresStaleWarningWorkStillResolves(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tenantID := "pg-card-warning-stale-resolve"
	seedTenant(t, pool, tenantID)
	s := mandate.NewPostgresStore(pool)
	a := mandate.Aggregate{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 3, Window: mandate.WindowRollingHour}
	m := mandate.Mandate{ID: "stale-resolve-card", TenantID: tenantID, Subject: "agent-a", Holders: []string{"agent-a"}, Aggregates: []mandate.Aggregate{a}}
	if err := s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	// An open warning from an episode the Card no longer has.
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.mandate_warnings(tenant_id,mandate_id,id,recorded_at,body) VALUES($1,$2,'w-old',now()-interval '2 days','{"episode":"gone-limit:1"}')`, tenantID, m.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	d := mandate.Draw{TenantID: tenantID, MandateID: m.ID, ProposalID: fmt.Sprintf("stale-resolve-%d", now.UnixNano()), Subject: "agent-a", QuantityKey: "count", Amount: 1, DrawnAt: now, AccountingVersion: 1, Measure: mandate.MeasureAttempts,
		ChargedLimits: []mandate.ChargedLimit{{Measure: mandate.MeasureAttempts, Window: a.Window, Location: "UTC", WindowStart: now.Add(-time.Hour), WindowEnd: now.Add(time.Hour)}}}
	if err := s.RecordAll(ctx, []mandate.Draw{d}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cloud_meta.mandate_warning_work SET observed_at=observed_at-interval '25 hours' WHERE tenant_id=$1 AND completed_at IS NULL`, tenantID); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.ProcessWarningWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var open, recorded, skipped int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE resolved_at IS NULL), count(*) FILTER (WHERE id<>'w-old') FROM cloud_meta.mandate_warnings WHERE tenant_id=$1`, tenantID).Scan(&open, &recorded); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.mandate_warning_work WHERE tenant_id=$1 AND last_error=$2`, tenantID, mandate.SkippedWarningWork).Scan(&skipped); err != nil {
		t.Fatal(err)
	}
	if open != 0 || recorded != 0 || skipped == 0 {
		t.Fatalf("open %d, new %d, skipped %d: stale work resolves the old warning and records none", open, recorded, skipped)
	}
}

// A build whose unlock fails doesn't hand its
// connection, still holding the lock, back to the pool. Another instance
// (its own pool) can build afterwards.
// A quiet pass over stale work marks nothing seen that it didn't send: a
// lifetime Card left spent after a backlog still warns spent on the next
// fresh observation.
func TestPostgresAQuietPassStillLeavesSpentDue(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tenantID := "pg-card-warning-quiet-spent"
	seedTenant(t, pool, tenantID)
	s := mandate.NewPostgresStore(pool)
	a := mandate.Aggregate{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 3, Window: mandate.WindowLifetime}
	m := mandate.Mandate{ID: "quiet-spent-card", TenantID: tenantID, Subject: "agent-a", Holders: []string{"agent-a"}, Aggregates: []mandate.Aggregate{a}}
	if err := s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	draw := func(i int, now time.Time) {
		t.Helper()
		d := mandate.Draw{TenantID: tenantID, MandateID: m.ID, ProposalID: fmt.Sprintf("quiet-%d-%d", i, now.UnixNano()), Subject: "agent-a", QuantityKey: "count", Amount: 1, DrawnAt: now, AccountingVersion: 1, Measure: mandate.MeasureAttempts,
			ChargedLimits: []mandate.ChargedLimit{{Measure: mandate.MeasureAttempts, Window: a.Window, Location: "UTC", WindowStart: time.Unix(0, 0).UTC(), WindowEnd: time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)}}}
		if err := s.RecordAll(ctx, []mandate.Draw{d}); err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
	}
	// The backlog: three draws a day ago spent the Card, and the work that
	// saw them is a day old too, so it passes quietly.
	for i := range 3 {
		draw(i, time.Now().UTC().Add(-26*time.Hour))
	}
	if _, err := pool.Exec(ctx, `UPDATE cloud_meta.mandate_warning_work SET observed_at=observed_at-interval '25 hours' WHERE tenant_id=$1 AND completed_at IS NULL`, tenantID); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := s.ProcessWarningWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh observation of the still-spent Card warns spent.
	draw(3, time.Now().UTC())
	for range 5 {
		if err := s.ProcessWarningWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var spent int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.mandate_warnings WHERE tenant_id=$1 AND body->>'kind'='spent'`, tenantID).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent != 1 {
		t.Fatalf("%d spent warnings after the backlog, want 1: the quiet pass took it", spent)
	}
}

func TestPostgresAFailedUnlockDoesntLeaveTheBuildLocked(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	restore := mandate.SetWarningUnlockSQL(`SELECT pg_advisory_unlock(hashtextextended($1, 0)), 1/0`)
	err := mandate.BuildWarningIndexes(ctx, pool)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	other, err := pgxpool.New(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := mandate.BuildWarningIndexes(ctx, other); err != nil {
		t.Fatalf("after a failed unlock, another instance: %v", err)
	}
}

// An instance that found another building
// tries again, so a build whose instance died (its lock gone, its index
// left invalid) is finished without waiting for the next start.
func TestPostgresABuildLeftByADeadInstanceIsFinished(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS cloud_meta.mandate_warning_work_pending_id`); err != nil {
		t.Fatal(err)
	}
	// The dying instance: it holds the build lock on its own session.
	dying, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dying.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('cloud_meta.mandate_warning_work indexes', 0))`); err != nil {
		t.Fatal(err)
	}
	waits := 0
	done := make(chan error, 1)
	go func() {
		done <- mandate.BuildWarningIndexesWhenFree(ctx, pool, 50*time.Millisecond, func() { waits++ })
	}()
	time.Sleep(200 * time.Millisecond)
	_ = dying.Close(ctx) // it dies; its lock goes with its session
	select {
	case err := <-done:
		if err != nil || waits == 0 {
			t.Fatalf("err %v after %d waits", err, waits)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("never built after the other instance died")
	}
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = 'mandate_warning_work_pending_id'`).Scan(&valid); err != nil || !valid {
		t.Fatalf("index valid %v %v", valid, err)
	}
}
