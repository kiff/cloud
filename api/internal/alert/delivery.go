package alert

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// newDeliveryID returns an opaque "adlv_<hex>" delivery id.
func newDeliveryID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "adlv_" + hex.EncodeToString(b[:])
}

// newEventID names one alert event, the same for every target it goes to.
func newEventID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "evt_" + hex.EncodeToString(b[:])
}

// Delivery status values. A delivery attempt is recorded for
// every matching target on a consequential decision, so the Control
// Room can show "alert sent / failed / blocked" per triggering decision.
const (
	// DeliveryDelivered: the sink returned a 2xx.
	DeliveryDelivered = "delivered"
	// DeliveryFailed: the sink errored (non-2xx, timeout, transport).
	DeliveryFailed = "failed"
	// DeliveryBlocked: the SSRF guard refused the address. A
	// first-class status, not a silent drop — the operator can see the
	// alert was withheld because the target resolved to a blocked range.
	DeliveryBlocked = "blocked"
)

// Delivery is one persisted alert delivery attempt against one target,
// triggered by one decision. Tenant-scoped; queried for a control's
// evidence timeline.
type Delivery struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	ProposalID  string    `json:"proposal_id"`
	TargetID    string    `json:"target_id"`
	TargetType  string    `json:"target_type"`
	Action      string    `json:"action"`
	Outcome     string    `json:"outcome"`
	Status      string    `json:"status"`
	Detail      string    `json:"detail,omitempty"`
	AttemptedAt time.Time `json:"attempted_at"`
}

// DeliveryStore persists alert delivery attempts and reads them back
// for a tenant. Tenant-scoped at the call boundary.
type DeliveryStore interface {
	// RecordDelivery persists one attempt. Best-effort at the call
	// site: the decide path's alert dispatch is detached, so a failed
	// record is logged, not propagated.
	RecordDelivery(ctx context.Context, d Delivery) error

	// ListDeliveries returns a tenant's delivery attempts for a
	// proposal (the triggering decision), newest first. Empty slice
	// when none.
	ListDeliveries(ctx context.Context, tenantID, proposalID string) ([]Delivery, error)
}

// MemoryDeliveryStore is the in-process DeliveryStore for tests and
// no-Postgres runs. Concurrent-safe.
type MemoryDeliveryStore struct {
	mu         sync.RWMutex
	deliveries []Delivery
}

// NewMemoryDeliveryStore returns an empty in-memory delivery store.
func NewMemoryDeliveryStore() *MemoryDeliveryStore {
	return &MemoryDeliveryStore{}
}

// RecordDelivery implements DeliveryStore.
func (m *MemoryDeliveryStore) RecordDelivery(_ context.Context, d Delivery) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliveries = append(m.deliveries, d)
	return nil
}

// ListDeliveries implements DeliveryStore.
func (m *MemoryDeliveryStore) ListDeliveries(_ context.Context, tenantID, proposalID string) ([]Delivery, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Delivery, 0)
	for _, d := range m.deliveries {
		if d.TenantID == tenantID && d.ProposalID == proposalID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AttemptedAt.After(out[j].AttemptedAt) })
	return out, nil
}
