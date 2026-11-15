package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ApprovalCheck is KIFF's own record that it verified an owner's approval
// of a connection inside an open approval window (kiff/tafs window_required):
// /v1/devices/verify writes it when planque asks at the approve, after KIFF
// checked the device's signature and found the window open itself. planque
// may present that same approval once more, for Setup's read Card of the
// same connection (POST /v1/me/setup-reads), within ApprovalCheckLife of
// KIFF's check: the vendor's sign-in comes in between, and the phone closes
// its window when it leaves the app. Nothing a request sends creates or
// extends one, and it counts once.
type ApprovalCheck struct {
	// ID is ApprovalCheckID of the signed message and its signature.
	ID       string
	Tenant   string
	DeviceID string
	// Grant is planque's grant id, from the signed request's own path.
	Grant string
	// Window is the approval window the message named, open at CheckedAt.
	Window string
	// CheckedAt is KIFF's clock when it verified the window, never a time
	// from the request.
	CheckedAt time.Time
	// UsedAt is when Setup's read Card was issued on it; nil before.
	UsedAt *time.Time
}

// ApprovalCheckLife is how long after KIFF's check the approval counts for
// Setup's read Card.
const ApprovalCheckLife = 30 * time.Minute

// approvalChecksKept is how long a record's row stays, used or not; older
// rows go with the next record.
const approvalChecksKept = 24 * time.Hour

// ErrApprovalCheckNotFound is a check KIFF never recorded.
var ErrApprovalCheckNotFound = errors.New("auth: no such approval check")

// ApprovalCheckID is a record's id: the SHA-256 of the exact signed bytes,
// a zero byte, and the signature as sent. Another message or signature is
// another record.
func ApprovalCheckID(message []byte, signature string) string {
	h := sha256.New()
	h.Write(message)
	h.Write([]byte{0})
	h.Write([]byte(signature))
	return hex.EncodeToString(h.Sum(nil))
}

// ApprovalCheckStore keeps the records.
type ApprovalCheckStore interface {
	// Record keeps c. A record with the same id stays as it was: never
	// replaced, never moved later. Rows older than a day are deleted.
	Record(ctx context.Context, c ApprovalCheck) error
	// Get returns a record. ErrApprovalCheckNotFound when absent.
	Get(ctx context.Context, id string) (ApprovalCheck, error)
	// Use marks a record used at at, only if it isn't used yet: false when
	// another use came first, or there's none. One conditional write, so of
	// two parallel uses only one is true.
	Use(ctx context.Context, id string, at time.Time) (bool, error)
}

// MemoryApprovalCheckStore is an ApprovalCheckStore in memory, for tests and
// local runs.
type MemoryApprovalCheckStore struct {
	mu   sync.Mutex
	byID map[string]ApprovalCheck
}

// NewMemoryApprovalCheckStore returns an empty store.
func NewMemoryApprovalCheckStore() *MemoryApprovalCheckStore {
	return &MemoryApprovalCheckStore{byID: map[string]ApprovalCheck{}}
}

func (m *MemoryApprovalCheckStore) Record(_ context.Context, c ApprovalCheck) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, o := range m.byID {
		if c.CheckedAt.Sub(o.CheckedAt) > approvalChecksKept {
			delete(m.byID, id)
		}
	}
	if _, ok := m.byID[c.ID]; ok {
		return nil
	}
	c.UsedAt = nil
	m.byID[c.ID] = c
	return nil
}

func (m *MemoryApprovalCheckStore) Get(_ context.Context, id string) (ApprovalCheck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.byID[id]
	if !ok {
		return ApprovalCheck{}, ErrApprovalCheckNotFound
	}
	if c.UsedAt != nil {
		at := *c.UsedAt
		c.UsedAt = &at
	}
	return c, nil
}

func (m *MemoryApprovalCheckStore) Use(_ context.Context, id string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.byID[id]
	if !ok || c.UsedAt != nil {
		return false, nil
	}
	c.UsedAt = &at
	m.byID[id] = c
	return true, nil
}
