package handoff_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiff/cloud/api/internal/handoff"
	"github.com/kiff/cloud/api/internal/testdb"
)

var t0 = time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC)

func trial(agent string) string { return agent + "~trial" }

// The link is kept as its host only: never its path, query, port or user,
// which may carry a token. Anything but an https URL to a named host is
// refused.
func TestLinkHostKeepsOnlyTheHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://app.qonto.com/transfers/requests/abc?token=SECRET#x": "app.qonto.com",
		"https://APP.Example.COM:8443/path":                           "app.example.com",
		"  https://sign.example.eu/s/123  ":                           "sign.example.eu",
	} {
		got, err := handoff.LinkHost(in)
		if err != nil || got != want {
			t.Errorf("LinkHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"http://app.example.com/x", "javascript:alert(1)", "https://user:pw@example.com/",
		"https://10.0.0.1/x", "https://localhost/x", "app.example.com/x", "intent://scan#Intent;end", ""} {
		if got, err := handoff.LinkHost(in); err == nil {
			t.Errorf("LinkHost(%q) = %q, want refused", in, got)
		}
	}
}

func request() handoff.Request {
	return handoff.Request{What: "Approve 3 supplier payments, €4,210 in all", Why: "Your bank asks you to approve transfers yourself",
		Link: "https://app.qonto.com/requests/r1?t=SECRET", About: "Batch 11 Nov", PreparedBy: []string{"p-1", "p-1", "p-2"}, Operation: "op-1"}
}

func TestValidate(t *testing.T) {
	h, err := request().Validate("t1", "a1", t0)
	if err != nil {
		t.Fatal(err)
	}
	if h.LinkHost != "app.qonto.com" || strings.Contains(h.LinkHost, "SECRET") {
		t.Errorf("link host = %q", h.LinkHost)
	}
	if len(h.PreparedBy) != 2 || h.Status != handoff.Waiting || h.Version != handoff.Version || !h.ExpiresAt.Equal(t0.Add(handoff.Wait)) {
		t.Errorf("hand-off = %+v", h)
	}
	for name, change := range map[string]func(*handoff.Request){
		"no what":      func(r *handoff.Request) { r.What = " " },
		"long why":     func(r *handoff.Request) { r.Why = strings.Repeat("x", handoff.MaxWhy+1) },
		"no operation": func(r *handoff.Request) { r.Operation = "" },
		"bad link":     func(r *handoff.Request) { r.Link = "http://app.qonto.com/x" },
		"control char": func(r *handoff.Request) { r.What = "Approve\x07" },
		"empty call":   func(r *handoff.Request) { r.PreparedBy = []string{""} },
	} {
		r := request()
		change(&r)
		if _, err := r.Validate("t1", "a1", t0); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A hand-off never stands in for a refused or held call: each call that
// prepared it is the agent's own (or its trial's) and was sent.
func TestCheckPrepared(t *testing.T) {
	calls := map[string]handoff.Call{
		"sent":    {Proposal: "tc-sent", Agent: "a1", State: "forwarded"},
		"sending": {Proposal: "tc-sending", Agent: "a1", State: "forwarding"},
		"trial":   {Proposal: "tc-trial", Agent: "a1~trial", State: "forwarded"},
		"refused": {Proposal: "tc-refused", Agent: "a1", State: "refused"},
		"held":    {Proposal: "tc-held", Agent: "a1", State: "held"},
		"other":   {Proposal: "tc-other", Agent: "a2", State: "forwarded"},
	}
	got, err := handoff.CheckPrepared(calls, []string{"sent", "sending", "trial"}, "a1", trial)
	if err != nil || strings.Join(got, ",") != "tc-sent,tc-sending,tc-trial" {
		t.Fatalf("own sent calls: %v, %v", got, err)
	}
	for _, p := range []string{"refused", "held", "other", "missing"} {
		if _, err := handoff.CheckPrepared(calls, []string{"sent", p}, "a1", trial); err == nil {
			t.Errorf("%s: accepted", p)
		}
	}
}

func TestClosed(t *testing.T) {
	answered := t0.Add(time.Minute)
	checked := t0.Add(2 * time.Minute)
	for name, c := range map[string]struct {
		h    handoff.Handoff
		now  time.Time
		want bool
	}{
		"waiting":             {handoff.Handoff{Status: handoff.Waiting}, t0, false},
		"skipped":             {handoff.Handoff{Status: handoff.Skipped, AnsweredAt: &answered}, answered, true},
		"no answer":           {handoff.Handoff{Status: handoff.NoAnswer}, t0, true},
		"done, waits a check": {handoff.Handoff{Status: handoff.Done, AnsweredAt: &answered}, answered.Add(handoff.CheckGrace - time.Second), false},
		"done, checked":       {handoff.Handoff{Status: handoff.Done, AnsweredAt: &answered, CheckedAt: &checked}, checked, true},
		"done, grace over":    {handoff.Handoff{Status: handoff.Done, AnsweredAt: &answered}, answered.Add(handoff.CheckGrace), true},
	} {
		if got := c.h.Closed(c.now); got != c.want {
			t.Errorf("%s: Closed = %v, want %v", name, got, c.want)
		}
	}
}

// The words a receipt carries as a hash change with every word.
func TestWordsHash(t *testing.T) {
	a := handoff.Handoff{What: "w", Why: "y", About: "a"}
	b := a
	b.About = "b"
	if a.WordsHash() == b.WordsHash() || len(a.WordsHash()) != 64 {
		t.Error("words hash doesn't follow the words")
	}
}

func stores(t *testing.T) map[string]handoff.Store {
	ctx := context.Background()
	out := map[string]handoff.Store{"memory": handoff.NewMemory()}
	url := testdb.URL(t)
	if url == "" {
		return out
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := handoff.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE SCHEMA IF NOT EXISTS gateway`,
		`CREATE TABLE IF NOT EXISTS gateway.invocations (key text PRIMARY KEY, tenant_id text NOT NULL, agent_id text NOT NULL,
			operation_id text NOT NULL DEFAULT '', state text NOT NULL, tool_outcome text NOT NULL DEFAULT '', created_at timestamptz NOT NULL)`,
		`ALTER TABLE gateway.invocations ADD COLUMN IF NOT EXISTS operation_id text NOT NULL DEFAULT ''`,
		`DELETE FROM cloud_meta.handoffs WHERE tenant_id IN ('t1', 't2')`,
		`DELETE FROM gateway.invocations WHERE tenant_id IN ('t1', 't2')`,
		`INSERT INTO gateway.invocations (key, tenant_id, agent_id, operation_id, state, tool_outcome, created_at)
			VALUES ('p-1', 't1', 'a1', 'op-read', 'forwarded', 'done', '2026-11-11T16:00:00Z'),
			       ('p-2', 't1', 'a2', 'op-other', 'forwarded', 'done', '2026-11-11T16:00:00Z')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	out["postgres"] = handoff.NewPostgres(pool)
	return out
}

func raise(t *testing.T, s handoff.Store, tenant, agent, op string, at time.Time) handoff.Handoff {
	t.Helper()
	r := request()
	r.Operation = op
	r.What += " (" + op + ")" // each its own step
	h, err := r.Validate(tenant, agent, at)
	if err != nil {
		t.Fatal(err)
	}
	got, created, err := s.Raise(context.Background(), h)
	if err != nil || !created {
		t.Fatalf("raise %s: created %v, %v", op, created, err)
	}
	return got
}

// Raised once per operation, answered once, never past its expiry; a
// check only after "done"; the sweep's reads see what each owes.
func TestStore(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		if m, ok := s.(*handoff.Memory); ok {
			m.PutCall("t1", "p-1", handoff.Call{Operation: "op-read", Agent: "a1", State: "forwarded", ToolOutcome: "done", CreatedAt: t0.Add(-time.Hour)})
			m.PutCall("t1", "p-2", handoff.Call{Operation: "op-other", Agent: "a2", State: "forwarded", ToolOutcome: "done", CreatedAt: t0.Add(-time.Hour)})
		}
		h := raise(t, s, "t1", "a1", "op-1", t0)
		// The same operation answers the same hand-off, after a crash too.
		r := request()
		r.What += " (op-1)"
		again, _ := r.Validate("t1", "a1", t0.Add(time.Minute))
		same, created, err := s.Raise(ctx, again)
		if err != nil || created || same.ID != h.ID {
			t.Fatalf("%s: raised again: %v %v %v", name, same.ID, created, err)
		}
		if got, err := s.Get(ctx, "t2", h.ID); !errors.Is(err, handoff.ErrNotFound) {
			t.Errorf("%s: another account read it: %+v %v", name, got, err)
		}
		open, err := s.Open(ctx, "t1", t0, 10)
		if err != nil || len(open) != 1 || open[0].LinkHost != "app.qonto.com" {
			t.Fatalf("%s: open = %+v, %v", name, open, err)
		}
		if _, err := s.Check(ctx, "t1", h.ID, "p-1", t0); !errors.Is(err, handoff.ErrNotChecked) {
			t.Errorf("%s: checked before done: %v", name, err)
		}
		dev := &handoff.Device{KeyID: "d1", PublicKey: []byte{1, 2}, Message: "m", Signature: "s"}
		done, err := s.Answer(ctx, "t1", h.ID, handoff.Done, "user_1", "", dev, t0.Add(time.Minute))
		if err != nil || done.Status != handoff.Done || done.Device == nil || done.Device.KeyID != "d1" || done.AnsweredAt == nil {
			t.Fatalf("%s: answer = %+v, %v", name, done, err)
		}
		if cur, err := s.Answer(ctx, "t1", h.ID, handoff.Skipped, "user_2", "", nil, t0.Add(2*time.Minute)); !errors.Is(err, handoff.ErrAnswered) || cur.Status != handoff.Done {
			t.Errorf("%s: answered twice: %+v %v", name, cur, err)
		}
		checked, err := s.Check(ctx, "t1", h.ID, "p-1", t0.Add(3*time.Minute))
		if err != nil || checked.CheckedBy != "p-1" {
			t.Fatalf("%s: check = %+v, %v", name, checked, err)
		}
		if _, err := s.Check(ctx, "t1", h.ID, "p-1", t0.Add(4*time.Minute)); !errors.Is(err, handoff.ErrNotChecked) {
			t.Errorf("%s: checked twice: %v", name, err)
		}
		// An unanswered one expires as no_answer, never done; an answer
		// after its expiry is refused.
		late := raise(t, s, "t1", "a1", "op-2", t0)
		if _, err := s.Answer(ctx, "t1", late.ID, handoff.Done, "user_1", "", nil, late.ExpiresAt); !errors.Is(err, handoff.ErrAnswered) {
			t.Errorf("%s: answered at expiry: %v", name, err)
		}
		expired, err := s.Expire(ctx, late.ExpiresAt, 10)
		if err != nil || len(expired) != 1 || expired[0].ID != late.ID || expired[0].Status != handoff.NoAnswer {
			t.Fatalf("%s: expired = %+v, %v", name, expired, err)
		}
		owed, err := s.Unreceipted(ctx, t0.Add(5*time.Minute), 10)
		if err != nil || len(owed) != 2 {
			t.Fatalf("%s: unreceipted = %d, %v", name, len(owed), err)
		}
		settled := t0.Add(5 * time.Minute)
		for _, x := range owed {
			if err := s.Receipted(ctx, "t1", x.ID, &settled); err != nil {
				t.Fatal(err)
			}
		}
		if owed, _ := s.Unreceipted(ctx, t0.Add(5*time.Minute), 10); len(owed) != 0 {
			t.Errorf("%s: still owed after receipts: %d", name, len(owed))
		}
		calls, err := s.Calls(ctx, "t1", []string{"p-1", "p-x"})
		if err != nil || len(calls) != 1 || calls["p-1"].Agent != "a1" || !calls["p-1"].Sent() || calls["p-1"].ToolOutcome != "done" {
			t.Errorf("%s: calls = %+v, %v", name, calls, err)
		}
		// By operation id, only the agents asked about.
		ops, err := s.Operations(ctx, "t1", []string{"a1", "a1~trial"}, []string{"op-read", "op-other", "op-missing"})
		if err != nil || len(ops) != 1 || ops["op-read"].Proposal != "p-1" {
			t.Errorf("%s: operations = %+v, %v", name, ops, err)
		}
	}
}

// A taf in a loop can't fill its owner's list.
func TestRaiseCap(t *testing.T) {
	for name, s := range stores(t) {
		for i := range handoff.MaxWaitingPerAgent {
			raise(t, s, "t2", "a9", "cap-"+string(rune('a'+i)), t0)
		}
		r := request()
		r.Operation = "one-too-many"
		h, _ := r.Validate("t2", "a9", t0)
		if _, _, err := s.Raise(context.Background(), h); !errors.Is(err, handoff.ErrTooMany) {
			t.Errorf("%s: over the cap: %v", name, err)
		}
		// Another agent has its own room.
		raise(t, s, "t2", "a8", "other-agent", t0)
	}
}

// A later run that finds the same work hands nothing over twice: the same
// what and about, still waiting, answers the step already there, whatever
// the operation; once it's answered, the same words raise a new one.
func TestRaiseSameStepAgain(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		first := raise(t, s, "t1", "a1", "run1-op", t0)
		r := request()
		r.Operation, r.What = "run2-op", first.What
		h, _ := r.Validate("t1", "a1", t0.Add(time.Hour))
		got, created, err := s.Raise(ctx, h)
		if err != nil || created || got.ID != first.ID || !got.AlreadyWaiting {
			t.Fatalf("%s: the same step again = %s created %v already %v, %v", name, got.ID, created, got.AlreadyWaiting, err)
		}
		// The same operation is a replay, not "already waiting".
		r.Operation = "run1-op"
		h1, _ := r.Validate("t1", "a1", t0.Add(time.Hour))
		if again, _, _ := s.Raise(ctx, h1); again.AlreadyWaiting {
			t.Errorf("%s: a replay read as already waiting", name)
		}
		// Another agent, or other words, is another step.
		r.Operation = "run2-op-b"
		hb, _ := r.Validate("t1", "a2", t0.Add(time.Hour))
		if other, created, _ := s.Raise(ctx, hb); !created || other.ID == first.ID {
			t.Errorf("%s: another agent's step was taken for a1's", name)
		}
		if _, err := s.Answer(ctx, "t1", first.ID, handoff.Skipped, "user_1", "", nil, t0.Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		r.Operation = "run3-op"
		h3, _ := r.Validate("t1", "a1", t0.Add(3*time.Hour))
		if next, created, _ := s.Raise(ctx, h3); !created || next.ID == first.ID {
			t.Errorf("%s: after an answer, the same words didn't raise a new step", name)
		}
	}
}

// Without a case (about), the same words may be a new batch: never taken
// for the step already waiting.
func TestRaiseSameWordsWithoutCaseIsANewStep(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		r := request()
		r.About, r.Operation = "", "week1-op"
		h1, _ := r.Validate("t1", "a1", t0)
		first, created, err := s.Raise(ctx, h1)
		if err != nil || !created {
			t.Fatalf("%s: first: %v %v", name, created, err)
		}
		r.Operation = "week2-op"
		h2, _ := r.Validate("t1", "a1", t0.Add(7*24*time.Hour-time.Hour))
		second, created, err := s.Raise(ctx, h2)
		if err != nil || !created || second.ID == first.ID || second.AlreadyWaiting {
			t.Errorf("%s: a new batch with the same words and no case = %s created %v already %v, %v", name, second.ID, created, second.AlreadyWaiting, err)
		}
	}
}
