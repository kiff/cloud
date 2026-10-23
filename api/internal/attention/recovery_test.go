package attention_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/kiff/cloud/api/internal/testdb"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/attention"
	"github.com/kiff/cloud/api/internal/tenant"
)

// decisionForProposal writes a blocked decision record with its own audit id
// under a shared proposal trace, the shape a re-decided proposal produces.
const decisionForProposal = `INSERT INTO kiff_audit(id,kind,entity_id,entity_type,actor_id,trace_id,created_at,data) VALUES($1,'proposal_decision','order','Order','agent',$2,now(),'{"outcome":"blocked","action":"refund","reasons":["mandate_ceiling"],"domain_id":"support"}')`

func count(t *testing.T, db *tenant.SchemaPool, id, sql string) int {
	t.Helper()
	var n int
	if err := db.WithTenantConn(context.Background(), id, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), sql).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// The same blocked decision delivered twice — as two decision records for
// one proposal, concurrently, and then replayed — is one queue item.
func TestDuplicateDecisionIsOneItem(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for n := 0; n < 6; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := db.WithTenantConn(ctx, id, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, decisionForProposal, fmt.Sprintf("rec-%d", n), "prop-1")
				return err
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := s.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	exec(t, db, id, `UPDATE cloud_attention_jobs SET completed_at=NULL`)
	if err := s.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	f := feed(t, s, id, "alice")
	if f.Open != 1 || f.Items[0].ID != "decision:prop-1" {
		t.Fatalf("duplicate decision made %d items: %+v", f.Open, f.Items)
	}
	if n := count(t, db, id, `SELECT count(*) FROM cloud_attention_events`); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
}

// Reviewers racing to claim one item: exactly one wins, every other gets
// ErrConflict (the API maps it to 409), and the winner is the assignee.
func TestConcurrentClaimHasOneWinner(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	exec(t, db, id, approvalSQL, "a1")
	item := feed(t, s, id, "alice").Items[0]

	const reviewers = 16
	var wins, conflicts atomic.Int32
	var winner atomic.Value
	start := make(chan struct{})
	var wg sync.WaitGroup
	for n := 0; n < reviewers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			who := fmt.Sprintf("reviewer-%d", n)
			<-start
			err := s.Act(ctx, id, who, item.ID, "claim", item.Revision)
			switch {
			case err == nil:
				wins.Add(1)
				winner.Store(who)
			case errors.Is(err, attention.ErrConflict):
				conflicts.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != reviewers-1 {
		t.Fatalf("wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
	got := feed(t, s, id, "alice").Items[0]
	if got.Assignee != winner.Load() || got.Revision != item.Revision+1 {
		t.Fatalf("assignee=%q rev=%d, want %q rev=%d", got.Assignee, got.Revision, winner.Load(), item.Revision+1)
	}
	if n := count(t, db, id, `SELECT count(*) FROM cloud_attention_history WHERE action='claim'`); n != 1 {
		t.Fatalf("claim history rows = %d, want 1", n)
	}
}

// New work arriving while a reviewer repeatedly renders the bell and clears
// it: only what was rendered is marked seen. Anything that arrived after
// the render, or changed revision since, stays unread.
func TestClearDuringArrivalOnlyAcknowledgesRendered(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	const arrivals = 40

	var mu sync.Mutex
	acked := map[string]int64{} // highest revision acknowledged per item
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // arrivals: new approvals and revisions of existing ones
		defer wg.Done()
		defer close(done)
		for n := 0; n < arrivals; n++ {
			exec(t, db, id, approvalSQL, fmt.Sprintf("a%02d", n))
			if n%5 == 4 {
				exec(t, db, id, `UPDATE kiff_approvals SET reason=$2 WHERE id=$1`, fmt.Sprintf("a%02d", n-2), fmt.Sprint("evidence ", n))
			}
		}
	}()
	go func() { // reviewer: render unread, then clear exactly that
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			f, err := s.List(ctx, id, "alice", 100, 0, true)
			if err != nil {
				t.Error(err)
				return
			}
			rendered := map[string]int64{}
			for _, it := range f.Items {
				rendered[it.ID] = it.Revision
			}
			if len(rendered) == 0 {
				continue
			}
			if err := s.MarkSeen(ctx, id, "alice", rendered); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for k, v := range rendered {
				if v > acked[k] {
					acked[k] = v
				}
			}
			mu.Unlock()
		}
	}()
	wg.Wait()

	f := feed(t, s, id, "alice")
	if f.Open != arrivals {
		t.Fatalf("open = %d, want %d (work lost)", f.Open, arrivals)
	}
	unread := 0
	for _, it := range f.Items {
		wantUnread := acked[it.ID] < it.Revision
		if it.Unread != wantUnread {
			t.Errorf("%s rev %d acked %d: unread=%v, want %v", it.ID, it.Revision, acked[it.ID], it.Unread, wantUnread)
		}
		if it.Unread {
			unread++
		}
	}
	if f.Unread != unread {
		t.Fatalf("feed unread count %d disagrees with items %d", f.Unread, unread)
	}
}

// A worker that dies mid-batch commits nothing: its projected items and
// job completions roll back together. After a restart (a fresh Store on a
// fresh pool) every pending item appears exactly once and every job is
// processed exactly once.
func TestCrashMidBatchRecoversWithoutDoubleProcessing(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	const events = 12
	for n := 0; n < events; n++ {
		exec(t, db, id, decisionSQL, fmt.Sprintf("d%02d", n))
	}

	// Hold an uncommitted row on one of the items the batch will project,
	// so the worker blocks partway through its batch.
	blocker, err := db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{tenant.SchemaNameFor(id)}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, `INSERT INTO cloud_attention(id,kind,source_id,action,entity_id,status,created_at) VALUES('decision:d06','blocked','d06','refund','order','open',now())`); err != nil {
		t.Fatal(err)
	}

	// The worker is killed while blocked: its context ends, the connection's
	// transaction aborts, exactly as when the process dies.
	crashCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := s.Process(crashCtx, id); err == nil {
		t.Fatal("worker should have been interrupted mid-batch")
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, id, `SELECT count(*) FROM cloud_attention`); n != 0 {
		t.Fatalf("crashed batch left %d projected items", n)
	}
	if n := count(t, db, id, `SELECT count(*) FROM cloud_attention_jobs WHERE completed_at IS NULL AND attempts=0`); n != events {
		t.Fatalf("pending untouched jobs after crash = %d, want %d", n, events)
	}

	// Restart: a new store over a new pool, with competing workers.
	freshPool, err := pgxpool.New(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer freshPool.Close()
	restarted := attention.NewStore(tenant.NewSchemaPool(freshPool))
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := restarted.Process(ctx, id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := restarted.Process(ctx, id); err != nil { // drain anything a locked-out worker skipped
		t.Fatal(err)
	}

	if f := feed(t, restarted, id, "alice"); f.Open != events || f.Pending != 0 || f.Failed != 0 {
		t.Fatalf("after restart: %+v", f)
	}
	if n := count(t, db, id, `SELECT count(*) FROM cloud_attention_jobs WHERE completed_at IS NOT NULL AND attempts=1`); n != events {
		t.Fatalf("jobs processed exactly once = %d, want %d", n, events)
	}
}

// Tenants provisioned before approvals were domain-scoped have no kiff_approvals.domain_id until
// kiff-tenant migrates them. Installing attention there must not break
// approval writes or the startup migration.
func TestLegacyApprovalsWithoutDomainColumn(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	exec(t, db, id, `ALTER TABLE kiff_approvals DROP COLUMN domain_id`)
	exec(t, db, id, `INSERT INTO kiff_approvals(id,entity_id,entity_type,action_name,requested_by,status,created_at) VALUES('legacy-pending','order','Order','refund','agent','pending',now())`)
	if err := s.Migrate(ctx, id); err != nil {
		t.Fatalf("migrate on legacy tenant: %v", err)
	}
	exec(t, db, id, `INSERT INTO kiff_approvals(id,entity_id,entity_type,action_name,requested_by,status,created_at) VALUES('legacy-new','order','Order','refund','agent','pending',now())`)
	if f := feed(t, s, id, "alice"); f.Open != 2 {
		t.Fatalf("legacy approvals in queue = %d, want 2", f.Open)
	}
}

// A materially different re-decision of the same proposal is not a
// duplicate: the item stays one item but updates, reopens, and bumps its
// revision so a reviewer who saw the old decision sees it as unread. An
// older decision processed late does not overwrite the newer one.
func TestReDecisionUpdatesItemAndMarksUnread(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	decide := func(rec, reason, at string) {
		exec(t, db, id, `INSERT INTO kiff_audit(id,kind,entity_id,entity_type,actor_id,trace_id,created_at,data)
			VALUES($1,'proposal_decision','order','Order','agent','prop-1',$3::timestamptz,
			jsonb_build_object('outcome','blocked','action','refund','reasons',jsonb_build_array($2::text),'domain_id','support'))`, rec, reason, at)
		if err := s.Process(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	decide("r1", "mandate_ceiling", "2026-09-29T10:00:00Z")
	first := feed(t, s, id, "alice").Items[0]
	if err := s.MarkSeen(ctx, id, "alice", map[string]int64{first.ID: first.Revision}); err != nil {
		t.Fatal(err)
	}
	if err := s.Act(ctx, id, "alice", first.ID, "claim", first.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.Act(ctx, id, "alice", first.ID, "resolve", first.Revision+1); err != nil {
		t.Fatal(err)
	}

	decide("r2", "mandate_ceiling", "2026-09-29T10:05:00Z") // same content: a duplicate
	if f := feed(t, s, id, "alice"); f.Open != 0 {
		t.Fatalf("identical re-delivery reopened the item: %+v", f)
	}

	decide("r3", "card_expired", "2026-09-29T11:00:00Z") // materially new
	f := feed(t, s, id, "alice")
	if f.Open != 1 || len(f.Items) != 1 {
		t.Fatalf("new decision should reopen the one item: %+v", f)
	}
	got := f.Items[0]
	if got.ID != first.ID || !strings.Contains(got.Reason, "card_expired") || !got.Unread || got.Revision <= first.Revision+2 {
		t.Fatalf("item not updated to the new decision: %+v (was rev %d)", got, first.Revision)
	}

	decide("r0", "stale_reason", "2026-09-29T09:00:00Z") // older decision, processed late
	if late := feed(t, s, id, "alice").Items[0]; late.Revision != got.Revision || strings.Contains(late.Reason, "stale_reason") {
		t.Fatalf("older decision overwrote newer: %+v", late)
	}
	if n := count(t, db, id, `SELECT count(*) FROM cloud_attention WHERE kind='blocked'`); n != 1 {
		t.Fatalf("items = %d, want 1", n)
	}
}

// A refusal that is the owner's answer to a held action (rejected,
// or the hold expired) is settled on Needs you and is not open work. It
// opens no item, a queued event from before the fix opens none either,
// and an item opened before the fix is resolved on migrate, on the record.
func TestAnsweredHoldIsNotOpenWork(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	decide := func(rec, reason string) {
		exec(t, db, id, `INSERT INTO kiff_audit(id,kind,entity_id,entity_type,actor_id,trace_id,created_at,data)
			VALUES($1,'proposal_decision','order','Order','agent',$1,now(),
			jsonb_build_object('outcome','blocked','action','refund','reasons',jsonb_build_array($2::text),'domain_id','support'))`, rec, reason)
	}
	decide("rejected", "mandate_owner_rejected")
	decide("expired", "mandate_owner_expired")
	decide("ceiling", "mandate_ceiling")
	// Queued before the capture trigger skipped answered holds.
	exec(t, db, id, `WITH e AS (INSERT INTO cloud_attention_events(event_key,payload) VALUES('decision:queued:x',
		'{"source_id":"queued","domain_id":"support","actor_id":"agent","action":"refund","entity_id":"order","reason":"[\"mandate_owner_rejected\"]","created_at":"2026-10-05T18:05:00Z"}')
		RETURNING id) INSERT INTO cloud_attention_jobs(event_id,consumer) SELECT id,'attention' FROM e`)
	if err := s.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	f := feed(t, s, id, "alice")
	if f.Open != 1 || len(f.Items) != 1 || f.Items[0].SourceID != "ceiling" {
		t.Fatalf("only the ceiling refusal is open work: %+v", f)
	}

	// Opened before the fix, as production has them.
	exec(t, db, id, `INSERT INTO cloud_attention(id,kind,source_id,action,entity_id,reason,status,created_at)
		VALUES('decision:old','blocked','old','get_order','tc-old','["mandate_owner_rejected"]','open',now())`)
	if err := s.Migrate(ctx, id); err != nil {
		t.Fatal(err)
	}
	if f := feed(t, s, id, "alice"); f.Open != 1 || f.Items[0].SourceID != "ceiling" {
		t.Fatalf("old answered hold still open after migrate: %+v", f)
	}
	if n := count(t, db, id, `SELECT count(*) FROM cloud_attention_history WHERE item_id='decision:old' AND actor='kiff:hold-answered' AND action='resolve'`); n != 1 {
		t.Fatalf("history rows = %d, want 1", n)
	}
	if err := s.Migrate(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, id, `SELECT count(*) FROM cloud_attention_history WHERE item_id='decision:old'`); n != 1 {
		t.Fatalf("migrate is not idempotent: %d history rows", n)
	}
}
