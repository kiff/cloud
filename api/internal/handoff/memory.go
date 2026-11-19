package handoff

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"
)

// Memory is the Store in memory, for tests and a database-less run. Its
// calls are what a test puts in CallsByID.
type Memory struct {
	mu        sync.Mutex
	rows      map[[2]string]Handoff
	CallsByID map[[2]string]Call
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{rows: map[[2]string]Handoff{}, CallsByID: map[[2]string]Call{}}
}

// PutCall records a call as the gateway would, by its proposal id.
func (m *Memory) PutCall(tenantID, id string, c Call) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.Proposal = id
	m.CallsByID[[2]string{tenantID, id}] = c
}

func clone(h Handoff) Handoff {
	h.PreparedBy = slices.Clone(h.PreparedBy)
	if h.PreparedBy == nil {
		h.PreparedBy = []string{}
	}
	return h
}

func (m *Memory) Raise(_ context.Context, h Handoff) (Handoff, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	open := 0
	var waiting *Handoff
	for _, r := range m.rows {
		if r.TenantID == h.TenantID && r.Agent == h.Agent {
			if r.Operation == h.Operation {
				return clone(r), false, nil
			}
			if r.Open(h.CreatedAt) {
				open++
				if h.About != "" && r.What == h.What && r.About == h.About && (waiting == nil || r.CreatedAt.Before(waiting.CreatedAt)) {
					w := r
					waiting = &w
				}
			}
		}
	}
	if waiting != nil {
		w := clone(*waiting)
		w.AlreadyWaiting = true
		return w, false, nil
	}
	if open >= MaxWaitingPerAgent {
		return Handoff{}, false, ErrTooMany
	}
	if h.ID == "" {
		h.ID = NewID()
	}
	h.Status = Waiting
	m.rows[[2]string{h.TenantID, h.ID}] = clone(h)
	return clone(h), true, nil
}

func (m *Memory) Get(_ context.Context, tenantID, id string) (Handoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.rows[[2]string{tenantID, id}]
	if !ok {
		return Handoff{}, ErrNotFound
	}
	return clone(h), nil
}

func (m *Memory) sorted(keep func(Handoff) bool) []Handoff {
	var out []Handoff
	for _, h := range m.rows {
		if keep(h) {
			out = append(out, clone(h))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func head(hs []Handoff, limit int) []Handoff {
	if limit >= 0 && len(hs) > limit {
		return hs[:limit]
	}
	return hs
}

func (m *Memory) Open(_ context.Context, tenantID string, now time.Time, limit int) ([]Handoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return head(m.sorted(func(h Handoff) bool { return h.TenantID == tenantID && h.Open(now) }), limit), nil
}

func (m *Memory) Answer(_ context.Context, tenantID, id string, answer Status, by, byID string, device *Device, at time.Time) (Handoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{tenantID, id}
	h, ok := m.rows[k]
	if !ok {
		return Handoff{}, ErrNotFound
	}
	if !h.Open(at) || (answer != Done && answer != Skipped) {
		return clone(h), ErrAnswered
	}
	t := at.UTC()
	h.Status, h.AnsweredBy, h.AnsweredByID, h.AnsweredAt, h.Device = answer, by, byID, &t, device
	m.rows[k] = h
	return clone(h), nil
}

func (m *Memory) Check(_ context.Context, tenantID, id, proposal string, at time.Time) (Handoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{tenantID, id}
	h, ok := m.rows[k]
	if !ok {
		return Handoff{}, ErrNotFound
	}
	if h.Status != Done || h.CheckedBy != "" || h.SettledAt != nil {
		return clone(h), ErrNotChecked
	}
	t := at.UTC()
	h.CheckedBy, h.CheckedAt = proposal, &t
	m.rows[k] = h
	return clone(h), nil
}

func (m *Memory) Expire(_ context.Context, now time.Time, limit int) ([]Handoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	due := head(m.sorted(func(h Handoff) bool { return h.Status == Waiting && !now.Before(h.ExpiresAt) }), limit)
	for i := range due {
		due[i].Status = NoAnswer
		m.rows[[2]string{due[i].TenantID, due[i].ID}] = clone(due[i])
	}
	return due, nil
}

func (m *Memory) Unreceipted(_ context.Context, now time.Time, limit int) ([]Handoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return head(m.sorted(func(h Handoff) bool { return h.SettledAt == nil && (!h.RaisedReceipt || h.Closed(now)) }), limit), nil
}

func (m *Memory) Receipted(_ context.Context, tenantID, id string, settled *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{tenantID, id}
	h, ok := m.rows[k]
	if !ok {
		return ErrNotFound
	}
	h.RaisedReceipt = true
	if settled != nil && h.SettledAt == nil {
		t := settled.UTC()
		h.SettledAt = &t
	}
	m.rows[k] = h
	return nil
}

func (m *Memory) Operations(_ context.Context, tenantID string, agents, ops []string) (map[string]Call, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Call{}
	for k, c := range m.CallsByID {
		if k[0] == tenantID && c.Operation != "" && slices.Contains(ops, c.Operation) && slices.Contains(agents, c.Agent) {
			out[c.Operation] = c
		}
	}
	return out, nil
}

func (m *Memory) Calls(_ context.Context, tenantID string, ids []string) (map[string]Call, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Call{}
	for _, id := range ids {
		if c, ok := m.CallsByID[[2]string{tenantID, id}]; ok {
			out[id] = c
		}
	}
	return out, nil
}
