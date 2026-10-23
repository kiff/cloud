package tenant_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
)

func withMeta(meta map[string]any) tenant.Tenant {
	return tenant.Tenant{ID: tenant.NewID(), Slug: "x", Status: tenant.StatusActive, Metadata: meta}
}

// The plan table: what each plan includes, whether it caps, and whether
// it's billed.
func TestPlanFor_ThePlans(t *testing.T) {
	t.Parallel()
	for plan, want := range map[string]tenant.PlanLimits{
		"trial":    {Canonical: tenant.PlanTrial, IncludedTasks: 25, IncludedActions: 500, Capped: true, Seats: 1, MonthlyProposalLimit: 5_000, HardCap: true, AnchorChain: tenant.ChainSepolia},
		"solo":     {Canonical: tenant.PlanSolo, IncludedTasks: 20, IncludedActions: 500, Billed: true, Seats: 1, AnchorChain: tenant.ChainSepolia},
		"team":     {Canonical: tenant.PlanTeam, IncludedTasks: 40, IncludedActions: 1_000, Billed: true, Seats: 2, AnchorChain: tenant.ChainSepolia},
		"company":  {Canonical: tenant.PlanCompany, AnchorChain: tenant.ChainSepolia},
		"internal": {Canonical: tenant.PlanInternal, AnchorChain: tenant.ChainSepolia},
	} {
		got := tenant.PlanFor(withMeta(map[string]any{tenant.MetadataKeyPlan: plan}))
		want.Plan = tenant.Plan(plan)
		if got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", plan, got, want)
		}
	}
}

// Team pools 20 tasks and 500 actions per person, and has at least two.
func TestPlanFor_TeamPoolsPerSeat(t *testing.T) {
	t.Parallel()
	for seats, want := range map[any][3]int{float64(5): {5, 100, 2_500}, float64(1): {2, 40, 1_000}, nil: {2, 40, 1_000}, "3": {2, 40, 1_000}} {
		meta := map[string]any{tenant.MetadataKeyPlan: "team"}
		if seats != nil {
			meta[tenant.MetadataKeySeats] = seats
		}
		l := tenant.PlanFor(withMeta(meta))
		if got := [3]int{l.Seats, l.IncludedTasks, l.IncludedActions}; got != want {
			t.Errorf("seats %v: %v, want %v", seats, got, want)
		}
	}
	// Seats are read for Team only.
	if l := tenant.PlanFor(withMeta(map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeySeats: float64(5)})); l.Seats != 1 || l.IncludedTasks != 20 {
		t.Errorf("solo with seats: %+v", l)
	}
}

// There is no free tier: a missing, unknown, mistyped or legacy plan is
// the trial, so nothing escalates by accident.
func TestPlanFor_EverythingElseIsTheTrial(t *testing.T) {
	t.Parallel()
	for name, meta := range map[string]map[string]any{
		"none":       nil,
		"no plan":    {},
		"unknown":    {tenant.MetadataKeyPlan: "enterprise_unlimited"},
		"not a text": {tenant.MetadataKeyPlan: 42},
		"developer":  {tenant.MetadataKeyPlan: "developer"},
		"growth":     {tenant.MetadataKeyPlan: "growth"},
		"scale":      {tenant.MetadataKeyPlan: "scale"},
		"enterprise": {tenant.MetadataKeyPlan: "enterprise"},
		"free_dev":   {tenant.MetadataKeyPlan: "free_dev"},
		"builder":    {tenant.MetadataKeyPlan: "builder"},
	} {
		l := tenant.PlanFor(withMeta(meta))
		if l.Canonical != tenant.PlanTrial || !l.Capped || l.Billed || l.Paid() || l.IncludedTasks != 25 {
			t.Errorf("%s: %+v", name, l)
		}
	}
}

func TestCanonicalPlan(t *testing.T) {
	t.Parallel()
	for in, want := range map[tenant.Plan]tenant.Plan{
		"trial": tenant.PlanTrial, "solo": tenant.PlanSolo, "team": tenant.PlanTeam,
		"company": tenant.PlanCompany, "internal": tenant.PlanInternal,
		"  SOLO ": tenant.PlanSolo, "growth": tenant.PlanTrial, "": tenant.PlanTrial,
	} {
		if got := tenant.CanonicalPlan(in); got != want {
			t.Errorf("CanonicalPlan(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlanFor_DomainsUncapped(t *testing.T) {
	t.Parallel()
	for _, plan := range []string{"trial", "solo", "team", "company", "internal"} {
		if l := tenant.PlanFor(withMeta(map[string]any{tenant.MetadataKeyPlan: plan})); l.MaxDomains != 0 {
			t.Errorf("%s caps domains at %d", plan, l.MaxDomains)
		}
	}
}

// The ceiling: the owner's on a billed plan, never below what's included;
// the included tasks otherwise; none on company and internal.
func TestTaskCeiling(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		meta map[string]any
		want int
	}{
		"solo, none set":       {map[string]any{tenant.MetadataKeyPlan: "solo"}, 20},
		"solo, raised":         {map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyTaskCeiling: float64(60)}, 60},
		"solo, below included": {map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyTaskCeiling: float64(5)}, 20},
		"trial, set anyway":    {map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTaskCeiling: float64(60)}, 25},
		"internal":             {map[string]any{tenant.MetadataKeyPlan: "internal"}, 0},
	} {
		if got := tenant.TaskCeiling(withMeta(c.meta)); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

// The period: the trial (its 7 days, or from its stamped start), a paid
// plan's Stripe period while it covers now, else the calendar month.
func TestPeriod(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	day := func(d int, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC) }
	for name, c := range map[string]struct {
		meta     map[string]any
		from, to time.Time
	}{
		"trial":            {map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialEndsAt: "2026-10-25T09:00:00Z"}, day(18, 9), day(25, 9)},
		"trial, started":   {map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialStartedAt: "2026-10-01T09:00:00Z", tenant.MetadataKeyTrialEndsAt: "2026-11-05T00:00:00Z"}, day(1, 9), time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)},
		"trial, bad start": {map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialStartedAt: "2026-10-26T09:00:00Z", tenant.MetadataKeyTrialEndsAt: "2026-10-25T09:00:00Z"}, day(18, 9), day(25, 9)},
		"trial, no end":    {map[string]any{tenant.MetadataKeyPlan: "trial"}, day(1, 0), day(1, 0).AddDate(0, 1, 0)},
		"solo, Stripe's":   {map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyPeriodStart: "2026-10-12T08:00:00Z", tenant.MetadataKeyPeriodEnd: "2026-11-12T08:00:00Z"}, day(12, 8), day(12, 8).AddDate(0, 1, 0)},
		// The renewal's webhook is late: the period rolls forward from
		// Stripe's, not to the calendar month.
		"solo, Stripe's late":      {map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyPeriodStart: "2026-09-12T08:00:00Z", tenant.MetadataKeyPeriodEnd: "2026-10-12T08:00:00Z"}, day(12, 8), day(12, 8).AddDate(0, 1, 0)},
		"solo, Stripe's long late": {map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyPeriodStart: "2026-07-12T08:00:00Z", tenant.MetadataKeyPeriodEnd: "2026-08-12T08:00:00Z"}, day(12, 8), day(12, 8).AddDate(0, 1, 0)},
		// A 31st anchor stays the month's end, two months late.
		"solo, 31st late": {map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyPeriodStart: "2026-07-31T08:00:00Z", tenant.MetadataKeyPeriodEnd: "2026-08-31T08:00:00Z"}, time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)},
		"solo, no period": {map[string]any{tenant.MetadataKeyPlan: "solo"}, day(1, 0), day(1, 0).AddDate(0, 1, 0)},
		"internal":        {map[string]any{tenant.MetadataKeyPlan: "internal"}, day(1, 0), day(1, 0).AddDate(0, 1, 0)},
	} {
		from, to := tenant.Period(withMeta(c.meta), now)
		if !from.Equal(c.from) || !to.Equal(c.to) {
			t.Errorf("%s: [%v, %v), want [%v, %v)", name, from, to, c.from, c.to)
		}
	}
	if from, to := tenant.PeriodFrom(withMeta(map[string]any{tenant.MetadataKeyPlan: "trial"}), day(3, 0)); !to.Equal(from.AddDate(0, 0, 7)) {
		t.Errorf("a trial period is 7 days: %v, %v", from, to)
	}
	// Asked by its start, a longer trial is the whole trial.
	long := withMeta(map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialStartedAt: "2026-10-01T09:00:00Z", tenant.MetadataKeyTrialEndsAt: "2026-11-05T00:00:00Z"})
	if from, to := tenant.PeriodFrom(long, day(1, 9)); !from.Equal(day(1, 9)) || !to.Equal(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("a longer trial asked by its start: [%v, %v)", from, to)
	}
}

func TestPlanResolver(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	trial, solo := tenant.NewID(), tenant.NewID()
	for id, plan := range map[string]string{trial: "trial", solo: "solo"} {
		if err := store.Create(context.Background(), tenant.Tenant{ID: id, Slug: plan, Status: tenant.StatusActive,
			Metadata: map[string]any{tenant.MetadataKeyPlan: plan}}); err != nil {
			t.Fatal(err)
		}
	}
	r := tenant.NewPlanResolver(store)
	// The trial's checks are bounded, so free refusals can't run unbounded.
	if limit, hardCap, err := r.PlanProposalLimit(context.Background(), trial); err != nil || limit != 5_000 || !hardCap {
		t.Errorf("trial: %d %v %v", limit, hardCap, err)
	}
	// A paid plan's checks are not.
	if limit, _, err := r.PlanProposalLimit(context.Background(), solo); err != nil || limit != 0 {
		t.Errorf("solo: %d %v", limit, err)
	}
	// An unknown tenant, or no store: unlimited (auth rejected it already).
	if limit, _, err := r.PlanProposalLimit(context.Background(), tenant.NewID()); err != nil || limit != 0 {
		t.Errorf("unknown tenant: %d %v", limit, err)
	}
	var none *tenant.PlanResolver
	if limit, _, err := none.PlanProposalLimit(context.Background(), "any"); err != nil || limit != 0 {
		t.Errorf("nil resolver: %d %v", limit, err)
	}
}
