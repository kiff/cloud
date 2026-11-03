package permit

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Target is where an action's permit goes: the upstream tool the verifier
// receives, the audience it checks, and the argument the Card governs.
// It is account configuration, set by an owner or admin, so a decide
// request cannot choose it.
type Target struct {
	// Domain is the domain whose Cards authorize the action: the decision
	// must be in it for the permit to be issued.
	Domain    string `json:"domain"`
	Action    string `json:"action"`
	Tool      string `json:"tool"`
	Audience  string `json:"audience"`
	AmountArg string `json:"amount_argument,omitempty"`
	// ResourceArg is the string argument a Card's resource scope reads:
	// which order, customer or account the call is on.
	ResourceArg string `json:"resource_argument,omitempty"`
	// AmountLookedUp says the amount isn't one of the call's arguments: the
	// gateway reads it from the resource the signed arguments name
	// (amount_lookup). Only a relay tool has it, where
	// only the gateway can use the permit.
	AmountLookedUp bool `json:"amount_looked_up,omitempty"`
	// AmountDecimal says the signed amount is a decimal string ("80.00"),
	// which the Card counts in minor units at AmountScale (connection profiles).
	AmountDecimal bool `json:"amount_decimal,omitempty"`
	AmountScale   int  `json:"amount_scale,omitempty"`
	// AmountPath and ResourcePath say where a relay tool's signed arguments
	// carry the governed values when they're nested ("invoice.total");
	// empty, at the top level under AmountArg and ResourceArg.
	AmountPath   string `json:"amount_path,omitempty"`
	ResourcePath string `json:"resource_path,omitempty"`
}

var argPath = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}(\.[A-Za-z][A-Za-z0-9_]{0,63}){0,7}$`)

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

var domainSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ErrInvalidTarget: a target is malformed.
var ErrInvalidTarget = errors.New("permit: invalid target")

// Validate checks a target's fields.
func (t Target) Validate() error {
	if !domainSlug.MatchString(t.Domain) || !identifier.MatchString(t.Action) || (t.AmountArg != "" && !identifier.MatchString(t.AmountArg)) {
		return ErrInvalidTarget
	}
	if t.ResourceArg != "" && (!identifier.MatchString(t.ResourceArg) || t.ResourceArg == t.AmountArg) {
		return ErrInvalidTarget
	}
	if t.AmountLookedUp && (t.AmountArg == "" || t.ResourceArg == "") {
		return ErrInvalidTarget
	}
	if t.AmountScale < 0 || t.AmountScale > 8 || (t.AmountDecimal && (t.AmountArg == "" || t.AmountLookedUp)) {
		return ErrInvalidTarget
	}
	if (t.AmountPath != "" && (!argPath.MatchString(t.AmountPath) || t.AmountArg == "" || t.AmountLookedUp)) ||
		(t.ResourcePath != "" && (!argPath.MatchString(t.ResourcePath) || t.ResourceArg == "")) {
		return ErrInvalidTarget
	}
	if strings.TrimSpace(t.Tool) == "" || len(t.Tool) > 128 || strings.TrimSpace(t.Audience) == "" ||
		len(t.Audience) > 512 || strings.ContainsAny(t.Audience, " \t\r\n") {
		return ErrInvalidTarget
	}
	return nil
}

// Targets keeps each account's permit targets.
type Targets interface {
	// Replace sets the account's targets to exactly ts.
	Replace(ctx context.Context, tenantID string, ts []Target, by string, at time.Time) error
	Get(ctx context.Context, tenantID, domain, action string) (Target, bool, error)
	List(ctx context.Context, tenantID string) ([]Target, error)
}

// PostgresTargets keeps targets in cloud_meta.permit_targets.
type PostgresTargets struct{ Pool *pgxpool.Pool }

func (s PostgresTargets) Replace(ctx context.Context, tenantID string, ts []Target, by string, at time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM cloud_meta.permit_targets WHERE tenant_id = $1`, tenantID); err != nil {
		return err
	}
	for _, t := range ts {
		if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.permit_targets (tenant_id, domain, action, tool, audience, amount_arg, resource_arg, amount_looked_up, updated_at, updated_by, amount_decimal, amount_scale, amount_path, resource_path)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`, tenantID, t.Domain, t.Action, t.Tool, t.Audience, t.AmountArg, t.ResourceArg, t.AmountLookedUp, at.UTC(), by, t.AmountDecimal, t.AmountScale, t.AmountPath, t.ResourcePath); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s PostgresTargets) Get(ctx context.Context, tenantID, domain, action string) (Target, bool, error) {
	t := Target{Domain: domain, Action: action}
	err := s.Pool.QueryRow(ctx, `SELECT tool, audience, amount_arg, resource_arg, amount_looked_up, amount_decimal, amount_scale, amount_path, resource_path FROM cloud_meta.permit_targets WHERE tenant_id = $1 AND domain = $2 AND action = $3`,
		tenantID, domain, action).Scan(&t.Tool, &t.Audience, &t.AmountArg, &t.ResourceArg, &t.AmountLookedUp, &t.AmountDecimal, &t.AmountScale, &t.AmountPath, &t.ResourcePath)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, false, nil
	}
	return t, err == nil, err
}

func (s PostgresTargets) List(ctx context.Context, tenantID string) ([]Target, error) {
	rows, err := s.Pool.Query(ctx, `SELECT domain, action, tool, audience, amount_arg, resource_arg, amount_looked_up, amount_decimal, amount_scale, amount_path, resource_path FROM cloud_meta.permit_targets WHERE tenant_id = $1 ORDER BY domain, action`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.Domain, &t.Action, &t.Tool, &t.Audience, &t.AmountArg, &t.ResourceArg, &t.AmountLookedUp, &t.AmountDecimal, &t.AmountScale, &t.AmountPath, &t.ResourcePath); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MemoryTargets keeps targets in memory, for local development and tests.
type MemoryTargets struct {
	mu sync.Mutex
	m  map[string]map[string]Target
}

func NewMemoryTargets() *MemoryTargets { return &MemoryTargets{m: map[string]map[string]Target{}} }

func (s *MemoryTargets) Replace(_ context.Context, tenantID string, ts []Target, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]Target{}
	for _, t := range ts {
		m[t.Domain+"\x00"+t.Action] = t
	}
	s.m[tenantID] = m
	return nil
}

func (s *MemoryTargets) Get(_ context.Context, tenantID, domain, action string) (Target, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[tenantID][domain+"\x00"+action]
	return t, ok, nil
}

func (s *MemoryTargets) List(_ context.Context, tenantID string) ([]Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Target{}
	for _, t := range s.m[tenantID] {
		out = append(out, t)
	}
	return out, nil
}
