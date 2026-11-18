package meter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kiff/cloud/api/internal/tenant"
)

// Why the meters refuse a call, as decide's reasons name them.
const (
	// ReasonPlanLimitReached: one of the account's own agents' calls
	// would count past the trial's actions.
	ReasonPlanLimitReached = "plan_limit_reached"
	// ReasonPlanTrialEnded: the account's plan ended and no plan followed
	// it.
	ReasonPlanTrialEnded = "plan_trial_ended"
	// ReasonPlanNotStarted: the account has no plan yet. There is no free
	// tier and no trial.
	ReasonPlanNotStarted = "plan_not_started"
	// ReasonPlanOwnAgentsCompany: a Solo account's own agent (not a taf)
	// made a call that would count. Solo is tafs only; KIFF actions for
	// one's own agents are on Company.
	ReasonPlanOwnAgentsCompany = "plan_own_agents_company"
)

// Limits refuses a call that would count past what the plan allows, before
// anything is sent. Only a call that can count is ever refused for a meter:
// a call to a connected tool that isn't read-only. Reads are free and never
// stop, and paid KIFF actions never stop. On Solo, which is tafs only, a
// call of the account's own agents that would count is refused. A taf's calls never stop for a
// meter: what its runs use is credits, which the balance checks before each
// run starts (GET /v1/me/credits/allowance). An account with no plan, or
// whose plan ended, has every call stopped.
type Limits struct {
	tenants tenant.Store
	m       *Postgres
	now     func() time.Time
}

// NewLimits checks calls against the plans in tenants and the counts in m.
func NewLimits(tenants tenant.Store, m *Postgres) *Limits {
	return &Limits{tenants: tenants, m: m, now: time.Now}
}

// WithClock sets the clock, for tests.
func (l *Limits) WithClock(now func() time.Time) *Limits {
	l.now = now
	return l
}

// Refusal is why tenantID's agent may not make this call now, with the
// message the agent reads; "" when it may. A call is counted within a
// minute of ending, so a burst can pass a limit by the calls still in
// flight. On an error it allows, and returns the error to log: a meter
// that can't be read must not stop an otherwise valid decision.
func (l *Limits) Refusal(ctx context.Context, tenantID, agentID, tool string) (reason, message string, err error) {
	t, err := l.tenants.Get(ctx, tenantID)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			return "", "", nil
		}
		return "", "", err
	}
	plan := tenant.PlanFor(t)
	switch plan.Canonical {
	case tenant.PlanInternal, tenant.PlanCompany:
		return "", "", nil
	}
	now := l.now().UTC()
	if plan.Canonical == tenant.PlanTrial {
		switch tenant.TrialStatusAt(t, now) {
		case tenant.TrialNotStarted:
			return ReasonPlanNotStarted, "This account has no plan yet, so KIFF refuses its calls until one is chosen. Nothing was sent. Don't retry: stop and tell the owner.", nil
		case tenant.TrialEnded:
			return ReasonPlanTrialEnded, "This account's plan ended, so KIFF refuses its calls until a plan is chosen. Nothing was sent. Don't retry: stop and tell the owner.", nil
		}
	}
	taf, counts, err := l.m.wouldCount(ctx, tenantID, agentID, tool)
	if err != nil || !counts || taf {
		return "", "", err
	}
	if plan.Canonical == tenant.PlanSolo {
		return ReasonPlanOwnAgentsCompany, "Your own agents are on the Company plan. Nothing was sent. Don't retry: tell the owner.", nil
	}
	from, _ := tenant.Period(t, now)
	switch {
	case plan.Capped && plan.IncludedActions > 0:
		n, err := l.m.Counted(ctx, tenantID, UnitAction, from)
		if err != nil || n < plan.IncludedActions {
			return "", "", err
		}
		return ReasonPlanLimitReached, fmt.Sprintf("This account's trial has used its %d KIFF actions. Nothing was sent. Don't retry: stop and tell the owner.", plan.IncludedActions), nil
	}
	return "", "", nil
}

// wouldCount reads whether agentID is a taf's, and whether a call to tool
// can count: it's a connected gateway tool that isn't read-only. A call
// KIFF decides without the gateway is never known to be done, so it never
// counts.
func (p *Postgres) wouldCount(ctx context.Context, tenantID, agentID, tool string) (taf, counts bool, err error) {
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass('gateway.tools') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return false, false, err
	}
	var readOnly *bool
	err = p.pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT kind FROM cloud_meta.agents WHERE tenant_id = $1 AND id = $2), '') = 'taf',
		       (SELECT read_only FROM gateway.tools WHERE tenant_id = $1 AND name = $3)`,
		tenantID, agentID, tool).Scan(&taf, &readOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("meter: would count: %w", err)
	}
	return taf, readOnly != nil && !*readOnly, nil
}
