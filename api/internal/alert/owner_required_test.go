package alert_test

import (
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
)

// From a review: an owner_required decision is delivered to a target
// subscribed to it, and not to one subscribed only to blocked.
func TestDispatcher_DeliversOwnerRequired(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-owner", TenantID: "T", URL: "https://h/owner", Outcomes: []string{"owner_required"}, Enabled: true})
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-blocked", TenantID: "T", URL: "https://h/blocked", Outcomes: []string{"blocked"}, Enabled: true})
	sink := newCaptureSink(4)
	d := alert.NewDispatcher(store, sink, alert.WithBaseURL("https://app.kiff.dev"))
	d.Notify(alert.Event{TenantID: "T", Action: "REFUND_ORDER", EntityID: "o1", Outcome: "owner_required",
		Reasons: []string{"mandate_owner_required"}, DecidedAt: time.Now().UTC()})
	sink.waitFor(t, 1)
	time.Sleep(100 * time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.sent) != 1 || sink.sent[0].URL != "https://h/owner" {
		t.Fatalf("sends = %+v", sink.sent)
	}
}
