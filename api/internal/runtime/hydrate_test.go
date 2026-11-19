package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffstate "github.com/kiff/kiff/pkg/kiff/state"
)

func orderMachine() *kiffstate.TransitionMachine {
	return kiffstate.NewTransitionMachine(
		kiffstate.Transition{EventType: "ORDER_PLACED", From: "", To: "CREATED"},
		kiffstate.Transition{EventType: "ORDER_PAID", From: "CREATED", To: "PAID"},
		kiffstate.Transition{EventType: "ORDER_REFUNDED", From: "PAID", To: "REFUNDED"},
	)
}

func ev(id, typ string) kiffevent.Event {
	return kiffevent.Event{ID: id, Type: typ, EntityID: "o1", EntityType: "Order",
		Source: "test", ActorID: "a", OccurredAt: time.Now().UTC()}
}

// State is rebuilt from history on first touch, once, skipping an
// event whose transition did not match (as live ingestion leaves it).
func TestHydratingMachine_ReplaysHistoryOnce(t *testing.T) {
	ctx := context.Background()
	calls := 0
	m := newHydratingMachine(orderMachine(), func(context.Context, string) ([]kiffevent.Event, error) {
		calls++
		return []kiffevent.Event{ev("e1", "ORDER_PLACED"), ev("e2", "ORDER_REFUNDED"), ev("e3", "ORDER_PAID")}, nil
	})

	for i := 0; i < 2; i++ {
		st, ok, err := m.Current(ctx, "o1")
		if err != nil || !ok {
			t.Fatalf("Current: ok=%v err=%v", ok, err)
		}
		if st.Value != "PAID" || st.Version != 2 {
			t.Fatalf("state = %s v%d, want PAID v2", st.Value, st.Version)
		}
	}
	if calls != 1 {
		t.Errorf("history read %d times, want 1", calls)
	}
}

// The framework appends an event before applying it. Hydrating ahead of
// the append means the new event is applied once, on top of history.
func TestHydratingMachine_HydrateBeforeAppendAppliesNewEventOnce(t *testing.T) {
	ctx := context.Background()
	stored := []kiffevent.Event{ev("e1", "ORDER_PLACED"), ev("e2", "ORDER_PAID")}
	m := newHydratingMachine(orderMachine(), func(context.Context, string) ([]kiffevent.Event, error) {
		return append([]kiffevent.Event(nil), stored...), nil
	})

	// What IngestEvent does, with the store's BeforeAppend hook.
	if err := m.hydrate(ctx, "o1"); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	refund := ev("e3", "ORDER_REFUNDED")
	stored = append(stored, refund)
	current, _, err := m.Current(ctx, "o1")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	next, err := m.Apply(ctx, current, refund)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if next.Value != "REFUNDED" || next.Version != 3 {
		t.Fatalf("state = %s v%d, want REFUNDED v3", next.Value, next.Version)
	}
}

func TestHydratingMachine_UnknownEntityAndRetryAfterError(t *testing.T) {
	ctx := context.Background()
	fail := true
	m := newHydratingMachine(orderMachine(), func(context.Context, string) ([]kiffevent.Event, error) {
		if fail {
			return nil, errors.New("db down")
		}
		return nil, nil
	})
	if _, _, err := m.Current(ctx, "o1"); err == nil {
		t.Fatal("a failed history read must surface, not read as no state")
	}
	fail = false
	if _, ok, err := m.Current(ctx, "o1"); err != nil || ok {
		t.Fatalf("entity with no history: ok=%v err=%v, want not found", ok, err)
	}
}

// failingOnce fails the Nth Apply once, after the earlier ones have
// already been stored, as a cancelled context mid-replay would.
type failingOnce struct {
	*kiffstate.TransitionMachine
	failAt, calls int
}

func (f *failingOnce) Apply(ctx context.Context, cur kiffstate.State, e kiffevent.Event) (kiffstate.State, error) {
	f.calls++
	if f.calls == f.failAt {
		return kiffstate.State{}, context.Canceled
	}
	return f.TransitionMachine.Apply(ctx, cur, e)
}

// A replay that fails partway must not leave its partial
// state as the entity's state; the next touch replays again in full.
func TestHydratingMachine_PartialReplayIsRetriedInFull(t *testing.T) {
	ctx := context.Background()
	inner := &failingOnce{TransitionMachine: orderMachine(), failAt: 2}
	m := newHydratingMachine(inner, func(context.Context, string) ([]kiffevent.Event, error) {
		return []kiffevent.Event{ev("e1", "ORDER_PLACED"), ev("e2", "ORDER_PAID")}, nil
	})

	if _, _, err := m.Current(ctx, "o1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("first touch: err = %v, want the replay failure", err)
	}
	if st, ok, _ := inner.TransitionMachine.Current(ctx, "o1"); !ok || st.Value != "CREATED" {
		t.Fatalf("precondition: the failed replay should have left CREATED behind, got %q ok=%v", st.Value, ok)
	}
	st, ok, err := m.Current(ctx, "o1")
	if err != nil || !ok || st.Value != "PAID" || st.Version != 2 {
		t.Fatalf("after retry: %s v%d ok=%v err=%v, want PAID v2", st.Value, st.Version, ok, err)
	}
}
