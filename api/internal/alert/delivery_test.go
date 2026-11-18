package alert_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
)

// blockedSink always refuses with ErrBlockedAddress, modeling the SSRF
// guard rejecting a target at delivery time.
type blockedSink struct{ signal chan struct{} }

func (b blockedSink) Send(_ context.Context, _ alert.Target, _ []byte) error {
	if b.signal != nil {
		b.signal <- struct{}{}
	}
	return alert.ErrBlockedAddress
}

// The dispatcher records a delivery per matching target with the right
// status: delivered on success, failed on sink error, blocked on the
// SSRF guard refusal — all linked to the triggering proposal.
func TestDispatcher_RecordsDeliveries(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-ok", TenantID: "T", URL: "https://h/ok", Enabled: true})
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-fail", TenantID: "T", URL: "https://h/fail", Enabled: true})

	deliveries := alert.NewMemoryDeliveryStore()
	sink := newCaptureSink(8)
	sink.failURL = "https://h/fail"
	d := alert.NewDispatcher(store, sink, alert.WithDeliveryStore(deliveries))

	d.Notify(alert.Event{
		TenantID: "T", ProposalID: "prop-1", Action: "REFUND_ORDER",
		EntityID: "o1", EntityType: "Order", Outcome: "blocked",
		DecidedAt: time.Now().UTC(),
	})
	sink.waitFor(t, 2)
	time.Sleep(100 * time.Millisecond)

	recs, err := deliveries.ListDeliveries(ctx(), "T", "prop-1")
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d deliveries, want 2", len(recs))
	}
	byTarget := map[string]alert.Delivery{}
	for _, r := range recs {
		byTarget[r.TargetID] = r
		if r.ProposalID != "prop-1" || r.Action != "REFUND_ORDER" || r.Outcome != "blocked" {
			t.Errorf("delivery link wrong: %+v", r)
		}
	}
	if byTarget["t-ok"].Status != alert.DeliveryDelivered {
		t.Errorf("t-ok status = %q, want delivered", byTarget["t-ok"].Status)
	}
	if byTarget["t-fail"].Status != alert.DeliveryFailed {
		t.Errorf("t-fail status = %q, want failed", byTarget["t-fail"].Status)
	}
}

// An SSRF-blocked send records a first-class "blocked" delivery, not a
// silent drop.
func TestDispatcher_RecordsBlockedDelivery(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-ssrf", TenantID: "T", URL: "http://169.254.169.254/x", Enabled: true})

	deliveries := alert.NewMemoryDeliveryStore()
	sig := make(chan struct{}, 1)
	d := alert.NewDispatcher(store, blockedSink{signal: sig}, alert.WithDeliveryStore(deliveries))

	d.Notify(alert.Event{
		TenantID: "T", ProposalID: "prop-2", Action: "REFUND_ORDER",
		EntityID: "o2", EntityType: "Order", Outcome: "blocked", DecidedAt: time.Now().UTC(),
	})
	select {
	case <-sig:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for send")
	}
	time.Sleep(100 * time.Millisecond)

	recs, _ := deliveries.ListDeliveries(ctx(), "T", "prop-2")
	if len(recs) != 1 || recs[0].Status != alert.DeliveryBlocked {
		t.Fatalf("want 1 blocked delivery; got %+v", recs)
	}
	if recs[0].Detail == "" {
		t.Error("blocked delivery should carry the guard's detail")
	}
}

// No delivery store wired → dispatch still works, nothing recorded
// (unchanged behavior).
func TestDispatcher_NoDeliveryStoreIsFine(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t1", TenantID: "T", URL: "https://h/ok", Enabled: true})
	sink := newCaptureSink(2)
	d := alert.NewDispatcher(store, sink) // no WithDeliveryStore

	d.Notify(alert.Event{TenantID: "T", ProposalID: "p", Action: "A", Outcome: "blocked", DecidedAt: time.Now().UTC()})
	sink.waitFor(t, 1)
	// No panic, send happened; nothing to assert on a nil store.
}

// MemoryDeliveryStore scopes by tenant + proposal and orders newest first.
func TestMemoryDeliveryStore_ListScopesAndOrders(t *testing.T) {
	t.Parallel()
	s := alert.NewMemoryDeliveryStore()
	now := time.Now().UTC()
	_ = s.RecordDelivery(ctx(), alert.Delivery{ID: "1", TenantID: "T", ProposalID: "p1", TargetID: "a", Status: alert.DeliveryDelivered, AttemptedAt: now.Add(-2 * time.Minute)})
	_ = s.RecordDelivery(ctx(), alert.Delivery{ID: "2", TenantID: "T", ProposalID: "p1", TargetID: "b", Status: alert.DeliveryFailed, AttemptedAt: now.Add(-1 * time.Minute)})
	_ = s.RecordDelivery(ctx(), alert.Delivery{ID: "3", TenantID: "T", ProposalID: "other", TargetID: "c", Status: alert.DeliveryDelivered, AttemptedAt: now})
	_ = s.RecordDelivery(ctx(), alert.Delivery{ID: "4", TenantID: "OTHER", ProposalID: "p1", TargetID: "d", Status: alert.DeliveryDelivered, AttemptedAt: now})

	got, err := s.ListDeliveries(ctx(), "T", "p1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (tenant T, proposal p1)", len(got))
	}
	if got[0].ID != "2" || got[1].ID != "1" {
		t.Errorf("order wrong (want newest first 2,1): %s,%s", got[0].ID, got[1].ID)
	}
}

// The "blocked" status is the headline of G4, and its production path
// is SSRF guard (dialer Control) → *net.OpError → *url.Error → the
// sink's fmt %w. This exercises that REAL chain through the production
// HTTPSink (allowPrivate=false) rather than a stand-in sink: a target
// that resolves to a blocked range must surface an error that
// errors.Is(..., ErrBlockedAddress) still catches end to end. If the
// chain ever stops preserving the sentinel, "blocked" would silently
// downgrade to "failed" — this test is the guard against that.
func TestHTTPSink_BlockedAddressPreservesSentinel(t *testing.T) {
	t.Parallel()
	sink := alert.NewHTTPSink(false) // production posture: SSRF guard on
	// Loopback is a blocked range; the dial Control refuses it before
	// any connect, so this is deterministic and offline.
	target := alert.Target{ID: "t", TenantID: "T", URL: "http://127.0.0.1:9/hook", Enabled: true}

	err := sink.Send(context.Background(), target, []byte(`{}`))
	if err == nil {
		t.Fatal("expected the SSRF dial guard to refuse a loopback target")
	}
	if !errors.Is(err, alert.ErrBlockedAddress) {
		t.Fatalf("errors.Is(err, ErrBlockedAddress) = false through the real dial chain; got %v", err)
	}
}

// End-to-end: the dispatcher wired with the production HTTPSink records
// a first-class "blocked" delivery (not "failed", not a silent drop)
// when the SSRF guard refuses the target — proving the wrapped-error
// path the unit test with the stand-in sink can't.
func TestDispatcher_RecordsBlockedDelivery_RealHTTPSink(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-ssrf", TenantID: "T", URL: "http://127.0.0.1:9/hook", Enabled: true})

	deliveries := alert.NewMemoryDeliveryStore()
	d := alert.NewDispatcher(store, alert.NewHTTPSink(false), alert.WithDeliveryStore(deliveries))

	d.Notify(alert.Event{
		TenantID: "T", ProposalID: "prop-ssrf", Action: "REFUND_ORDER",
		EntityID: "o1", EntityType: "Order", Outcome: "blocked", DecidedAt: time.Now().UTC(),
	})

	// Dispatch is async (Notify spawns a goroutine); poll for the record.
	var recs []alert.Delivery
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		recs, _ = deliveries.ListDeliveries(ctx(), "T", "prop-ssrf")
		if len(recs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(recs) != 1 {
		t.Fatalf("got %d deliveries, want 1", len(recs))
	}
	if recs[0].Status != alert.DeliveryBlocked {
		t.Fatalf("status = %q, want %q (real SSRF chain must record blocked, not failed)", recs[0].Status, alert.DeliveryBlocked)
	}
	if recs[0].Detail == "" {
		t.Error("blocked delivery should carry the guard's detail")
	}
}
