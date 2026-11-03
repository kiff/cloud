package proposals_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/proposals"
)

func TestMemoryReader_ActorFilter(t *testing.T) {
	t.Parallel()
	r := proposals.NewMemoryReader()
	now := time.Now().UTC()
	r.Append("t1", proposals.DecisionRow{ProposalID: "a", ActorID: "agent:refund-bot", Outcome: "allowed", CreatedAt: now})
	r.Append("t1", proposals.DecisionRow{ProposalID: "b", ActorID: "agent:refund-bot", Outcome: "blocked", CreatedAt: now})
	r.Append("t1", proposals.DecisionRow{ProposalID: "c", ActorID: "agent:other", Outcome: "allowed", CreatedAt: now})

	rows, err := r.ListDecisionsInWindow(context.Background(), "t1", proposals.DecisionQuery{Actor: "agent:refund-bot"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("actor filter should return 2 rows, got %d", len(rows))
	}
	for _, row := range rows {
		if row.ActorID != "agent:refund-bot" {
			t.Errorf("leaked another actor: %s", row.ActorID)
		}
	}
	// Counts respect the actor filter too.
	counts, total, _ := r.CountDecisionsByOutcome(context.Background(), "t1", proposals.DecisionQuery{Actor: "agent:refund-bot"})
	if total != 2 || counts["allowed"] != 1 || counts["blocked"] != 1 {
		t.Errorf("actor-scoped counts wrong: total=%d counts=%+v", total, counts)
	}
}

func TestMemoryReader_CountDecisionsOverTime(t *testing.T) {
	t.Parallel()
	r := proposals.NewMemoryReader()
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	// Two decisions in the first minute, one in the third minute.
	r.Append("t1", proposals.DecisionRow{ProposalID: "a", Outcome: "allowed", CreatedAt: base.Add(10 * time.Second)})
	r.Append("t1", proposals.DecisionRow{ProposalID: "b", Outcome: "blocked", CreatedAt: base.Add(40 * time.Second)})
	r.Append("t1", proposals.DecisionRow{ProposalID: "c", Outcome: "allowed", CreatedAt: base.Add(2*time.Minute + 5*time.Second)})

	q := proposals.DecisionQuery{From: base, To: base.Add(5 * time.Minute)}
	buckets, err := r.CountDecisionsOverTime(context.Background(), "t1", q, time.Minute)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("want 2 non-empty minute buckets, got %d: %+v", len(buckets), buckets)
	}
	// Ascending order; first bucket has both outcomes.
	if !buckets[0].Start.Equal(base) {
		t.Errorf("first bucket start = %v, want %v", buckets[0].Start, base)
	}
	if buckets[0].Total != 2 || buckets[0].Counts["allowed"] != 1 || buckets[0].Counts["blocked"] != 1 {
		t.Errorf("first bucket wrong: %+v", buckets[0])
	}
	if buckets[1].Total != 1 || buckets[1].Counts["allowed"] != 1 {
		t.Errorf("second bucket wrong: %+v", buckets[1])
	}
	if !buckets[1].Start.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("second bucket start = %v, want %v", buckets[1].Start, base.Add(2*time.Minute))
	}
}

// A person's own part of the record: every call of the agents they manage
// and their own runs' calls, counted and listed as such.
func TestMemoryReader_ScopeKeepsAPersonsOwnPart(t *testing.T) {
	r := proposals.NewMemoryReader()
	now := time.Now()
	r.Append("t1", proposals.DecisionRow{ProposalID: "theirs", ActorID: "refunds", Outcome: "allowed", CreatedAt: now})
	r.Append("t1", proposals.DecisionRow{ProposalID: "mine", ActorID: "refunds", Outcome: "allowed", CreatedAt: now})
	r.Append("t1", proposals.DecisionRow{ProposalID: "managed", ActorID: "billing", Outcome: "blocked", CreatedAt: now})
	page, err := r.ListCalls(context.Background(), "t1", proposals.CallQuery{Scope: &proposals.CallScope{
		Agents: map[string]bool{"billing": true}, Mine: map[string]bool{"mine": true}}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range page.Calls {
		ids = append(ids, c.ProposalID)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "managed,mine" || page.Counts.All != 2 || strings.Join(page.Agents, ",") != "billing,refunds" {
		t.Fatalf("calls %v, counts %+v, agents %v", ids, page.Counts, page.Agents)
	}
	var nilScope *proposals.CallScope
	if !nilScope.Keeps("any", "any") {
		t.Fatal("a nil scope is the whole account")
	}
}
