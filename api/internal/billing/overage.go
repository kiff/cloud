package billing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// OverageLedger is what the reporter needs of the meter.
type OverageLedger interface {
	Pending(ctx context.Context) ([]string, error)
	Unsettled(ctx context.Context, tenantID string, unit meter.Unit, from, to time.Time) ([]meter.Placed, error)
	Settle(ctx context.Context, tenantID string, keys []string) error
	SettleBefore(ctx context.Context, tenantID string, t time.Time) error
}

// OverageSender sends one meter event; *MeterEventClient is one.
type OverageSender interface {
	Send(ctx context.Context, customerID, identifier string, value int, at time.Time) error
}

// RunLedger is what settling runs needs of the run ledger;
// *meter.Postgres is one.
type RunLedger interface {
	PendingRunTenants(ctx context.Context) ([]string, error)
	UnsettledRuns(ctx context.Context, tenantID string, from, to time.Time) ([]meter.UnsettledRun, error)
	SettleRunsBefore(ctx context.Context, tenantID string, t time.Time) error
	// RunTotals and SettleRuns are Company's: what a period's counted runs
	// used, and settling the runs whose credits are accounted for.
	RunTotals(ctx context.Context, tenantID string, from, to time.Time) (meter.RunTotals, error)
	SettleRuns(ctx context.Context, tenantID string, runs []string) error
}

// RunSettler takes a run's credits from Solo's balance: the included
// credits, then the packs; *packs.Postgres is one.
type RunSettler interface {
	SettleRun(ctx context.Context, tenantID, run string, from, to time.Time, included int64) (packs.Settled, error)
}

// OverageReporter settles each counted call past what the period
// includes. A KIFF action past the included ones is one meter event on
// Stripe's action overage price: KIFF works out what's
// included (Team's grows with its seats), so Stripe meters only what's
// billed. Each is sent once, with its key as Stripe's dedup identifier,
// and is settled only after Stripe accepted it, so an outage loses or
// doubles nothing. A tafs task past the included ones is never billed: it
// was drawn from packs; tasks are counted no more, and are settled with
// nothing drawn. Calls within what's included, and actions of a plan that
// isn't billed (the trial, internal, company on its contract), are settled
// with nothing sent.
//
// It also settles each run of a taf's or Coach's that used credits
// (WithRuns): on Solo, its credits come from the period's included credits,
// then the packs, oldest first, and what's past both is the free overshoot.
// On Company (Team), its credits come from the period's included ones
// (seats × meter.CompanyCreditsPerSeat), oldest first, and what's past them
// is one meter event on Stripe's credit overage price (WithCredits), keyed
// by the run, so each run is billed once; a run is settled only after
// Stripe took it. On any other plan it's settled taking nothing.
type OverageReporter struct {
	ledger  OverageLedger
	tenants tenant.Store
	actions OverageSender
	credits OverageSender
	runs    RunLedger
	settler RunSettler
	warn    *CreditWarnings
	logger  *slog.Logger
	now     func() time.Time
}

// NewOverageReporter settles ledger's calls: actions sent to Stripe through
// actions (nil: kept pending).
func NewOverageReporter(ledger OverageLedger, tenants tenant.Store, actions OverageSender, logger *slog.Logger) *OverageReporter {
	if logger == nil {
		logger = slog.Default()
	}
	return &OverageReporter{ledger: ledger, tenants: tenants, actions: actions, logger: logger, now: time.Now}
}

// WithRuns settles the runs kept in runs, Solo's through settler.
func (r *OverageReporter) WithRuns(runs RunLedger, settler RunSettler) *OverageReporter {
	r.runs, r.settler = runs, settler
	return r
}

// WithCredits sends Company's credits past the included ones to Stripe
// through credits, on its credit overage price. Nil: they're kept pending.
func (r *OverageReporter) WithCredits(credits OverageSender) *OverageReporter {
	r.credits = credits
	return r
}

// WithWarnings warns, through w, about each account whose runs it settled:
// at 80% and 100% of what the period has.
func (r *OverageReporter) WithWarnings(w *CreditWarnings) *OverageReporter {
	r.warn = w
	return r
}

// WithClock sets the clock, for tests.
func (r *OverageReporter) WithClock(now func() time.Time) *OverageReporter {
	r.now = now
	return r
}

// Run reports every interval until ctx ends.
func (r *OverageReporter) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := r.Once(ctx); err != nil {
			r.logger.WarnContext(ctx, "billing: overage report failed", slog.String("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once settles every pending account, its calls and its runs. An account
// that fails is left for the next run; the others go on.
func (r *OverageReporter) Once(ctx context.Context) error {
	ids, err := r.ledger.Pending(ctx)
	if err != nil {
		return err
	}
	var failed int
	for _, id := range ids {
		if err := r.account(ctx, id); err != nil {
			failed++
			r.logger.WarnContext(ctx, "billing: overage for an account failed", slog.String("tenant", id), slog.String("err", err.Error()))
		}
	}
	all := len(ids)
	if r.runs != nil {
		ids, err := r.runs.PendingRunTenants(ctx)
		if err != nil {
			return err
		}
		all += len(ids)
		for _, id := range ids {
			if err := r.accountRuns(ctx, id); err != nil {
				failed++
				r.logger.WarnContext(ctx, "billing: settling an account's runs failed", slog.String("tenant", id), slog.String("err", err.Error()))
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("billing: overage failed for %d of %d accounts", failed, all)
	}
	return nil
}

// accountRuns settles an account's runs. On Solo, each run of the current
// period and the one before is settled in turn, oldest first, against the
// included credits and the packs; older ones, and any other plan's, are
// settled taking nothing.
func (r *OverageReporter) accountRuns(ctx context.Context, tenantID string) error {
	t, err := r.tenants.Get(ctx, tenantID)
	if err != nil {
		return err
	}
	now := r.now().UTC()
	if err := r.settleRuns(ctx, t, now); err != nil {
		return err
	}
	if r.warn != nil {
		// A warning that can't be sent now is looked at again with the
		// account's next run: the runs are settled either way.
		if err := r.warn.Check(ctx, t, now); err != nil {
			r.logger.WarnContext(ctx, "billing: credits warning failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		}
	}
	return nil
}

func (r *OverageReporter) settleRuns(ctx context.Context, t tenant.Tenant, now time.Time) error {
	tenantID := t.ID
	kind, seats := meter.KindOf(t)
	if kind == meter.BalanceCompany {
		return r.settleCompanyRuns(ctx, t, seats, now)
	}
	if kind != meter.BalanceSolo || r.settler == nil {
		return r.runs.SettleRunsBefore(ctx, tenantID, now.Add(time.Hour))
	}
	from, to := tenant.Period(t, now)
	prev, _ := tenant.PeriodAt(t, from.Add(-time.Nanosecond), now)
	if err := r.runs.SettleRunsBefore(ctx, tenantID, prev); err != nil {
		return err
	}
	for _, p := range [][2]time.Time{{prev, from}, {from, to}} {
		runs, err := r.runs.UnsettledRuns(ctx, tenantID, p[0], p[1])
		if err != nil {
			return err
		}
		for _, u := range runs {
			if _, err := r.settler.SettleRun(ctx, tenantID, u.Run, p[0], p[1], meter.SoloIncludedCredits); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *OverageReporter) account(ctx context.Context, tenantID string) error {
	t, err := r.tenants.Get(ctx, tenantID)
	if err != nil {
		return err
	}
	now := r.now().UTC()
	l := tenant.PlanFor(t)
	customer, _ := t.Metadata[MetaStripeCustomerID].(string)
	if !l.Billed && l.IncludedTasks == 0 {
		return r.ledger.SettleBefore(ctx, tenantID, now)
	}
	from, to := tenant.Period(t, now)
	// The period before this one too, for calls Stripe couldn't take
	// before it ended; anything older is settled as it is.
	prev := from.AddDate(0, -1, 0)
	if err := r.ledger.SettleBefore(ctx, tenantID, prev); err != nil {
		return err
	}
	for _, p := range [][2]time.Time{{prev, from}, {from, to}} {
		// Tasks are counted no more: settled with nothing drawn.
		if err := r.settle(ctx, tenantID, meter.UnitTask, p[0], p[1], func(meter.Placed) error { return nil }); err != nil {
			return err
		}
		switch {
		case !l.Billed, l.Canonical == tenant.PlanSolo:
			// Unbilled plans, and Solo, which has no KIFF actions (its own
			// agents' calls are refused): settled with nothing sent.
			err = r.settle(ctx, tenantID, meter.UnitAction, p[0], p[1], func(meter.Placed) error { return nil })
		case r.actions == nil:
			// No Stripe here (an API without its key): actions stay
			// pending until one reports them.
			continue
		case customer == "":
			// A paid plan with no Stripe customer is a data problem: say
			// so, and keep its actions pending so they're reported once
			// it's fixed.
			r.logger.WarnContext(ctx, "billing: paid plan without a Stripe customer; overage kept pending",
				slog.String("tenant", tenantID), slog.String("plan", string(l.Canonical)))
			continue
		default:
			err = r.settle(ctx, tenantID, meter.UnitAction, p[0], p[1], func(c meter.Placed) error {
				if c.Place <= l.IncludedActions {
					return nil
				}
				return r.actions.Send(ctx, customer, c.Key, 1, c.At)
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// settleCompanyRuns settles Company's runs of the current period and the one
// before, oldest first, against the period's included credits; the credits
// past them are sent to Stripe, one meter event per run. Older runs are
// settled taking nothing, as Stripe takes no event that old. With no
// sender, or a paid plan with no Stripe customer, they're kept pending.
func (r *OverageReporter) settleCompanyRuns(ctx context.Context, t tenant.Tenant, seats int, now time.Time) error {
	from, to := tenant.Period(t, now)
	prev, _ := tenant.PeriodAt(t, from.Add(-time.Nanosecond), now)
	if err := r.runs.SettleRunsBefore(ctx, t.ID, prev); err != nil {
		return err
	}
	customer, _ := t.Metadata[MetaStripeCustomerID].(string)
	switch {
	case r.credits == nil:
		return nil // no Stripe here: kept until one reports them
	case customer == "":
		r.logger.WarnContext(ctx, "billing: paid plan without a Stripe customer; credit overage kept pending",
			slog.String("tenant", t.ID))
		return nil
	}
	included := meter.CompanyCreditsPerSeat * int64(max(seats, 1))
	for _, p := range [][2]time.Time{{prev, from}, {from, to}} {
		runs, err := r.runs.UnsettledRuns(ctx, t.ID, p[0], p[1])
		if err != nil || len(runs) == 0 {
			if err != nil {
				return err
			}
			continue
		}
		tot, err := r.runs.RunTotals(ctx, t.ID, p[0], p[1])
		if err != nil {
			return err
		}
		// What the period's settled runs used: they came first.
		used := tot.Credits
		for _, u := range runs {
			used -= u.Credits
		}
		for _, u := range runs {
			before := used
			used += u.Credits
			if over := used - max(before, included); over > 0 {
				if err := r.credits.Send(ctx, customer, "credit-"+u.Run, int(over), u.Ended); err != nil {
					return err
				}
			}
			if err := r.runs.SettleRuns(ctx, t.ID, []string{u.Run}); err != nil {
				return err
			}
		}
	}
	return nil
}

// settle applies each of unit's unsettled calls in [from, to) and settles
// it once applied. A call apply fails on stays for the next run.
func (r *OverageReporter) settle(ctx context.Context, tenantID string, unit meter.Unit, from, to time.Time, apply func(meter.Placed) error) error {
	calls, err := r.ledger.Unsettled(ctx, tenantID, unit, from, to)
	if err != nil {
		return err
	}
	var done []string
	for _, c := range calls {
		if err := apply(c); err != nil {
			_ = r.ledger.Settle(ctx, tenantID, done)
			return err
		}
		done = append(done, c.Key)
	}
	return r.ledger.Settle(ctx, tenantID, done)
}
