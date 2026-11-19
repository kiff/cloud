package handlers

import (
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

func TestParseTaskLimit(t *testing.T) {
	t.Parallel()
	money := []struct {
		in   string
		want int64
	}{
		{"1000", 100000}, {"1,000", 100000}, {"1 000", 100000}, {"€1000", 100000},
		{"250.50", 25050}, {"250,50", 25050}, {"250.5", 25050}, {"1,000.25", 100025},
		{"0.01", 1},
	}
	for _, c := range money {
		if got, err := parseTaskLimit(c.in, true); err != nil || got != c.want {
			t.Errorf("parseTaskLimit(%q, money) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "0", "-5", "abc", "1.234", "12e3", "1000000000000000"} {
		if _, err := parseTaskLimit(bad, true); err == nil {
			t.Errorf("parseTaskLimit(%q, money) accepted", bad)
		}
	}
	if got, err := parseTaskLimit("1,200", false); err != nil || got != 1200 {
		t.Errorf("count 1,200 = %d, %v", got, err)
	}
	for _, bad := range []string{"0", "2.5", "x"} {
		if _, err := parseTaskLimit(bad, false); err == nil {
			t.Errorf("parseTaskLimit(%q, count) accepted", bad)
		}
	}
}

func TestTaskCardID(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	refunds, _ := pages.CardTaskBySlug("refunds")

	if id, upd := taskCardID("Refund Agent", refunds, nil, now); id != "refund-agent-refunds" || upd {
		t.Errorf("fresh = %q, %v", id, upd)
	}
	own := func(id string) apiclient.Mandate {
		return apiclient.Mandate{ID: id, Subject: "refund-agent", Domain: refunds.Template}
	}
	active := []apiclient.Mandate{own("refund-agent-refunds")}
	if id, upd := taskCardID("refund-agent", refunds, active, now); id != "refund-agent-refunds" || !upd {
		t.Errorf("same job again should change the card in force, got %q, %v", id, upd)
	}
	revoked := own("refund-agent-refunds")
	revoked.RevokedAt = &past
	if id, upd := taskCardID("refund-agent", refunds, []apiclient.Mandate{revoked}, now); id != "refund-agent-refunds-2" || upd {
		t.Errorf("a revoked name must not be reused, got %q, %v", id, upd)
	}
	expired := own("refund-agent-refunds")
	expired.ValidUntil = &past
	expiredThenActive := []apiclient.Mandate{expired, own("refund-agent-refunds-2")}
	if id, upd := taskCardID("refund-agent", refunds, expiredThenActive, now); id != "refund-agent-refunds-2" || !upd {
		t.Errorf("should update the card in force after the expired one, got %q, %v", id, upd)
	}
}

// Card names are slugs, so another agent's Card, or one this agent holds
// only as a later holder, can sit under the name. Issuing never changes
// it: that would move it to this agent and cancel its held actions.
func TestCardID_NeverTakesAnotherAgentsCard(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	refunds, _ := pages.CardTaskBySlug("refunds")
	cards := []apiclient.Mandate{
		{ID: "refund-agent-refunds", Subject: "refund_agent", Domain: refunds.Template},
		{ID: "refund-agent-refunds-2", Subject: "billing-agent", Subjects: []string{"billing-agent", "refund-agent"}, Domain: refunds.Template},
		{ID: "refund-agent-refunds-3", Subject: "refund-agent", Domain: "another-domain"},
	}
	if id, upd := taskCardID("refund-agent", refunds, cards, now); id != "refund-agent-refunds-4" || upd {
		t.Errorf("task card = %q, %v; want a new refund-agent-refunds-4", id, upd)
	}
	if id, upd := taskCardID("refund_agent", refunds, cards, now); id != "refund-agent-refunds" || !upd {
		t.Errorf("refund_agent's own card = %q, %v; want it changed", id, upd)
	}

	// The unnamed-limit default on the card form follows the same rule.
	limits := []apiclient.Mandate{{ID: "limit-refund-agent", Subject: "refund_agent", Domain: "refunds"}}
	if id, upd := ownCardID(mandateIDFromSubject("refund-agent"), limits, now, func(m apiclient.Mandate) bool {
		return isCardOf(m, "refund-agent", "refunds")
	}); id != "limit-refund-agent-2" || upd {
		t.Errorf("unnamed limit = %q, %v; want limit-refund-agent-2", id, upd)
	}
}

func TestTaskLimitLabel(t *testing.T) {
	t.Parallel()
	refunds, _ := pages.CardTaskBySlug("refunds")
	plans, _ := pages.CardTaskBySlug("plan-changes")
	for _, c := range []struct {
		n    int64
		task pages.CardTask
		want string
	}{{100000, refunds, "€1,000"}, {25050, refunds, "€250.50"}, {123456789, refunds, "€1,234,567.89"}, {40, plans, "40 plan changes"}} {
		if got := taskLimitLabel(c.n, c.task); got != c.want {
			t.Errorf("taskLimitLabel(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
