package mandate_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/anchor"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/settle"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

type lifecycleTools struct {
	mu sync.Mutex
	m  map[string]tooloutcome.Outcome
}

func (l *lifecycleTools) Outcomes(context.Context, string, []string) (map[string]tooloutcome.Outcome, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]tooloutcome.Outcome{}
	for k, v := range l.m {
		out[k] = v
	}
	return out, nil
}

// countingEmails counts lookups, as the Clerk lookup would make them.
type countingEmails struct {
	mu sync.Mutex
	n  int
}

func (c *countingEmails) EmailForSubject(_ context.Context, subject string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return subject + "@example.com", nil
}

func (c *countingEmails) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// Change Card decides nothing; the call is
// re-decided when the agent asks again. A held call whose Card was
// changed gets no settled receipt until that retry is recorded, however
// long it waits; then the receipt holds the hold, the change, the retry
// and the tool's report, in that order. Without a report it settles a
// grace after the retry, never after the change.
func TestSettledReceiptWaitsForTheRetryAfterChangeCard(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := mandate.NewMemoryStore()
	store.SetClock(clock)
	m := homeCard("card", 50, 200)
	m.TenantID, m.ValidFrom, m.ValidUntil = "t1", now.Add(-time.Hour), now.Add(48*time.Hour)
	m.HoldExpiry = time.Hour
	if err := store.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	// A second account with its own Card, so the first change's room does
	// not let the second call through.
	m2 := m
	m2.TenantID = "t2"
	if err := store.Issue(ctx, m2, "admin"); err != nil {
		t.Fatal(err)
	}
	tenant := "t1"
	authz := mandate.NewAuthorizer(store, nil, clock)
	audit := kiffaudit.NewInMemoryStore()
	n := 0
	record := func(pid, outcome string) {
		n++
		if err := audit.Append(ctx, kiffaudit.Record{ID: "rec-" + string(rune('a'+n)), Kind: kiffaudit.Kind("proposal_decision"), TraceID: pid,
			EntityID: "order-1", EntityType: "Order", ActorID: m.Subject, CreatedAt: now,
			Data: map[string]any{"proposal_id": pid, "action": m.Grants[0].Action, "outcome": outcome}}); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(pid string, want mandate.Outcome) {
		t.Helper()
		d, err := authz.Authorize(ctx, tenant, pid, req(m.Subject, m.Grants[0].Action, "PAID", map[string]int64{"amount_cents": 75}))
		if err != nil || d.Outcome != want {
			t.Fatalf("%s: %+v %v, want %s", pid, d, err, want)
		}
		// What the decide path records: a call within the Card is allowed.
		outcome := string(d.Outcome)
		if d.Outcome == mandate.OutcomeWithinLimit {
			outcome = "allowed"
		}
		record(pid, outcome)
	}
	change := func(pid string) time.Time {
		t.Helper()
		e, ok, err := store.GetException(ctx, tenant, pid)
		if err != nil || !ok {
			t.Fatalf("exception %s: %v", pid, err)
		}
		if _, err := store.ChangeCardRoom(ctx, tenant, e.ID, "admin", now, mandate.RoomThisWindow, time.Time{}); err != nil {
			t.Fatalf("change card %s: %v", pid, err)
		}
		return now
	}
	wallet, err := anchor.NewRandomWallet()
	if err != nil {
		t.Fatal(err)
	}
	tools := &lifecycleTools{m: map[string]tooloutcome.Outcome{}}
	receipts := receipt.NewMemoryStore()
	emails := &countingEmails{}
	pl, err := receipt.NewPipeline(receipts, receipt.NewLocalAttester(wallet, nil), receipt.WithClock(clock),
		receipt.WithSettlements(settle.Source{Exceptions: store, Tools: tools, Now: clock}), receipt.WithAnswererEmails(emails))
	if err != nil {
		t.Fatal(err)
	}
	rt := &kiffruntime.Runtime{Audit: audit}
	sweep := func() int {
		t.Helper()
		built, err := pl.ReconcileSettlements(ctx, tenant, rt)
		if err != nil {
			t.Fatal(err)
		}
		return built
	}

	// p-tool: held, Card changed, no retry for an hour: not settled.
	ask("p-tool", mandate.OutcomeOwnerRequired)
	now = now.Add(5 * time.Minute)
	changedAt := change("p-tool")
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		if built := sweep(); built != 0 {
			t.Fatalf("settled %d before any retry", built)
		}
	}
	// Nothing is looked up for a hold not yet settled.
	if n := emails.count(); n != 0 {
		t.Fatalf("%d email lookups before the hold settled", n)
	}
	// The retry is allowed and the tool reports: now it settles.
	ask("p-tool", mandate.OutcomeWithinLimit)
	retryAt := now
	tools.mu.Lock()
	tools.m["p-tool"] = tooloutcome.Outcome{Status: tooloutcome.Done, At: now.Add(time.Minute)}
	tools.mu.Unlock()
	now = now.Add(2 * time.Minute)
	if built := sweep(); built != 1 {
		t.Fatalf("after the retry and report: built %d, want 1", built)
	}
	if n := emails.count(); n != 1 {
		t.Fatalf("%d email lookups at minting, want 1", n)
	}
	r, err := receipts.Get(ctx, "t1", receipt.SettledReceiptTraceID("p-tool"))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, st := range r.CausalChain {
		kinds = append(kinds, st.Kind)
	}
	if r.Outcome != "card_changed" || strings.Join(kinds, ",") != "proposal_decision,hold_settled,proposal_decision,tool_outcome" ||
		!r.CausalChain[1].OccurredAt.Equal(changedAt) || !r.CausalChain[2].OccurredAt.Equal(retryAt) || r.CausalChain[1].Data["answered_by"] != "admin@example.com" || r.CausalChain[1].Data["answered_by_id"] != "admin" {
		t.Errorf("settled receipt = %v %+v", kinds, r)
	}

	// p-quiet: changed, retried, no tool report: settles a grace after the
	// retry, not after the change.
	tenant = "t2"
	ask("p-quiet", mandate.OutcomeOwnerRequired)
	change("p-quiet")
	now = now.Add(receipt.SettlementGrace + time.Minute)
	ask("p-quiet", mandate.OutcomeWithinLimit)
	if built := sweep(); built != 0 {
		t.Fatalf("settled %d right at the retry, though the change was a grace ago", built)
	}
	if n := emails.count(); n != 1 {
		t.Fatalf("%d email lookups while the grace runs, want still 1", n)
	}
	now = now.Add(receipt.SettlementGrace)
	if built := sweep(); built != 1 {
		t.Fatalf("a grace after the retry: built %d, want 1", built)
	}
	if built := sweep(); built != 0 {
		t.Errorf("a second sweep built %d", built)
	}
	if n := emails.count(); n != 2 {
		t.Errorf("%d email lookups in all, want one per minted receipt (2)", n)
	}
}
