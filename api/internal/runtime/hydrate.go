package runtime

import (
	"context"
	"errors"
	"sync"

	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffstate "github.com/kiff/kiff/pkg/kiff/state"
)

// hydratingMachine wraps a domain's state machine so an entity's state is
// rebuilt from its stored history the first time this runtime touches it
// The framework keeps state only in memory, and the cloud builds
// a fresh runtime on every restart and cache reset; without this every
// deploy left existing entities stateless and their actions refused as
// state_not_allowed.
//
// Hydration happens at most once per entity per runtime, and always
// before the entity's next event is stored (the event store's
// BeforeAppend), because the framework appends an event before applying
// it: a replay after the append would apply the new event twice.
type hydratingMachine struct {
	kiffstate.StateMachine // the domain's machine; Apply and AllowedActions pass through

	history func(ctx context.Context, entityID string) ([]kiffevent.Event, error)

	mu       sync.Mutex
	entities map[string]*entityHydration
}

type entityHydration struct {
	mu   sync.Mutex
	done bool
}

func newHydratingMachine(inner kiffstate.StateMachine, history func(context.Context, string) ([]kiffevent.Event, error)) *hydratingMachine {
	return &hydratingMachine{StateMachine: inner, history: history, entities: map[string]*entityHydration{}}
}

// Current hydrates the entity, then answers from the domain's machine.
func (m *hydratingMachine) Current(ctx context.Context, entityID string) (kiffstate.State, bool, error) {
	if err := m.hydrate(ctx, entityID); err != nil {
		return kiffstate.State{}, false, err
	}
	return m.StateMachine.Current(ctx, entityID)
}

// hydrate replays the entity's history into the domain's machine once.
// A failed read or replay is not remembered, so the next touch retries
// from the start.
func (m *hydratingMachine) hydrate(ctx context.Context, entityID string) error {
	if entityID == "" {
		return nil
	}
	m.mu.Lock()
	h := m.entities[entityID]
	if h == nil {
		h = &entityHydration{}
		m.entities[entityID] = h
	}
	m.mu.Unlock()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		return nil
	}
	// State already in the machine is not proof that replay finished: a
	// replay that failed partway (a cancelled context, say) leaves what it
	// applied so far. Every write reaches the machine through this method
	// first, so an entity not marked done has at most such a partial
	// state, and replaying from a blank state overwrites it: Apply stores
	// the state it computes from the one passed in.
	events, err := m.history(ctx, entityID)
	if err != nil {
		return err
	}
	var current kiffstate.State
	for i, ev := range events {
		if i == 0 {
			current = kiffstate.State{EntityID: ev.EntityID, EntityType: ev.EntityType}
		}
		next, err := m.StateMachine.Apply(ctx, current, ev)
		if errors.Is(err, kiffstate.ErrInvalidTransition) {
			// Live ingestion stores an event whose transition does not
			// match and leaves the state where it was; replay does the same.
			continue
		}
		if err != nil {
			return err
		}
		current = next
	}
	h.done = true
	return nil
}
