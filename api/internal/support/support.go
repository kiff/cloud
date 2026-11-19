// Package support is the private support-ticket system: a registered tenant files a ticket, an operator answers it
// from the /ops console, and the two exchange replies on a thread. It
// is deliberately NOT a public community board — tickets are visible
// only to the submitting tenant and to operators. Content lives behind
// auth; the close-the-loop email carries a link, never the body.
//
// Shape:
//   - Ticket: the opening post (title + body) plus status; tenant- and
//     author-scoped.
//   - Message: a reply on the thread, from the "user" or an "operator".
//   - Store: persistence (memory + Postgres), tenant-scoped at the call
//     boundary — a Get/List for a tenant never returns another tenant's
//     tickets.
package support

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Status is a ticket's lifecycle state. A user reply reopens an
// answered/closed ticket to "open" so it resurfaces for the operator.
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusAnswered   Status = "answered"
	StatusClosed     Status = "closed"
)

// ValidStatus reports whether s is a recognized status.
func ValidStatus(s Status) bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusAnswered, StatusClosed:
		return true
	default:
		return false
	}
}

// Author kinds for a thread message.
const (
	AuthorUser     = "user"
	AuthorOperator = "operator"
)

// ErrNotFound is returned when a ticket does not exist for the
// requested (tenant, id) — including the cross-tenant case, so a
// ticket from another tenant is indistinguishable from a missing one.
var ErrNotFound = errors.New("support: ticket not found")

// Ticket is the opening post plus lifecycle metadata.
type Ticket struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	AuthorSubject string    `json:"author_subject"`
	AuthorEmail   string    `json:"author_email,omitempty"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	Category      string    `json:"category"`
	Status        Status    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Message is one reply on a ticket's thread.
type Message struct {
	ID            string    `json:"id"`
	TicketID      string    `json:"ticket_id"`
	AuthorKind    string    `json:"author_kind"` // "user" | "operator"
	AuthorSubject string    `json:"author_subject,omitempty"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
}

// Store persists tickets and their thread messages.
//
// Tenant scoping is at the call boundary: the *ForTenant / tenant-keyed
// methods never cross tenants. The unscoped ListAll / GetByID methods
// are the operator (/ops) surface and must only be reached behind the
// operator allow-list.
type Store interface {
	// CreateTicket persists a new ticket (its opening post).
	CreateTicket(ctx context.Context, t Ticket) error
	// ListForTenant returns a tenant's tickets, most-recently-updated
	// first.
	ListForTenant(ctx context.Context, tenantID string) ([]Ticket, error)
	// GetForTenant returns one ticket scoped to a tenant; ErrNotFound
	// when it is missing or belongs to another tenant.
	GetForTenant(ctx context.Context, tenantID, id string) (Ticket, error)

	// ListAll returns every tenant's tickets (operator surface),
	// optionally filtered by status, most-recently-updated first.
	ListAll(ctx context.Context, status Status) ([]Ticket, error)
	// GetByID returns one ticket regardless of tenant (operator
	// surface); ErrNotFound when missing.
	GetByID(ctx context.Context, id string) (Ticket, error)
	// SetStatus updates a ticket's status and bumps updated_at.
	SetStatus(ctx context.Context, id string, status Status) error

	// AddMessage appends a reply and bumps the ticket's updated_at.
	AddMessage(ctx context.Context, m Message) error
	// ListMessages returns a ticket's thread, oldest first.
	ListMessages(ctx context.Context, ticketID string) ([]Message, error)
}

// MemoryStore is the in-process Store for tests and no-Postgres runs.
type MemoryStore struct {
	mu       sync.RWMutex
	tickets  map[string]Ticket    // id -> ticket
	messages map[string][]Message // ticketID -> messages
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{tickets: map[string]Ticket{}, messages: map[string][]Message{}}
}

func (m *MemoryStore) CreateTicket(_ context.Context, t Ticket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickets[t.ID] = t
	return nil
}

func (m *MemoryStore) ListForTenant(_ context.Context, tenantID string) ([]Ticket, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Ticket{}
	for _, t := range m.tickets {
		if t.TenantID == tenantID {
			out = append(out, t)
		}
	}
	sortByUpdatedDesc(out)
	return out, nil
}

func (m *MemoryStore) GetForTenant(_ context.Context, tenantID, id string) (Ticket, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tickets[id]
	if !ok || t.TenantID != tenantID {
		return Ticket{}, ErrNotFound
	}
	return t, nil
}

func (m *MemoryStore) ListAll(_ context.Context, status Status) ([]Ticket, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Ticket{}
	for _, t := range m.tickets {
		if status != "" && t.Status != status {
			continue
		}
		out = append(out, t)
	}
	sortByUpdatedDesc(out)
	return out, nil
}

func (m *MemoryStore) GetByID(_ context.Context, id string) (Ticket, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tickets[id]
	if !ok {
		return Ticket{}, ErrNotFound
	}
	return t, nil
}

func (m *MemoryStore) SetStatus(_ context.Context, id string, status Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return ErrNotFound
	}
	t.Status = status
	t.UpdatedAt = time.Now().UTC()
	m.tickets[id] = t
	return nil
}

func (m *MemoryStore) AddMessage(_ context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[msg.TicketID]
	if !ok {
		return ErrNotFound
	}
	m.messages[msg.TicketID] = append(m.messages[msg.TicketID], msg)
	t.UpdatedAt = time.Now().UTC()
	m.tickets[msg.TicketID] = t
	return nil
}

func (m *MemoryStore) ListMessages(_ context.Context, ticketID string) ([]Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]Message(nil), m.messages[ticketID]...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func sortByUpdatedDesc(ts []Ticket) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].UpdatedAt.After(ts[j].UpdatedAt) })
}
