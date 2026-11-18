package billing

import (
	"context"
	"strconv"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/tenant"
)

// RunTotals reads an account's runs in a period; *meter.Postgres is one.
type RunTotals interface {
	RunTotals(ctx context.Context, tenantID string, from, to time.Time) (meter.RunTotals, error)
}

// CreditPacks reads the pack credits drawn in a period and those left;
// *packs.Postgres is one.
type CreditPacks interface {
	Credits(ctx context.Context, tenantID string, from, to time.Time) (drawn, left int64, err error)
}

// CreditWarnings tells an account's alert targets when its tafs have used
// 80% of what the period has, and again when they've used all of it and
// wait: once each a period, however many look at once (MarkCreditsWarned).
// On Solo that's the included credits and the packs; on Company, the spend
// limit beyond the included credits. A receiver, the tafs app's backend,
// tells the people who manage billing.
type CreditWarnings struct {
	tenants tenant.Store
	totals  RunTotals
	packs   CreditPacks
	alerts  alert.Notifier
}

// NewCreditWarnings reads runs from totals and packs from packs (nil: none),
// and tells alerts.
func NewCreditWarnings(tenants tenant.Store, totals RunTotals, packs CreditPacks, alerts alert.Notifier) *CreditWarnings {
	return &CreditWarnings{tenants: tenants, totals: totals, packs: packs, alerts: alerts}
}

// Check warns about t's balance at now, if it's past a warning not yet sent
// this period.
func (c *CreditWarnings) Check(ctx context.Context, t tenant.Tenant, now time.Time) error {
	kind, seats := meter.KindOf(t)
	if kind != meter.BalanceSolo && kind != meter.BalanceCompany {
		return nil
	}
	from, to := tenant.Period(t, now)
	tot, err := c.totals.RunTotals(ctx, t.ID, from, to)
	if err != nil {
		return err
	}
	in := meter.BalanceIn{Kind: kind, Seats: seats, Used: tot.Credits, Over: tot.Over, LimitCents: tenant.SpendLimitCents(t)}
	if kind == meter.BalanceSolo && c.packs != nil {
		if in.PacksDrawn, in.PacksLeft, err = c.packs.Credits(ctx, t.ID, from, to); err != nil {
			return err
		}
	}
	b := meter.ComputeBalance(in)
	pct := meter.WarnPct
	switch {
	case b.Waiting():
		pct = 100
	case b.State != meter.StateWarning:
		return nil
	}
	first, err := c.tenants.MarkCreditsWarned(ctx, t.ID, from, pct)
	if err != nil || !first || c.alerts == nil {
		return err
	}
	reasons := []string{strconv.Itoa(pct)}
	if b.Why != "" {
		reasons = append(reasons, b.Why)
	}
	c.alerts.Notify(alert.Event{TenantID: t.ID, Action: "tafs.credits", EntityType: "credits", EntityID: from.UTC().Format(time.RFC3339),
		Outcome: alert.CreditsWarning, Reasons: reasons, DecidedAt: now.UTC()})
	return nil
}
