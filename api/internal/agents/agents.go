// Package agents holds the tenant's agents as first-class records (RFC
// 041 D1).
//
// An agent is one name used everywhere: the holder of its cards, the
// agent_id its bound keys carry, the agent_id its kiff-guard runtime
// registers under, the actor_id on its decisions. This package stores
// only the record that the name exists in the tenant; cards, keys and
// runtimes keep their own stores and are joined by that name.
package agents

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
)

// Agent is one agent record.
type Agent struct {
	TenantID   string
	ID         string
	CreatedAt  time.Time
	CreatedBy  string
	ArchivedAt *time.Time
	// Kind is KindTaf for an agent the tafs app made, whose calls are
	// tafs tasks; empty for the account's own agents, whose calls are
	// KIFF actions.
	Kind string
	// Name is what people read the agent by: the label of its latest
	// confirmed draft ("Refunds 4"), "" when it has none. Ensure with a
	// name sets it; without one it stays.
	Name string
}

// KindTaf marks an agent made by the tafs app: from a phone's signed
// request, which only the app's device key makes.
const KindTaf = "taf"

var (
	// ErrInvalidID is returned for an agent id outside the rules of a
	// bound key's agent_id: non-empty after trimming, at most 200
	// characters, no control characters.
	ErrInvalidID = errors.New("agents: agent id must be 1–200 characters with no control characters and no ~")
	// ErrExists is returned by Create for an id the tenant already has.
	ErrExists = errors.New("agents: agent already exists")
	// ErrNotFound is returned for an id the tenant has no record of.
	ErrNotFound = errors.New("agents: agent not found")
)

// checkStored is the store boundary's rule: the id must already be
// normalized. A store that trimmed silently would let " refund-agent"
// and "refund-agent" become different records, or a record match no key.
func checkStored(id string) error {
	n, err := NormalizeID(id)
	if err != nil {
		return err
	}
	if n != id {
		return ErrInvalidID
	}
	return nil
}

// NormalizeID trims id and checks it against the agent id rules.
func NormalizeID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 200 {
		return "", ErrInvalidID
	}
	for _, r := range id {
		// "~" is KIFF's own: an agent's trial Cards are held by
		// "<agent>~trial" (mandate.TrialSuffix), so an agent named so
		// would be taken for another's trial, and halted with it.
		if unicode.IsControl(r) || r == '~' {
			return "", ErrInvalidID
		}
	}
	return id, nil
}

// Store persists agent records. Every method is tenant-scoped.
type Store interface {
	// Create records a new agent; ErrExists when the id is taken,
	// archived or not; ErrInvalidID when the id breaks the agent id
	// rules (it must already be normalized).
	Create(ctx context.Context, a Agent) error
	// Ensure records the agent if the tenant has none by that id, and
	// is a no-op otherwise, except that a Kind is set on a record that
	// has none. Used where acting on a name implies the
	// agent (issuing it a card). ErrInvalidID for an out-of-rule id:
	// a name no key can be bound to is never promoted to an agent.
	Ensure(ctx context.Context, a Agent) error
	// List returns every record for the tenant, archived included,
	// oldest first.
	List(ctx context.Context, tenantID string) ([]Agent, error)
	// Archive marks the agent archived. ErrNotFound when absent;
	// archiving an archived agent is not an error.
	Archive(ctx context.Context, tenantID, id string, at time.Time) error
}
