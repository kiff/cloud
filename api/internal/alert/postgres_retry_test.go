package alert_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
)

// The retry queue claims only what is due, hides a claimed retry for the
// lease, keeps the exact body, and drops a row once finished. A
// target is switched off with its reason and back on.
func TestPostgresRetryStoreAndTargetSwitch(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	c := context.Background()
	if err := alert.ApplySchema(c, pool); err != nil {
		t.Fatal(err)
	}
	tt := provisionTenantRow(t, pool)
	q := alert.NewPostgresRetryStore(pool)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, r := range []alert.Retry{
		{ID: "r-due", TenantID: tt.ID, TargetID: "t1", EventID: "evt_1", ProposalID: "p1", Action: "refund", Outcome: "owner_required",
			Body: []byte(`{"event_id":"evt_1"}`), Attempts: 1, NextAt: now.Add(-time.Second), LastError: "503"},
		{ID: "r-later", TenantID: tt.ID, TargetID: "t1", Body: []byte(`{}`), Attempts: 1, NextAt: now.Add(time.Hour)},
	} {
		if err := q.ScheduleRetry(c, r); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := q.ClaimDueRetry(c, now, 2*time.Minute)
	if err != nil || !ok || got.ID != "r-due" || string(got.Body) != `{"event_id":"evt_1"}` || got.Attempts != 1 || got.Outcome != "owner_required" || got.Claim == "" {
		t.Fatalf("claim = %+v, %v %v", got, ok, err)
	}
	if _, ok, _ := q.ClaimDueRetry(c, now.Add(time.Minute), 2*time.Minute); ok {
		t.Fatal("a leased retry was claimed again")
	}
	got.Attempts, got.NextAt, got.LastError = 2, now.Add(5*time.Minute), "500"
	if err := q.RescheduleRetry(c, got); err != nil {
		t.Fatal(err)
	}
	back, ok, _ := q.ClaimDueRetry(c, now.Add(6*time.Minute), 2*time.Minute)
	if !ok || back.Attempts != 2 || back.LastError != "500" || back.Claim == got.Claim {
		t.Fatalf("rescheduled = %+v", back)
	}
	// The earlier claim no longer holds it: its updates are refused.
	stale := got
	stale.Attempts = 9
	if err := q.RescheduleRetry(c, stale); !errors.Is(err, alert.ErrLeaseLost) {
		t.Errorf("stale reschedule: %v", err)
	}
	if err := q.FinishRetry(c, stale); !errors.Is(err, alert.ErrLeaseLost) {
		t.Errorf("stale finish: %v", err)
	}
	if err := q.FinishRetry(c, back); err != nil {
		t.Fatal(err)
	}
	if rest, ok, _ := q.ClaimDueRetry(c, now.Add(48*time.Hour), time.Minute); !ok || rest.ID != "r-later" {
		t.Fatalf("after finish = %+v", rest)
	}

	store := alert.NewPostgresStore(pool)
	_ = store.CreateTarget(c, alert.Target{ID: "t1", TenantID: tt.ID, URL: "https://h/1", Enabled: true, CreatedAt: now})
	if ok, err := store.SetEnabled(c, tt.ID, "t1", false, "deliveries kept failing", now); err != nil || !ok {
		t.Fatalf("switch off: %v %v", ok, err)
	}
	ts, _ := store.ListTargets(c, tt.ID)
	if ts[0].Enabled || ts[0].DisabledReason != "deliveries kept failing" || ts[0].DisabledAt == nil {
		t.Fatalf("off = %+v", ts[0])
	}
	if ok, _ := store.SetEnabled(c, tt.ID, "t1", true, "", now); !ok {
		t.Fatal("switch on")
	}
	if ts, _ := store.ListTargets(c, tt.ID); !ts[0].Enabled || ts[0].DisabledReason != "" || ts[0].DisabledAt != nil {
		t.Fatalf("on = %+v", ts[0])
	}
	if ok, _ := store.SetEnabled(c, "other", "t1", true, "", now); ok {
		t.Error("switched another account's target")
	}
}

// GiveUp is fenced by the claim in one transaction: a stale claim changes
// nothing, and while a worker gives up no one else can claim the retry.
func TestPostgresGiveUpIsFencedByTheClaim(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	c := context.Background()
	if err := alert.ApplySchema(c, pool); err != nil {
		t.Fatal(err)
	}
	tt := provisionTenantRow(t, pool)
	q := alert.NewPostgresRetryStore(pool)
	store := alert.NewPostgresStore(pool)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	_ = store.CreateTarget(c, alert.Target{ID: "t1", TenantID: tt.ID, URL: "https://h/1", Enabled: true, CreatedAt: now})
	enabled := func() bool {
		ts, _ := store.ListTargets(c, tt.ID)
		return ts[0].Enabled
	}
	if err := q.ScheduleRetry(c, alert.Retry{ID: "r1", TenantID: tt.ID, TargetID: "t1", Body: []byte(`{}`), Attempts: 7, NextAt: now}); err != nil {
		t.Fatal(err)
	}
	stale, _, _ := q.ClaimDueRetry(c, now, time.Minute)
	cur, ok, _ := q.ClaimDueRetry(c, now.Add(2*time.Minute), time.Minute)
	if !ok {
		t.Fatal("no re-claim after the lease")
	}
	if err := q.GiveUp(c, stale, "kept failing", now); !errors.Is(err, alert.ErrLeaseLost) || !enabled() {
		t.Fatalf("stale give-up: %v, enabled %v", err, enabled())
	}

	// Hold the target row, so the give-up waits inside its transaction
	// with the retry locked: a claim past the lease finds nothing.
	lock, err := pool.Begin(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(c, `SELECT 1 FROM cloud_meta.alert_targets WHERE tenant_id = $1 AND id = 't1' FOR UPDATE`, tt.ID); err != nil {
		t.Fatal(err)
	}
	gave := make(chan error)
	go func() { gave <- q.GiveUp(c, cur, "kept failing", now) }()
	time.Sleep(200 * time.Millisecond)
	if _, ok, err := q.ClaimDueRetry(c, now.Add(time.Hour), time.Minute); ok || err != nil {
		t.Fatalf("claimed a retry being given up: %v %v", ok, err)
	}
	_ = lock.Rollback(c)
	if err := <-gave; err != nil {
		t.Fatal(err)
	}
	ts, _ := store.ListTargets(c, tt.ID)
	if ts[0].Enabled || ts[0].DisabledReason != "kept failing" {
		t.Fatalf("target = %+v", ts[0])
	}
	if _, ok, _ := q.ClaimDueRetry(c, now.Add(time.Hour), time.Minute); ok {
		t.Error("the retry stayed after giving up")
	}
}
