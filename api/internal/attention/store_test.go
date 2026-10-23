package attention_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/kiff/cloud/api/internal/testdb"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiff/cloud/api/internal/approvals"
	"github.com/kiff/cloud/api/internal/attention"
	"github.com/kiff/cloud/api/internal/tenant"
	kiffpostgres "github.com/kiff/kiff/pkg/kiff/store/postgres"
)

func rig(t *testing.T) (*attention.Store, *tenant.SchemaPool, string) {
	t.Helper()
	dsn := testdb.URL(t)
	if dsn == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	id := tenant.NewID()
	schema := pgx.Identifier{tenant.SchemaNameFor(id)}.Sanitize()
	if _, err := pool.Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); pool.Close() })
	db := tenant.NewSchemaPool(pool)
	if err := db.WithTenantConn(context.Background(), id, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), kiffpostgres.SchemaSQL()+approvals.SchemaSQL())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	store := attention.NewStore(db)
	if err := store.Migrate(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	return store, db, id
}
func exec(t *testing.T, db *tenant.SchemaPool, id, sql string, args ...any) {
	t.Helper()
	if err := db.WithTenantConn(context.Background(), id, func(tx pgx.Tx) error { _, err := tx.Exec(context.Background(), sql, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

const approvalSQL = `INSERT INTO kiff_approvals(id,entity_id,entity_type,action_name,requested_by,status,created_at,domain_id) VALUES($1,'order','Order','refund','agent','pending',now(),'support')`

func TestCardWarningAttentionIsInformationalAndDeduplicated(t *testing.T) {
	s, _, id := rig(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.CardWarning(ctx, id, "synthetic-warning", "card", "Synthetic budget notice", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	f, err := s.ListKind(ctx, id, "reviewer", 100, 0, false, "card_warning")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Items) != 1 || f.Items[0].Kind != "card_warning" || f.Items[0].SourceID != "card" {
		t.Fatalf("%+v", f)
	}
	item := f.Items[0]
	if err = s.Act(ctx, id, "reviewer", item.ID, "claim", item.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.Act(ctx, id, "reviewer", item.ID, "resolve", item.Revision+1); err != nil {
		t.Fatal(err)
	}
	f, err = s.ListKind(ctx, id, "reviewer", 100, 0, false, "card_warning")
	if err != nil || f.Open != 0 {
		t.Fatalf("resolve: %+v %v", f, err)
	}
}

const decisionSQL = `INSERT INTO kiff_audit(id,kind,entity_id,entity_type,actor_id,trace_id,created_at,data) VALUES($1,'proposal_decision','order','Order','agent',$1,now(),'{"outcome":"blocked","action":"refund","reasons":["mandate_ceiling"],"domain_id":"support"}')`

func feed(t *testing.T, s *attention.Store, id, user string) attention.Feed {
	t.Helper()
	f, err := s.List(context.Background(), id, user, 100, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestApprovalLifecycleAndPersonalSeen(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	exec(t, db, id, approvalSQL, "a1")
	f := feed(t, s, id, "alice")
	if f.Open != 1 || f.Unread != 1 {
		t.Fatalf("approval must be visible without a worker: %+v", f)
	}
	item := f.Items[0]
	if err := s.MarkSeen(ctx, id, "alice", map[string]int64{item.ID: item.Revision}); err != nil {
		t.Fatal(err)
	}
	if f := feed(t, s, id, "alice"); f.Open != 1 || f.Unread != 0 {
		t.Fatalf("seen must not resolve: %+v", f)
	}
	if f := feed(t, s, id, "bob"); f.Unread != 1 {
		t.Fatal("seen leaked across users")
	}
	if err := s.Act(ctx, id, "alice", item.ID, "claim", item.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.Act(ctx, id, "bob", item.ID, "claim", item.Revision); !errors.Is(err, attention.ErrConflict) {
		t.Fatalf("stale claim: %v", err)
	}
	if err := s.Act(ctx, id, "alice", item.ID, "resolve", item.Revision+1); !errors.Is(err, attention.ErrConflict) {
		t.Fatal("queue resolved an approval")
	}
	exec(t, db, id, `UPDATE kiff_approvals SET status='granted',reviewed_by='alice',reviewed_at=now() WHERE id='a1'`)
	if f := feed(t, s, id, "alice"); f.Open != 0 {
		t.Fatalf("canonical review did not resolve: %+v", f)
	}
	if err := s.Migrate(ctx, id); err != nil {
		t.Fatal(err)
	}
	if f := feed(t, s, id, "alice"); f.Open != 0 {
		t.Fatal("migration reopened work")
	}
}
func TestCaptureRollbackReplayAndConcurrentConsumers(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	sentinel := errors.New("rollback")
	_ = db.WithTenantConn(ctx, id, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, decisionSQL, "rollback"); err != nil {
			t.Fatal(err)
		}
		return sentinel
	})
	for n := 0; n < 12; n++ {
		exec(t, db, id, decisionSQL, fmt.Sprint(n))
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Process(ctx, id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	f := feed(t, s, id, "alice")
	if f.Open != 12 {
		t.Fatalf("lost or duplicate projection: %+v", f)
	}
	exec(t, db, id, `UPDATE cloud_attention_jobs SET completed_at=NULL`)
	if err := s.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	if feed(t, s, id, "alice").Open != 12 {
		t.Fatal("replay duplicated items")
	}
	item := f.Items[0]
	if err := s.Act(ctx, id, "alice", item.ID, "claim", item.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.Act(ctx, id, "alice", item.ID, "resolve", item.Revision+1); err != nil {
		t.Fatal(err)
	}
	exec(t, db, id, `UPDATE cloud_attention_jobs SET completed_at=NULL`)
	if err := s.Process(ctx, id); err != nil {
		t.Fatal(err)
	}
	if feed(t, s, id, "alice").Open != 11 {
		t.Fatal("replay reopened resolved work")
	}
}
func TestSeenDoesNotHideNewWorkOrRevision(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	exec(t, db, id, approvalSQL, "a1")
	old := feed(t, s, id, "alice").Items[0]
	exec(t, db, id, approvalSQL, "a2")
	exec(t, db, id, `UPDATE kiff_approvals SET reason='new evidence' WHERE id='a1'`)
	if err := s.MarkSeen(ctx, id, "alice", map[string]int64{old.ID: old.Revision}); err != nil {
		t.Fatal(err)
	}
	if f := feed(t, s, id, "alice"); f.Unread != 2 {
		t.Fatalf("new work incorrectly acknowledged: %+v", f)
	}
}
func TestPoisonJobDoesNotBlockHealthyWork(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	exec(t, db, id, `WITH e AS (INSERT INTO cloud_attention_events(event_key,payload) VALUES('bad','{}') RETURNING id) INSERT INTO cloud_attention_jobs(event_id,consumer) SELECT id,'attention' FROM e`)
	exec(t, db, id, decisionSQL, "healthy")
	for n := 0; n < 8; n++ {
		exec(t, db, id, `UPDATE cloud_attention_jobs SET available_at=now()`)
		if err := s.Process(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	f := feed(t, s, id, "alice")
	if f.Open != 1 || f.Failed != 1 || f.Pending != 0 {
		t.Fatalf("poison handling: %+v", f)
	}
}
func TestTenantIsolation(t *testing.T) {
	s, db, a := rig(t)
	_, _, b := rig(t)
	exec(t, db, a, approvalSQL, "a1")
	if f := feed(t, s, b, "alice"); f.Open != 0 {
		t.Fatal("cross tenant read")
	}
	if err := s.Act(context.Background(), b, "alice", "approval:a1", "claim", 1); !errors.Is(err, attention.ErrConflict) {
		t.Fatal("cross tenant mutation")
	}
}
func TestUnreadPaging(t *testing.T) {
	s, db, id := rig(t)
	ctx := context.Background()
	for n := 0; n < 105; n++ {
		exec(t, db, id, approvalSQL, fmt.Sprintf("a%03d", n))
	}
	f := feed(t, s, id, "alice")
	if !f.Truncated || f.Open != 105 {
		t.Fatal("missing total or pagination")
	}
	seen := map[string]int64{}
	for _, item := range f.Items {
		seen[item.ID] = item.Revision
	}
	if err := s.MarkSeen(ctx, id, "alice", seen); err != nil {
		t.Fatal(err)
	}
	f, err := s.List(ctx, id, "alice", 100, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Items) != 5 || f.Unread != 5 {
		t.Fatalf("unseen work stranded: %+v", f)
	}
}

type boundedProcessor struct {
	mu          sync.Mutex
	active, max int
	started     chan struct{}
}

func (p *boundedProcessor) Process(ctx context.Context, _ string) error {
	p.mu.Lock()
	p.active++
	if p.active > p.max {
		p.max = p.active
	}
	p.mu.Unlock()
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return ctx.Err()
}
func TestWorkerConcurrencyAndShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &boundedProcessor{started: make(chan struct{}, 4)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		attention.Run(ctx, p, func(context.Context) ([]string, error) { return []string{"a", "b", "c", "d", "e"}, nil }, 3, time.Millisecond, nil)
	}()
	for n := 0; n < 3; n++ {
		select {
		case <-p.started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("workers not joined")
	}
	if p.max != 3 || p.active != 0 {
		t.Fatalf("max=%d active=%d", p.max, p.active)
	}
}
