package mandate_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
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
