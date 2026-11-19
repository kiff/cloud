// Package stream provides an in-process, tenant-scoped pub/sub broker
// for pushing live events (decisions, approvals, receipts) to connected
// dashboard clients over Server-Sent Events.
//
// It is deliberately in-process: the cloud runs as a single API binary
// today (one per service), so a goroutine-safe map of subscriber
// channels is sufficient and adds no infrastructure. When the deploy
// grows to multiple instances, Publish becomes a thin wrapper over
// Postgres LISTEN/NOTIFY (or Redis) and this interface stays the same.
//
// Publish never blocks the caller (the decide path): each subscriber
// has a small buffered channel and a full buffer drops the event rather
// than stalling governance. A dropped event only costs a client one
// missed nudge; the dashboard reconciles on its next full fetch.
package stream

import "sync"

// Event is one live update for a tenant's dashboard. Type is a short
// tag ("decision", "approval", "receipt"); Data is the JSON payload the
// client renders or uses as a refresh trigger.
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// subBuffer is how many events a slow subscriber can fall behind before
// events start dropping for that subscriber. Small on purpose: the
// dashboard treats an event as a nudge to refetch, so a missed one is
// harmless and we never want to hold memory for a stalled client.
const subBuffer = 16

// Broker fans out tenant-scoped events to subscribers. The zero value
// is not usable; call NewBroker. A nil *Broker is safe: every method is
// a no-op, so callers can wire it optionally without nil checks.
type Broker struct {
	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{}
}

// NewBroker returns a ready broker.
func NewBroker() *Broker {
	return &Broker{subs: make(map[string]map[chan Event]struct{})}
}

// Subscribe registers a subscriber for tenantID and returns its event
// channel plus a cancel func that unsubscribes and closes the channel.
// The caller must invoke cancel when done (e.g., on client disconnect).
func (b *Broker) Subscribe(tenantID string) (<-chan Event, func()) {
	if b == nil {
		ch := make(chan Event)
		return ch, func() {}
	}
	ch := make(chan Event, subBuffer)
	b.mu.Lock()
	if b.subs[tenantID] == nil {
		b.subs[tenantID] = make(map[chan Event]struct{})
	}
	b.subs[tenantID][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if set, ok := b.subs[tenantID]; ok {
				delete(set, ch)
				if len(set) == 0 {
					delete(b.subs, tenantID)
				}
			}
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Publish delivers e to every subscriber of tenantID. Non-blocking: a
// subscriber whose buffer is full misses this event (see subBuffer).
func (b *Broker) Publish(tenantID string, e Event) {
	if b == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs[tenantID] {
		select {
		case ch <- e:
		default: // slow subscriber; drop rather than block the caller
		}
	}
}

// SubscriberCount reports how many subscribers tenantID has. Test/ops aid.
func (b *Broker) SubscriberCount(tenantID string) int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs[tenantID])
}
