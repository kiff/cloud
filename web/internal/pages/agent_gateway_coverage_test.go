package pages

import (
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// A tool row says which Card covers it; a revoked Card covers nothing, and
// a tool no Card covers is refused by the gateway.
func TestToolCoverageIsACardInForce(t *testing.T) {
	gone := time.Now().Add(-time.Hour)
	a := AgentSummary{ID: "a", Cards: []apiclient.Mandate{
		{ID: "old", Grants: []apiclient.MandateGrant{{Action: "refund_order"}}, RevokedAt: &gone},
		{ID: "live", Grants: []apiclient.MandateGrant{{Action: "refund_order"}}},
	}}
	if m, ok := toolCoverage(a, "refund_order"); !ok || m.ID != "live" {
		t.Errorf("refund_order covered by %q (%v), want live", m.ID, ok)
	}
	if _, ok := toolCoverage(a, "get_order"); ok {
		t.Error("get_order has no Card but reads as covered")
	}
}
