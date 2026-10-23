package proposals_test

import (
	"context"
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
