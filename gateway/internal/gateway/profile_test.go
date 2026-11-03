package gateway_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// A call keeps the profile it was decided under, and a held call whose
// tool moved to another profile before it was sent is never sent under
// the new one: the owner's approval was for the old one (connection profiles: a call keeps the profile it was decided under). A call under the same profile is collected as before.
func TestAHeldCallIsNotSentUnderAnotherProfile(t *testing.T) {
	r := newRig(t)
	one, two := int64(1), int64(2)
	under := func(rev *int64) {
		tool := refundTool
		tool.ProfileRevision = rev
		r.gw.Catalog = catalog{tool}
	}
	under(&one)
	for _, op := range []string{"op-kept", "op-moved"} {
		if got := r.call(t, "agent-key", amount(5000, op)); !strings.Contains(got.text, "Waiting") {
			t.Fatalf("%s held: %+v", op, got)
		}
	}
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.invocations WHERE profile_revision = 1`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("calls recorded under revision 1: %d %v", n, err)
	}
	r.api.approveAll()
	if got := r.call(t, "agent-key", amount(5000, "op-kept")); got.isError || r.tool.count() != 1 {
		t.Fatalf("approved under the same profile: %+v, sent %d", got, r.tool.count())
	}
	under(&two)
	got := r.call(t, "agent-key", amount(5000, "op-moved"))
	if !got.isError || !strings.Contains(got.text, "profile changed") || r.tool.count() != 1 {
		t.Fatalf("approved, then the profile moved: %+v, sent %d", got, r.tool.count())
	}
	if reasons, _ := got.call["reasons"].([]any); len(reasons) != 1 || reasons[0] != gateway.ReasonProfileChanged {
		t.Fatalf("reasons: %v", got.call["reasons"])
	}
	// Asked again, it stays unsent: the refusal is recorded.
	if got := r.call(t, "agent-key", amount(5000, "op-moved")); !strings.Contains(got.text, "profile changed") || r.tool.count() != 1 {
		t.Fatalf("retried: %+v, sent %d", got, r.tool.count())
	}
}
