package billing

import (
	"context"
	"sync"
)

// Store persists the two pieces of billing state the processor needs
// that do not belong on the tenant row:
//
//   - event idempotency: a record of processed Stripe event ids, so
//     a duplicate webhook delivery (Stripe retries until it sees a
//     2xx, and delivers at-least-once) is a no-op.
//   - customer↔tenant correlation: the link captured at activation
//     (when the checkout session carries the tenant_slug custom
//     field) so later lifecycle events — which carry only the Stripe
//     customer id — resolve to exactly one tenant.
//
// Production uses the Postgres implementation; tests use the memory
// implementation. Both are safe for concurrent use.
type Store interface {
	// Seen reports whether eventID has already been fully processed,
	// without recording anything. The processor calls this BEFORE
	// applying an event so an already-applied event (including a
	// later one that superseded it) is skipped rather than
	// re-applied out of order.
	Seen(ctx context.Context, eventID string) (bool, error)

	// MarkProcessed records eventID as processed and reports whether
	// it was already present. It is called AFTER a successful apply
	// (mark-after-apply), so a transient apply failure leaves the
	// event unmarked and the Stripe retry re-applies it instead of
	// being silently deduped. The check-and-set stays atomic so two
	// concurrent first-deliveries converge (the apply is absolute
	// and idempotent, so the racing re-apply is harmless).
	MarkProcessed(ctx context.Context, eventID string) (already bool, err error)

	// LinkCustomer records that a Stripe customer maps to a KIFF
	// tenant. Called at activation. Idempotent: re-linking the same
	// pair is a no-op; relinking a customer to a different tenant
	// overwrites (a customer is owned by at most one tenant).
	LinkCustomer(ctx context.Context, customerID, tenantID string) error

	// TenantForCustomer resolves a Stripe customer id to its KIFF
	// tenant. Returns ("", nil) when no link exists — the caller
	// treats that as an unmapped event (logged for reconciliation,
	// never an error that makes Stripe retry forever).
	TenantForCustomer(ctx context.Context, customerID string) (tenantID string, err error)
}

// MemoryStore is an in-process Store for tests and single-node dev.
type MemoryStore struct {
	mu        sync.Mutex
	processed map[string]struct{}
	customers map[string]string // customerID -> tenantID
}

// NewMemoryStore returns an empty in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		processed: make(map[string]struct{}),
		customers: make(map[string]string),
	}
}

// Seen implements Store.
func (m *MemoryStore) Seen(_ context.Context, eventID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.processed[eventID]
	return ok, nil
}

// MarkProcessed implements Store.
func (m *MemoryStore) MarkProcessed(_ context.Context, eventID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.processed[eventID]; ok {
		return true, nil
	}
	m.processed[eventID] = struct{}{}
	return false, nil
}

// LinkCustomer implements Store.
func (m *MemoryStore) LinkCustomer(_ context.Context, customerID, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.customers[customerID] = tenantID
	return nil
}

// TenantForCustomer implements Store.
func (m *MemoryStore) TenantForCustomer(_ context.Context, customerID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.customers[customerID], nil
}
