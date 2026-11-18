package billing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
)

// SeatSetter sets a Team subscription's seats; *Stripe is one.
type SeatSetter interface {
	SetSeats(ctx context.Context, subscriptionID string, seats int) error
}

// SeatMembers lists an account's live members; the membership store is one.
type SeatMembers interface {
	List(ctx context.Context, tenantID string) ([]membership.Member, error)
}

// SeatsReconciler keeps each Team subscription's seats equal to the
// account's people who run things, its admins and editors, and at least 2
// (a product decision): adding an editor, or making an
// approver one, adds a seat from the next invoice. Stripe's answer comes
// back through the webhook, which writes the seats KIFF reads.
type SeatsReconciler struct {
	tenants tenant.Store
	members SeatMembers
	stripe  SeatSetter
	logger  *slog.Logger
}

// NewSeatsReconciler keeps tenants' Team seats in step with members.
func NewSeatsReconciler(tenants tenant.Store, members SeatMembers, stripe SeatSetter, logger *slog.Logger) *SeatsReconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SeatsReconciler{tenants: tenants, members: members, stripe: stripe, logger: logger}
}

// Run reconciles every interval until ctx ends.
func (r *SeatsReconciler) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := r.Once(ctx); err != nil {
			r.logger.WarnContext(ctx, "billing: seats reconcile failed", slog.String("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once reconciles every active Team account; one that fails is left for
// the next run.
func (r *SeatsReconciler) Once(ctx context.Context) error {
	ts, err := r.tenants.List(ctx, tenant.ListFilter{Status: tenant.StatusActive})
	if err != nil {
		return err
	}
	var failed int
	for _, t := range ts {
		l := tenant.PlanFor(t)
		sub, _ := t.Metadata[MetaStripeSubscriptionID].(string)
		if l.Canonical != tenant.PlanTeam || sub == "" {
			continue
		}
		ms, err := r.members.List(ctx, t.ID)
		if err != nil {
			failed++
			continue
		}
		people := 0
		for _, m := range ms {
			if m.Role == membership.RoleAdmin || m.Role == membership.RoleEditor {
				people++
			}
		}
		if want := max(people, tenant.TeamMinSeats); want != l.Seats {
			if err := r.stripe.SetSeats(ctx, sub, want); err != nil {
				failed++
				r.logger.WarnContext(ctx, "billing: set seats failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
				continue
			}
			r.logger.InfoContext(ctx, "billing: seats set", slog.String("tenant", t.ID), slog.Int("seats", want), slog.Int("were", l.Seats))
		}
	}
	if failed > 0 {
		return fmt.Errorf("billing: seats failed for %d accounts", failed)
	}
	return nil
}
