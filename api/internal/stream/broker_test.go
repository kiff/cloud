package stream

import (
	"testing"
	"time"
)

func TestBroker_PublishReachesTenantSubscribersOnly(t *testing.T) {
	t.Parallel()
	b := NewBroker()
	chA, cancelA := b.Subscribe("tenant-a")
	defer cancelA()
	chB, cancelB := b.Subscribe("tenant-b")
	defer cancelB()

	b.Publish("tenant-a", Event{Type: "decision", Data: map[string]any{"outcome": "allowed"}})

	select {
	case e := <-chA:
		if e.Type != "decision" || e.Data["outcome"] != "allowed" {
			t.Fatalf("unexpected event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("tenant-a subscriber did not receive the event")
	}

	select {
	case e := <-chB:
		t.Fatalf("tenant-b must not receive tenant-a's event, got %+v", e)
	case <-time.After(50 * time.Millisecond):
		// correct: isolated by tenant
	}
}

func TestBroker_CancelUnsubscribesAndCloses(t *testing.T) {
	t.Parallel()
	b := NewBroker()
	ch, cancel := b.Subscribe("t")
	if got := b.SubscriberCount("t"); got != 1 {
		t.Fatalf("SubscriberCount = %d, want 1", got)
	}
	cancel()
	if got := b.SubscriberCount("t"); got != 0 {
		t.Fatalf("after cancel SubscriberCount = %d, want 0", got)
	}
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after cancel")
	}
	cancel() // idempotent, must not panic
}

func TestBroker_PublishNonBlockingWhenBufferFull(t *testing.T) {
	t.Parallel()
	b := NewBroker()
	_, cancel := b.Subscribe("t") // never drained
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < subBuffer+50; i++ {
			b.Publish("t", Event{Type: "decision"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer")
	}
}

func TestBroker_NilSafe(t *testing.T) {
	t.Parallel()
	var b *Broker
	b.Publish("t", Event{Type: "x"}) // no panic
	ch, cancel := b.Subscribe("t")
	cancel()
	_ = ch
	if b.SubscriberCount("t") != 0 {
		t.Fatal("nil broker count must be 0")
	}
}
