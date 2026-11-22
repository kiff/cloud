// Package payreports keeps the payments an account's owner or admin
// reported as unexpected ("Report this payment"). A report names the
// payment's receipt and its taf; the handler halts the taf with the
// owner's halt, and the receipt it names is kept through a test account's
// prune. A report holds no free text: who reported it and when, nothing
// more.
package payreports

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Report is one reported payment: its receipt, its call and its taf.
type Report struct {
	TenantID   string
	TraceID    string
	ProposalID string
	AgentID    string
	ReportedAt time.Time
	ReportedBy string
}

// Store keeps reports. Every method is tenant-scoped.
type Store interface {
	// Report keeps r unless its receipt is reported already, and returns
	// the report that holds: the first one's.
	Report(ctx context.Context, r Report) (Report, error)
	// Reported is when each of traces was reported, for those that were.
	Reported(ctx context.Context, tenant string, traces []string) (map[string]time.Time, error)
}

// ErrInvalid is a report without its receipt, call or taf.
var ErrInvalid = errors.New("payreports: a report names its account, receipt, call and taf")

func (r Report) valid() bool {
	return r.TenantID != "" && r.TraceID != "" && r.ProposalID != "" && r.AgentID != "" && !r.ReportedAt.IsZero()
}

// MemoryStore is the in-memory Store for tests and local runs.
type MemoryStore struct {
	mu      sync.Mutex
	reports map[[2]string]Report
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{reports: map[[2]string]Report{}} }

func (s *MemoryStore) Report(_ context.Context, r Report) (Report, error) {
	if !r.valid() {
		return Report{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{r.TenantID, r.TraceID}
	if had, ok := s.reports[k]; ok {
		return had, nil
	}
	r.ReportedAt = r.ReportedAt.UTC()
	s.reports[k] = r
	return r, nil
}

func (s *MemoryStore) Reported(_ context.Context, tenant string, traces []string) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]time.Time{}
	for _, t := range traces {
		if r, ok := s.reports[[2]string{tenant, t}]; ok {
			out[t] = r.ReportedAt
		}
	}
	return out, nil
}
