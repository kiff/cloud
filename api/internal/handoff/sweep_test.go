package handoff_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/handoff"
	"github.com/kiff/cloud/api/internal/receipt"
)

type notes struct {
	mu  sync.Mutex
	got []alert.Event
}

func (n *notes) Notify(ev alert.Event) { n.mu.Lock(); n.got = append(n.got, ev); n.mu.Unlock() }

func sweeper(s handoff.Store) (*handoff.Sweeper, *receipt.MemoryStore, *notes) {
	rs, n := receipt.NewMemoryStore(), &notes{}
	return &handoff.Sweeper{Store: s, Receipts: rs, Alerts: n, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, rs, n
}

// Each hand-off gets its raised receipt, and its settled one once it
// closed: "skipped" at once, "done" with the taf's read once named, and an
// unanswered one as no_answer, with its runtime told. No receipt carries
// the words or the link's path.
func TestSweepReceipts(t *testing.T) {
	ctx := context.Background()
	s := handoff.NewMemory()
	s.PutCall("t1", "read-1", handoff.Call{Operation: "op-read", Agent: "a1", State: "forwarded", ToolOutcome: "done", CreatedAt: t0.Add(2 * time.Minute)})
	sw, rs, n := sweeper(s)

	done := raise(t, s, "t1", "a1", "op-done", t0)
	skipped := raise(t, s, "t1", "a1", "op-skip", t0)
	silent := raise(t, s, "t1", "a1", "op-silent", t0)
	sw.Sweep(ctx, t0)
	for _, h := range []handoff.Handoff{done, skipped, silent} {
		r, err := rs.Get(ctx, "t1", receipt.HandoffReceiptTraceID(h.ID))
		if err != nil || r.Outcome != receipt.HandoffOutcome || r.ActionName != receipt.HandoffAction || r.EntityID != "a1" {
			t.Fatalf("raised receipt of %s: %+v, %v", h.ID, r, err)
		}
		b, _ := json.Marshal(r)
		for _, secret := range []string{"Approve 3 supplier", "Your bank asks", "Batch 11 Nov", "SECRET", "/requests/"} {
			if strings.Contains(string(b), secret) {
				t.Errorf("receipt carries %q: %s", secret, b)
			}
		}
		if r.CausalChain[0].Data["link_host"] != "app.qonto.com" || r.CausalChain[0].Data["words_sha256"] != h.WordsHash() {
			t.Errorf("raised step data = %+v", r.CausalChain[0].Data)
		}
	}

	answered := t0.Add(time.Minute)
	if _, err := s.Answer(ctx, "t1", done.ID, handoff.Done, "user_1", "", nil, answered); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Answer(ctx, "t1", skipped.ID, handoff.Skipped, "user_1", "", nil, answered); err != nil {
		t.Fatal(err)
	}
	sw.Sweep(ctx, answered)
	if r, err := rs.Get(ctx, "t1", receipt.HandoffSettledTraceID(skipped.ID)); err != nil || r.Outcome != "skipped" {
		t.Errorf("skipped settles at once: %+v, %v", r, err)
	}
	if _, err := rs.Get(ctx, "t1", receipt.HandoffSettledTraceID(done.ID)); err == nil {
		t.Error("done settled before its check or the grace")
	}
	if _, err := s.Check(ctx, "t1", done.ID, "read-1", t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	sw.Sweep(ctx, t0.Add(3*time.Minute))
	r, err := rs.Get(ctx, "t1", receipt.HandoffSettledTraceID(done.ID))
	if err != nil || r.Outcome != "done" || len(r.CausalChain) != 3 {
		t.Fatalf("done's settled receipt: %+v, %v", r, err)
	}
	if c := r.CausalChain[2]; c.Kind != "handoff_checked" || c.Data["proposal_id"] != "read-1" || c.Data["status"] != "done" {
		t.Errorf("check step = %+v", c)
	}
	if a := r.CausalChain[1]; a.Kind != "handoff_settled" || a.ActorID != "user_1" || a.Data["outcome"] != "done" {
		t.Errorf("answer step = %+v", a)
	}

	sw.Sweep(ctx, silent.ExpiresAt)
	r, err = rs.Get(ctx, "t1", receipt.HandoffSettledTraceID(silent.ID))
	if err != nil || r.Outcome != "no_answer" || r.CausalChain[1].ActorID != "" {
		t.Fatalf("unanswered settled receipt: %+v, %v", r, err)
	}
	if len(n.got) != 1 || n.got[0].Outcome != alert.HandoffAnswered || n.got[0].Handoff == nil ||
		n.got[0].Handoff.ID != silent.ID || n.got[0].Handoff.Status != "no_answer" || n.got[0].EntityID != "a1" {
		t.Errorf("alerts = %+v", n.got)
	}
	// Nothing more is owed, and a second sweep changes nothing.
	if owed, _ := s.Unreceipted(ctx, silent.ExpiresAt, 10); len(owed) != 0 {
		t.Errorf("still owed: %d", len(owed))
	}
}

// "Done" without a read settles once the grace has passed, saying no read
// was named.
func TestDoneWithoutCheckSettlesAfterGrace(t *testing.T) {
	ctx := context.Background()
	s := handoff.NewMemory()
	sw, rs, _ := sweeper(s)
	h := raise(t, s, "t1", "a1", "op", t0)
	if _, err := s.Answer(ctx, "t1", h.ID, handoff.Done, "user_1", "", nil, t0); err != nil {
		t.Fatal(err)
	}
	sw.Sweep(ctx, t0.Add(handoff.CheckGrace))
	r, err := rs.Get(ctx, "t1", receipt.HandoffSettledTraceID(h.ID))
	if err != nil || r.Outcome != "done" || len(r.CausalChain) != 2 {
		t.Fatalf("settled = %+v, %v", r, err)
	}
}
