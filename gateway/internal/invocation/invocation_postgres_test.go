package invocation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/testdb"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*invocation.Store, *time.Time) {
	t.Helper()
	pool := testdb.Pool(t)
	if err := invocation.ApplySchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	s := invocation.NewStore(pool)
	now := t0
	s.SetClock(func() time.Time { return now })
	return s, &now
}

func call(cred, op, args string) invocation.Call {
	return invocation.Call{TenantID: "t1", CredentialID: cred, AgentID: "agent", Tool: "issue_refund",
		OperationID: op, Arguments: json.RawMessage(args)}
}

// Ten concurrent identical calls: one invocation, one creator.
func TestConcurrentBeginCreatesOnce(t *testing.T) {
	for _, op := range []string{"", "op-1"} {
		s, _ := newStore(t)
		cred := "cred-concurrent-" + op
		var created atomic.Int32
		keys := make(chan string, 10)
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				in, c, err := s.Begin(context.Background(), call(cred, op, `{"amount_eur":5000}`))
				if err != nil {
					t.Errorf("begin: %v", err)
					return
				}
				if c {
					created.Add(1)
				}
				keys <- in.Key
			}()
		}
		wg.Wait()
		close(keys)
		first := <-keys
		for k := range keys {
			if k != first {
				t.Fatalf("op %q: keys differ: %s %s", op, first, k)
			}
		}
		if created.Load() != 1 {
			t.Fatalf("op %q: created %d times", op, created.Load())
		}
	}
}

// Ten concurrent claims of one approved invocation: one forwarding move.
func TestConcurrentClaimForwardsOnce(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	in, _, err := s.Begin(ctx, call("cred-claim", "op-1", `{"amount_eur":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Transition(ctx, in.Key, []invocation.State{invocation.StateDeciding}, invocation.StateHeld,
		invocation.Update{ExceptionID: "exc-1", ReviewURL: "/needs-you/exc-1"}); !ok || err != nil {
		t.Fatalf("hold: %v %v", ok, err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Transition(ctx, in.Key, []invocation.State{invocation.StateDeciding, invocation.StateHeld},
				invocation.StateForwarding, invocation.Update{})
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			if ok {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("forwarding claimed %d times", won.Load())
	}
	got, _ := s.Get(ctx, in.Key)
	if got.State != invocation.StateForwarding || got.ExceptionID != "exc-1" {
		t.Fatalf("after claim: %+v", got)
	}
}

func TestDedupWindow(t *testing.T) {
	s, now := newStore(t)
	ctx := context.Background()
	first, created, _ := s.Begin(ctx, call("cred-w", "", `{"amount_eur":500,"note":"a"}`))
	if !created {
		t.Fatal("first call not created")
	}
	*now = t0.Add(9 * time.Minute)
	// Same arguments in another key order: the same call.
	again, created, _ := s.Begin(ctx, call("cred-w", "", `{"note":"a","amount_eur":500}`))
	if created || again.Key != first.Key {
		t.Fatalf("retry inside the window was a new invocation")
	}
	other, created, _ := s.Begin(ctx, call("cred-w", "", `{"amount_eur":501,"note":"a"}`))
	if !created || other.Key == first.Key {
		t.Fatal("different arguments were treated as a retry")
	}
	*now = t0.Add(11 * time.Minute)
	later, created, _ := s.Begin(ctx, call("cred-w", "", `{"amount_eur":500,"note":"a"}`))
	if !created || later.Key == first.Key {
		t.Fatal("an identical call after the window was treated as a retry")
	}
}

func TestOperationID(t *testing.T) {
	s, now := newStore(t)
	ctx := context.Background()
	first, _, _ := s.Begin(ctx, call("cred-op", "op-a", `{"amount_eur":500}`))
	*now = t0.Add(48 * time.Hour)
	again, created, err := s.Begin(ctx, call("cred-op", "op-a", `{"amount_eur":500}`))
	if err != nil || created || again.Key != first.Key {
		t.Fatalf("operation id retry after the window: %v %v", created, err)
	}
	if _, _, err := s.Begin(ctx, call("cred-op", "op-a", `{"amount_eur":900}`)); !errors.Is(err, invocation.ErrOperationReused) {
		t.Fatalf("reused id with other arguments: %v", err)
	}
	second, created, _ := s.Begin(ctx, call("cred-op", "op-b", `{"amount_eur":500}`))
	if !created || second.Key == first.Key {
		t.Fatal("a new operation id was not a new invocation")
	}
	foreign, created, _ := s.Begin(ctx, call("cred-other", "op-a", `{"amount_eur":500}`))
	if !created || foreign.Key == first.Key {
		t.Fatal("another credential's operation id collided")
	}
}

func TestTransitionRecordsResult(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	in, _, _ := s.Begin(ctx, call("cred-r", "op-r", `{}`))
	s.Transition(ctx, in.Key, []invocation.State{invocation.StateDeciding}, invocation.StateForwarding, invocation.Update{})
	ok, err := s.Transition(ctx, in.Key, []invocation.State{invocation.StateForwarding}, invocation.StateForwarded,
		invocation.Update{Result: json.RawMessage(`{"content":[{"type":"text","text":"done"}]}`)})
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	got, _ := s.Get(ctx, in.Key)
	if got.State != invocation.StateForwarded || !json.Valid(got.Result) || string(got.Result) == "" {
		t.Fatalf("%+v", got)
	}
	if ok, _ := s.Transition(ctx, in.Key, []invocation.State{invocation.StateForwarding}, invocation.StateUnknown, invocation.Update{}); ok {
		t.Fatal("a stale transition won")
	}
}

// Recovery never lets a handler that was presumed dead overwrite the
// record: its late transition loses.
func TestRecoveredCallCannotBeOverwritten(t *testing.T) {
	s, now := newStore(t)
	ctx := context.Background()
	in, _, _ := s.Begin(ctx, call("cred-stale", "op-s", `{}`))
	s.Transition(ctx, in.Key, []invocation.State{invocation.StateDeciding}, invocation.StateForwarding, invocation.Update{})
	if n, _ := s.RecoverStale(ctx, in.Key, t0.Add(-time.Second)); n != 0 {
		t.Fatal("a call inside its lease was recovered")
	}
	*now = t0.Add(5 * time.Minute)
	if n, err := s.RecoverStale(ctx, in.Key, t0); n != 1 || err != nil {
		t.Fatalf("recover: %d %v", n, err)
	}
	if ok, _ := s.Transition(ctx, in.Key, []invocation.State{invocation.StateForwarding}, invocation.StateForwarded, invocation.Update{}); ok {
		t.Fatal("a late handler overwrote the recovered call")
	}
	got, _ := s.Get(ctx, in.Key)
	if got.State != invocation.StateUnknown || got.Message != invocation.StaleForwardingMessage {
		t.Fatalf("%+v", got)
	}
}

// apps/api erases a tenant's gateway data by deleting from every table in
// the gateway schema that has a tenant_id column. A table without one
// would silently keep a deleted account's data.
func TestEveryGatewayTableIsErasable(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	rows, err := s.Pool().Query(ctx, `
		SELECT t.table_name FROM information_schema.tables t
		 WHERE t.table_schema = 'gateway' AND t.table_type = 'BASE TABLE'
		   AND NOT EXISTS (SELECT 1 FROM information_schema.columns c
		                    WHERE c.table_schema = 'gateway' AND c.table_name = t.table_name AND c.column_name = 'tenant_id')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		t.Errorf("gateway.%s has no tenant_id column, so account erasure cannot reach it", name)
	}
	in, _, err := s.Begin(ctx, call("cred-erase", "", `{"amount_eur":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var slotTenant string
	if err := s.Pool().QueryRow(ctx, `SELECT tenant_id FROM gateway.dedup_slots WHERE invocation_key = $1`, in.Key).Scan(&slotTenant); err != nil || slotTenant != "t1" {
		t.Fatalf("dedup slot tenant = %q, %v", slotTenant, err)
	}
}

// A read-only call without an operation id joins an identical call only
// while that call is unfinished: concurrent identical reads share
// one; a held read is joined whatever the time, so its retry can collect
// the owner's answer; once a read has finished, the next identical read
// is a new call. With an operation id, a retry still replays.
func TestReadOnlyCallsJoinOnlyUnfinished(t *testing.T) {
	s, now := newStore(t)
	ctx := context.Background()
	ro := func(op string) invocation.Call {
		c := call("cred-ro", op, `{"account":"a-1"}`)
		c.Tool, c.ReadOnly = "get_balance", true
		return c
	}
	var created atomic.Int32
	keys := make(chan string, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in, c, err := s.Begin(ctx, ro(""))
			if err != nil {
				t.Errorf("begin: %v", err)
				return
			}
			if c {
				created.Add(1)
			}
			keys <- in.Key
		}()
	}
	wg.Wait()
	close(keys)
	first := <-keys
	for k := range keys {
		if k != first {
			t.Fatal("identical reads while the first is deciding did not share it")
		}
	}
	if created.Load() != 1 {
		t.Fatalf("created %d times", created.Load())
	}
	finish := func(key string, to invocation.State) {
		t.Helper()
		if ok, err := s.Transition(ctx, key, []invocation.State{invocation.StateDeciding, invocation.StateHeld}, to, invocation.Update{}); !ok || err != nil {
			t.Fatalf("transition to %s: %v %v", to, ok, err)
		}
	}

	finish(first, invocation.StateForwarded)
	*now = t0.Add(time.Minute)
	second, c, _ := s.Begin(ctx, ro(""))
	if !c || second.Key == first {
		t.Fatal("a read after the first was forwarded joined it (stale answer)")
	}

	finish(second.Key, invocation.StateHeld)
	*now = t0.Add(3 * time.Hour)
	joined, c, _ := s.Begin(ctx, ro(""))
	if c || joined.Key != second.Key {
		t.Fatal("a held read was not joined after the window, so its approval could not be collected")
	}

	finish(second.Key, invocation.StateRefused)
	third, c, _ := s.Begin(ctx, ro(""))
	if !c || third.Key == second.Key {
		t.Fatal("a read after a refused one joined it")
	}

	opFirst, _, _ := s.Begin(ctx, ro("op-ro"))
	opAgain, c, _ := s.Begin(ctx, ro("op-ro"))
	if c || opAgain.Key != opFirst.Key {
		t.Fatal("a read-only retry with its operation id was not replayed")
	}
}

// Identical first calls released together, on a real clock, share one
// call: one creator and one key per round, for read-only tools and for
// tools that act. (A frozen clock gives every goroutine the same key and
// would hide a race.)
func TestConcurrentFirstCallsShareOneCallRealClock(t *testing.T) {
	s, _ := newStore(t)
	s.SetClock(time.Now)
	ctx := context.Background()
	run := time.Now().UnixNano() // tables persist across -count runs
	for _, readOnly := range []bool{true, false} {
		for round := 0; round < 20; round++ {
			c := call(fmt.Sprintf("cred-real-%v-%d", readOnly, run), "", fmt.Sprintf(`{"account":"a-%d"}`, round))
			c.ReadOnly = readOnly
			start := make(chan struct{})
			var created atomic.Int32
			keys := make(chan string, 10)
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					in, cr, err := s.Begin(ctx, c)
					if err != nil {
						t.Errorf("begin: %v", err)
						return
					}
					if cr {
						created.Add(1)
					}
					keys <- in.Key
				}()
			}
			close(start)
			wg.Wait()
			close(keys)
			distinct := map[string]bool{}
			for k := range keys {
				distinct[k] = true
			}
			if created.Load() != 1 || len(distinct) != 1 {
				t.Fatalf("read_only=%v round %d: created %d, %d distinct keys; want 1 and 1", readOnly, round, created.Load(), len(distinct))
			}
		}
	}
}

// A read after a finished one is a new call even at the same instant: the
// new key is derived from the one it replaces.
func TestReadOnlyTakeoverKeyDiffersAtSameInstant(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	c := call("cred-same", "", `{"account":"a-1"}`)
	c.ReadOnly = true
	first, _, _ := s.Begin(ctx, c)
	if ok, err := s.Transition(ctx, first.Key, []invocation.State{invocation.StateDeciding}, invocation.StateForwarded, invocation.Update{}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	second, created, err := s.Begin(ctx, c)
	if err != nil || !created || second.Key == first.Key || second.State != invocation.StateDeciding {
		t.Fatalf("read at the same instant after a finished one: created=%v key same=%v state=%s err=%v",
			created, second.Key == first.Key, second.State, err)
	}
}

// A call keeps its arguments only while it can still be forwarded: a
// held call needs them for when the owner allows it; a finished one
// never sends again, so they are dropped and only args_hash stays.
func TestFinishedCallsDropArguments(t *testing.T) {
	s, now := newStore(t)
	ctx := context.Background()
	args := `{"order":"A-1","email":"alice@example.com","amount":30}`

	held, _, _ := s.Begin(ctx, call("cred-min", "op-held", args))
	s.Transition(ctx, held.Key, []invocation.State{invocation.StateDeciding}, invocation.StateHeld, invocation.Update{})
	if got, _ := s.Get(ctx, held.Key); string(got.Arguments) == "{}" {
		t.Fatal("a held call lost the arguments it needs to be forwarded")
	}

	for _, to := range []invocation.State{invocation.StateRefused, invocation.StateForwarded, invocation.StateFailed, invocation.StateUnknown} {
		in, _, _ := s.Begin(ctx, call("cred-min", "op-"+string(to), args))
		if ok, err := s.Transition(ctx, in.Key, []invocation.State{invocation.StateDeciding}, to, invocation.Update{}); !ok || err != nil {
			t.Fatal(to, ok, err)
		}
		got, _ := s.Get(ctx, in.Key)
		if string(got.Arguments) != "{}" || got.ArgsHash != in.ArgsHash {
			t.Fatalf("%s kept %s (hash %q)", to, got.Arguments, got.ArgsHash)
		}
		// The hash still tells a retry from a reused operation id.
		if _, _, err := s.Begin(ctx, call("cred-min", "op-"+string(to), `{"amount":31}`)); !errors.Is(err, invocation.ErrOperationReused) {
			t.Fatalf("%s: reuse not detected: %v", to, err)
		}
		if _, created, err := s.Begin(ctx, call("cred-min", "op-"+string(to), args)); created || err != nil {
			t.Fatalf("%s: retry: %v %v", to, created, err)
		}
	}

	// A call recovered after its process died is finished too.
	stale, _, _ := s.Begin(ctx, call("cred-min", "op-stale", args))
	s.Transition(ctx, stale.Key, []invocation.State{invocation.StateDeciding}, invocation.StateForwarding, invocation.Update{})
	*now = t0.Add(time.Hour)
	if n, err := s.RecoverStale(ctx, stale.Key, t0); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if got, _ := s.Get(ctx, stale.Key); string(got.Arguments) != "{}" {
		t.Fatalf("recovered call kept %s", got.Arguments)
	}
}

// The tool's result is kept for retries, then dropped.
func TestPurgeResults(t *testing.T) {
	s, now := newStore(t)
	ctx := context.Background()
	in, _, _ := s.Begin(ctx, call("cred-purge", "op-p", `{}`))
	s.Transition(ctx, in.Key, []invocation.State{invocation.StateDeciding}, invocation.StateForwarding, invocation.Update{})
	s.Transition(ctx, in.Key, []invocation.State{invocation.StateForwarding}, invocation.StateForwarded,
		invocation.Update{Result: json.RawMessage(`{"content":[{"type":"text","text":"refunded alice"}]}`)})
	if _, err := s.PurgeResults(ctx, t0.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, in.Key); len(got.Result) == 0 {
		t.Fatal("result purged inside its retention")
	}
	*now = t0.Add(25 * time.Hour)
	if _, err := s.PurgeResults(ctx, t0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, in.Key)
	if len(got.Result) != 0 || got.State != invocation.StateForwarded {
		t.Fatalf("%+v", got)
	}
}
