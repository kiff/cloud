package alert

import (
	"testing"
	"time"
)

type capture struct{ got []Event }

func (c *capture) Notify(ev Event) { c.got = append(c.got, ev) }

// A refund notice is a refund_notice event about the call, with its kind
// and agent in Reasons, as the phones read it.
func TestRefundsSendsARefundNotice(t *testing.T) {
	c := &capture{}
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	Refunds{Notifier: c}.NotifyRefund("t1", "tc-1", "issue_refund", "agent-a", "refund_after_no", at)
	if len(c.got) != 1 {
		t.Fatalf("sent %d", len(c.got))
	}
	e := c.got[0]
	if e.Outcome != RefundNotice || e.TenantID != "t1" || e.EntityID != "tc-1" || e.ProposalID != "tc-1" || e.EntityType != "ToolCall" ||
		e.Action != "issue_refund" || len(e.Reasons) != 2 || e.Reasons[0] != "refund_after_no" || e.Reasons[1] != "agent-a" || !e.DecidedAt.Equal(at) {
		t.Fatalf("event: %+v", e)
	}
}
