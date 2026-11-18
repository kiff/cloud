package packs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is the ledger and the requests in process, for tests and an API
// without a database.
type Memory struct {
	mu       sync.Mutex
	entries  []*Entry
	draws    map[string]drawRow // tenant|key
	requests []*Request
	// takebacks are take-backs of packs not recorded yet: source|txn → when.
	takebacks map[string]time.Time
	// credits are the packs of credits, and creditDraws what runs drew
	// from them (SetCreditDraw, in tests: runs are settled in Postgres).
	credits     []*CreditPack
	creditDraws []creditDraw
}

type creditDraw struct {
	tenant  string
	credits int64
	at      time.Time
}

type drawRow struct {
	pack string
	at   time.Time
}

// NewMemory returns an empty ledger.
func NewMemory() *Memory {
	return &Memory{draws: map[string]drawRow{}, takebacks: map[string]time.Time{}}
}

func valid(e *Entry, at time.Time) bool {
	return e.RevokedAt == nil && !e.BoughtAt.After(at) && e.ExpiresAt.After(at)
}

// Record implements Ledger.
func (m *Memory) Record(_ context.Context, g Grant) (Entry, bool, error) {
	if g.TenantID == "" || g.Pack == "" || g.Tasks <= 0 || g.SourceTxn == "" {
		return Entry{}, false, fmt.Errorf("packs: record: a pack needs its account, pack, tasks and transaction")
	}
	if g.Kind == "" {
		g.Kind = KindBought
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.Source == g.Source && e.SourceTxn == g.SourceTxn {
			return *e, false, nil
		}
	}
	at := g.At.UTC()
	e := &Entry{ID: newID("pk_"), TenantID: g.TenantID, Pack: g.Pack, Tasks: g.Tasks, TasksLeft: g.Tasks, BoughtAt: at,
		ExpiresAt: Expiry(g.Kind, at), Kind: g.Kind, Source: g.Source, SourceTxn: g.SourceTxn, RequestID: g.RequestID}
	m.entries = append(m.entries, e)
	if back, ok := m.takebacks[string(g.Source)+"|"+g.SourceTxn]; ok {
		// Taken back before it was recorded.
		delete(m.takebacks, string(g.Source)+"|"+g.SourceTxn)
		e.TasksLeft, e.RevokedAt, e.RequestID = 0, &back, ""
		return *e, true, nil
	}
	for _, r := range m.requests {
		if r.TenantID == g.TenantID && r.ID == g.RequestID && r.Status == RequestOpen && r.Kind != AskRaise && r.ExpiresAt.After(at) {
			r.Status, r.AnsweredBy, r.PackID = RequestApproved, g.AnsweredBy, e.ID
			r.AnsweredAt = &at
		}
	}
	return *e, true, nil
}

// Revoke implements Ledger.
func (m *Memory) Revoke(_ context.Context, source Source, txn string, at time.Time) (Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.Source == source && e.SourceTxn == txn {
			e.TasksLeft = 0
			if e.RevokedAt == nil {
				at := at.UTC()
				e.RevokedAt = &at
			}
			return *e, true, nil
		}
	}
	if _, ok := m.takebacks[string(source)+"|"+txn]; !ok {
		m.takebacks[string(source)+"|"+txn] = at.UTC()
	}
	for k, back := range m.takebacks {
		if back.Before(at.Add(-KeepTakeBack)) {
			delete(m.takebacks, k)
		}
	}
	return Entry{}, false, nil
}

// Allowance implements Ledger.
func (m *Memory) Allowance(_ context.Context, tenantID string, from, at time.Time) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var drawn, left int
	for k, d := range m.draws {
		if strings.HasPrefix(k, tenantID+"|") && !d.at.Before(from) && !d.at.After(at) {
			drawn++
		}
	}
	for _, e := range m.entries {
		if e.TenantID == tenantID && valid(e, at) {
			left += e.TasksLeft
		}
	}
	return drawn, left, nil
}

// Draw implements Ledger.
func (m *Memory) Draw(_ context.Context, tenantID, key string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.draws[tenantID+"|"+key]; ok {
		return nil
	}
	for _, e := range m.sorted(tenantID) {
		if valid(e, at) && e.TasksLeft > 0 {
			e.TasksLeft--
			m.draws[tenantID+"|"+key] = drawRow{pack: e.ID, at: at}
			return nil
		}
	}
	return ErrNoTasks
}

func (m *Memory) sorted(tenantID string) []*Entry {
	var out []*Entry
	for _, e := range m.entries {
		if e.TenantID == tenantID {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].BoughtAt.Before(out[j].BoughtAt) })
	return out
}

// Valid implements Ledger.
func (m *Memory) Valid(_ context.Context, tenantID string, at time.Time) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Entry{}
	for _, e := range m.sorted(tenantID) {
		if valid(e, at) && e.TasksLeft > 0 {
			out = append(out, *e)
		}
	}
	return out, nil
}

// Ask implements Requests.
func (m *Memory) Ask(_ context.Context, r Request) (Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.requests {
		if o.TenantID == r.TenantID && o.AskedBy == r.AskedBy && o.read(r.CreatedAt).Status == RequestOpen {
			return Request{}, ErrAlreadyAsked
		}
	}
	r.ID, r.Status, r.Reason, r.Kind = newID("pr_"), RequestOpen, strings.TrimSpace(r.Reason), r.kind()
	if r.Kind == AskRaise {
		r.Pack = ""
	}
	r.CreatedAt, r.ExpiresAt = r.CreatedAt.UTC(), r.ExpiresAt.UTC()
	m.requests = append(m.requests, &r)
	return r, nil
}

// ApproveRaises implements Requests.
func (m *Memory) ApproveRaises(_ context.Context, tenantID, by string, now time.Time) ([]Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Request{}
	at := now.UTC()
	for _, r := range m.requests {
		if r.TenantID == tenantID && r.Kind == AskRaise && r.read(now).Status == RequestOpen {
			r.Status, r.AnsweredBy, r.AnsweredAt = RequestApproved, by, &at
			out = append(out, *r)
		}
	}
	return out, nil
}

// Get implements Requests.
func (m *Memory) Get(_ context.Context, tenantID, id string, now time.Time) (Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.requests {
		if r.TenantID == tenantID && r.ID == id {
			return r.read(now), nil
		}
	}
	return Request{}, ErrRequestNotFound
}

// List implements Requests.
func (m *Memory) List(_ context.Context, tenantID, askedBy string, openOnly bool, since, now time.Time) ([]Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Request{}
	for i := len(m.requests) - 1; i >= 0; i-- {
		r := m.requests[i].read(now)
		if r.TenantID != tenantID || (askedBy != "" && r.AskedBy != askedBy) || r.CreatedAt.Before(since) || (openOnly && r.Status != RequestOpen) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *Memory) answer(tenantID, id, status, by, note string, onlyAsker bool, now time.Time) (Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.requests {
		if r.TenantID != tenantID || r.ID != id {
			continue
		}
		if onlyAsker && r.AskedBy != by {
			return Request{}, ErrRequestNotFound
		}
		if r.read(now).Status != RequestOpen {
			return r.read(now), ErrAnswered
		}
		at := now.UTC()
		r.Status, r.AnsweredBy, r.AnsweredAt, r.Note = status, by, &at, strings.TrimSpace(note)
		return *r, nil
	}
	return Request{}, ErrRequestNotFound
}

// Withdraw implements Requests.
func (m *Memory) Withdraw(_ context.Context, tenantID, id, by string, now time.Time) (Request, error) {
	return m.answer(tenantID, id, RequestWithdrawn, by, "", true, now)
}

// Decline implements Requests.
func (m *Memory) Decline(_ context.Context, tenantID, id, by, note string, now time.Time) (Request, error) {
	return m.answer(tenantID, id, RequestDeclined, by, note, false, now)
}

var (
	_ Ledger   = (*Memory)(nil)
	_ Requests = (*Memory)(nil)
	_ Ledger   = (*Postgres)(nil)
	_ Requests = (*Postgres)(nil)
)

// RecordCredits implements CreditLedger.
func (m *Memory) RecordCredits(_ context.Context, g CreditGrant) (CreditPack, bool, error) {
	if g.TenantID == "" || g.Pack == "" || g.Credits <= 0 || g.SourceTxn == "" {
		return CreditPack{}, false, fmt.Errorf("packs: record credits: a pack needs its account, pack, credits and transaction")
	}
	if g.Kind == "" {
		g.Kind = KindBought
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.credits {
		if c.Source == g.Source && c.SourceTxn == g.SourceTxn {
			return *c, false, nil
		}
	}
	at := g.At.UTC()
	c := &CreditPack{ID: newID("cp_"), TenantID: g.TenantID, Pack: g.Pack, Credits: g.Credits, CreditsLeft: g.Credits, BoughtAt: at,
		Kind: g.Kind, Source: g.Source, SourceTxn: g.SourceTxn, RequestID: g.RequestID}
	m.credits = append(m.credits, c)
	key := string(g.Source) + "|" + g.SourceTxn
	if back, ok := m.takebacks[key]; ok {
		delete(m.takebacks, key)
		c.CreditsLeft, c.RevokedAt, c.RequestID = 0, &back, ""
		return *c, true, nil
	}
	if g.RequestID != "" {
		for _, r := range m.requests {
			if r.TenantID == g.TenantID && r.ID == g.RequestID && r.Status == RequestOpen && r.Kind != AskRaise && r.ExpiresAt.After(at) {
				r.Status, r.AnsweredBy, r.PackID = RequestApproved, g.AnsweredBy, c.ID
				r.AnsweredAt = &at
			}
		}
	}
	return *c, true, nil
}

// RevokeCredits implements CreditLedger.
func (m *Memory) RevokeCredits(_ context.Context, source Source, txn string, at time.Time) (CreditPack, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.credits {
		if c.Source == source && c.SourceTxn == txn {
			left := c.CreditsLeft
			c.CreditsLeft = 0
			if c.RevokedAt == nil {
				t := at.UTC()
				c.RevokedAt = &t
			}
			out := *c
			out.TakenBack = left
			return out, true, nil
		}
	}
	m.takebacks[string(source)+"|"+txn] = at.UTC()
	return CreditPack{}, false, nil
}

// Credits implements CreditLedger.
func (m *Memory) Credits(_ context.Context, tenantID string, from, to time.Time) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var drawn, left int64
	for _, d := range m.creditDraws {
		if d.tenant == tenantID && !d.at.Before(from) && d.at.Before(to) {
			drawn += d.credits
		}
	}
	for _, c := range m.credits {
		if c.TenantID == tenantID && c.RevokedAt == nil {
			left += c.CreditsLeft
		}
	}
	return drawn, left, nil
}

// BoughtPacks implements BoughtRead.
func (m *Memory) BoughtPacks(_ context.Context, tenantID string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for _, c := range m.credits {
		if c.TenantID == tenantID && c.Kind == KindBought {
			out[c.Pack] = true
		}
	}
	return out, nil
}

// CreditPacks implements CreditLedger.
func (m *Memory) CreditPacks(_ context.Context, tenantID string) ([]CreditPack, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []CreditPack{}
	for _, c := range m.credits {
		if c.TenantID == tenantID && c.RevokedAt == nil && c.CreditsLeft > 0 {
			out = append(out, *c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].BoughtAt.Before(out[j].BoughtAt) })
	return out, nil
}

// DrawCredits draws credits from tenantID's oldest packs for a run that
// ended at at, as settling it does in Postgres: for tests.
func (m *Memory) DrawCredits(tenantID string, credits int64, at time.Time) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var drawn int64
	for _, c := range m.credits {
		if credits == 0 {
			break
		}
		if c.TenantID != tenantID || c.RevokedAt != nil || c.CreditsLeft == 0 || c.BoughtAt.After(at) {
			continue
		}
		take := min(credits, c.CreditsLeft)
		c.CreditsLeft, credits, drawn = c.CreditsLeft-take, credits-take, drawn+take
	}
	if drawn > 0 {
		m.creditDraws = append(m.creditDraws, creditDraw{tenant: tenantID, credits: drawn, at: at.UTC()})
	}
	return drawn
}
