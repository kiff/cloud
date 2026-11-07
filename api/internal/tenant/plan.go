package tenant

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Plan is the plan identifier carried on a tenant's metadata. Plans are
// server-controlled: a tenant cannot self-assert their plan via any HTTP
// surface. Stripe's webhooks (billing.Processor), cmd/kiff-tenant and the
// operator console write it; the cloud reads it.
type Plan string

const (
	// ── The v1 plans ──────────────────────────────────────────────
	//
	// One KIFF account, one plan, covering the tafs app and KIFF Cloud
	// for the account's own agents. There is no free cloud tier.

	// PlanTrial is the 7-day trial: a card at checkout, then Solo
	// unless cancelled. Its included tasks and actions are hard caps.
	// Also where every legacy or unknown plan lands, so nothing
	// escalates by accident.
	PlanTrial Plan = "trial"

	// PlanSolo is one person, for tafs only: credits, sold in the app
	// stores. Its own agents' calls that would count as KIFF actions are
	// refused (meter.ReasonPlanOwnAgentsCompany); those are Company's.
	PlanSolo Plan = "solo"

	// PlanTeam is what kiff.dev sells as Company: paid per seat, one seat
	// or more, with 500 KIFF actions a seat for the account's own agents,
	// pooled, then metered. With priority support.
	// (Before v1 this id named an older multi-control tier; no account
	// carries it.)
	PlanTeam Plan = "team"

	// PlanCompany is the negotiated plan: committed volume, never
	// stopped by KIFF.
	PlanCompany Plan = "company"

	// PlanInternal is the team's own accounts (the nightly Eval, the e2e
	// and smoke accounts, test accounts, the team's): no caps, never
	// billed. Set explicitly by an operator, never inferred, so the
	// Eval doesn't stop at the trial's 25 tasks.
	PlanInternal Plan = "internal"

	// ── Legacy identifiers ────────────────────────────────────────
	//
	// Plans from before v1. No account pays on them, and each maps to the
	// trial: there is no free tier, for old accounts either. Recognized
	// only so the mapping is explicit; never written for new assignments.

	PlanDeveloper   Plan = "developer"
	PlanGrowth      Plan = "growth"
	PlanScale       Plan = "scale"
	PlanEnterprise  Plan = "enterprise"
	PlanFreeDev     Plan = "free_dev"
	PlanLaunchPilot Plan = "launch_pilot"
	PlanBuilder     Plan = "builder"
	PlanAgencyTeam  Plan = "agency_team"
)

// TrialDays is how long the trial lasts.
const TrialDays = 7

// CanonicalPlan maps any plan identifier to its v1 plan. Legacy and
// unknown values collapse to PlanTrial: a typo never escalates a tenant
// to a paid or uncapped plan.
func CanonicalPlan(p Plan) Plan {
	switch q := Plan(strings.ToLower(strings.TrimSpace(string(p)))); q {
	case PlanTrial, PlanSolo, PlanTeam, PlanCompany, PlanInternal:
		return q
	default:
		return PlanTrial
	}
}

// MetadataKeyPlan is the well-known key under which the plan
// identifier lives on Tenant.Metadata. Documented as a constant
// so downstream consumers (limit checks, dashboards, ops tools)
// agree on the name and can be searched as a single string.
const MetadataKeyPlan = "plan"

// MetadataKeyTrialEndsAt is when the trial ends (RFC 3339), stamped when
// it starts: at checkout, or when an existing account is moved onto it.
const MetadataKeyTrialEndsAt = "trial_ends_at"

// MetadataKeyTrialStartedAt is when the trial started (RFC 3339). A trial
// can be set to end later than TrialDays when an account is moved onto it.
// Absent, the trial is the TrialDays before its end.
const MetadataKeyTrialStartedAt = "trial_started_at"

// MetadataKeySeats is the people a Team plan pays for: its Stripe
// subscription's quantity, written by the webhooks. At least TeamMinSeats.
const MetadataKeySeats = "seats"

// TeamMinSeats is the fewest seats a Team (Company) plan has: there is no
// minimum beyond one.
const TeamMinSeats = 1

// MetadataKeySpendLimitCents is a Team (Company) account's spend limit
// beyond its included credits, in euro cents: raised one step at a time,
// signed on the phone by someone who manages billing. Absent, the default.
const MetadataKeySpendLimitCents = "spend_limit_cents"

// MetadataKeyCreditsWarnedPeriod and MetadataKeyCreditsWarnedPct are the
// last warning about the account's credits: the period it was in (its
// start, RFC 3339) and at what share, so each is sent once a period.
const (
	MetadataKeyCreditsWarnedPeriod = "credits_warned_period"
	MetadataKeyCreditsWarnedPct    = "credits_warned_pct"
)

// ErrLimitChanged is a raise of the spend limit from a limit it no longer
// is.
var ErrLimitChanged = errors.New("tenant: the spend limit changed")

// SpendLimitCents is t's spend limit as stored; 0 when none is, which
// reads as the default.
func SpendLimitCents(t Tenant) int64 {
	return int64(metaInt(t.Metadata, MetadataKeySpendLimitCents))
}

// Per person, on Solo and Team (pooled on Team).
const (
	tasksPerSeat   = 20
	actionsPerSeat = 500
)

// MetadataKeyBillingSource is who bills the account's plan: one source per
// account (BillingSource). Written with the plan by the store's webhooks,
// or by an operator for a contract; absent while no plan is billed.
const MetadataKeyBillingSource = "billing_source"

// BillingSource is who bills an account's plan. Packs are one-time, so a
// pack may be bought through either store whatever the plan's source.
type BillingSource string

const (
	// BillingNone: no plan is billed (the trial before checkout, a plan
	// that ended, internal).
	BillingNone BillingSource = ""
	// BillingStripe: Solo or Team, on kiff.dev.
	BillingStripe BillingSource = "stripe"
	// BillingApple: Solo, in the app, through Apple's in-app purchase.
	BillingApple BillingSource = "apple"
	// BillingGoogle: Solo, in the Android app, through Google Play Billing.
	BillingGoogle BillingSource = "google"
	// BillingContract: Company, on its contract.
	BillingContract BillingSource = "contract"
)

// BillingSourceOf is who bills t's plan. Company is on its contract
// whether or not one is stamped; internal is never billed.
func BillingSourceOf(t Tenant) BillingSource {
	switch PlanFor(t).Canonical {
	case PlanCompany:
		return BillingContract
	case PlanInternal:
		return BillingNone
	}
	s, _ := t.Metadata[MetadataKeyBillingSource].(string)
	switch q := BillingSource(strings.ToLower(strings.TrimSpace(s))); q {
	case BillingStripe, BillingApple, BillingGoogle, BillingContract:
		return q
	}
	// A Stripe subscription recorded before the source was stamped: its
	// next webhook stamps it.
	sub, _ := t.Metadata["stripe_subscription_id"].(string)
	switch status, _ := t.Metadata["billing_status"].(string); {
	case sub == "":
	case status == "active", status == "past_due", status == "trialing":
		return BillingStripe
	}
	return BillingNone
}

// ChainTier is the public chain a tenant's receipts are anchored on.
// It comes from the plan, server-controlled: a tenant can't choose it.
// Every receipt is signed whatever the chain, and anchoring is never off.
type ChainTier string

const (
	// ChainSepolia anchors receipts on Base Sepolia, a public test
	// network: anyone can check a batch, but a testnet carries no
	// economic finality and isn't a durability guarantee. Every plan
	// anchors here.
	ChainSepolia ChainTier = "sepolia"

	// ChainMainnet anchors receipts on Base mainnet. It's supported in
	// code but no plan offers it.
	ChainMainnet ChainTier = "mainnet"
)

// PlanLimits is what a plan allows. Consumers ask PlanFor(tenant).
type PlanLimits struct {
	// Plan is the identifier as stored (may be a legacy one); read
	// Canonical for the v1 plan it maps to.
	Plan Plan

	// Canonical is the v1 plan (trial, solo, team, company, internal).
	Canonical Plan

	// IncludedTasks and IncludedActions are the tafs tasks and KIFF
	// actions included each month (on the trial: in its 7 days). Zero
	// is unlimited (internal, company).
	IncludedTasks   int
	IncludedActions int

	// Capped reports that the included actions are a hard limit (the
	// trial): past them, KIFF refuses what would count. On paid plans KIFF
	// actions never stop and are metered. On every plan with included
	// tasks, tafs tasks stop at the included tasks plus the account's pack
	// tasks (PeriodTasks).
	Capped bool

	// Billed reports that KIFF actions past the included ones are
	// reported to Stripe (solo, team). Company is invoiced on its
	// contract; trial and internal never. Tasks are never metered: past
	// the included ones they come from packs.
	Billed bool

	// Seats is how many people may be members: 1 on the trial and Solo,
	// what Team pays for, and no limit (0) on Company and internal.
	Seats int

	// MonthlyProposalLimit bounds the checks (decisions) a month,
	// whatever their outcome: refusals are free, so this keeps them from
	// being abused. Zero or negative is unlimited.
	MonthlyProposalLimit int

	// HardCap reports that crossing MonthlyProposalLimit refuses the
	// check with limit_exceeded. Every bounded plan hard-caps checks;
	// it's an abuse bound, not a billed unit.
	HardCap bool

	// AnchorChain is the public chain the tenant's receipts anchor on.
	// It never disables anchoring, only selects the chain.
	AnchorChain ChainTier

	// MaxDomains optionally caps how many domains a tenant may have.
	// Zero or negative is unlimited, which is the
	// default for every plan: domain count is not a billing unit and
	// not a value gate. The field is retained as a dormant
	// per-contract lever, but no plan sets it.
	MaxDomains int
}

// IsUnlimited reports whether the check limit is non-binding.
func (l PlanLimits) IsUnlimited() bool {
	return l.MonthlyProposalLimit <= 0
}

// Paid reports whether the plan is paid (solo, team, company).
func (l PlanLimits) Paid() bool {
	switch l.Canonical {
	case PlanSolo, PlanTeam, PlanCompany:
		return true
	}
	return false
}

// trialChecks bounds a trial's checks a month: ten times its included
// actions, so a trial that tries things freely never meets it, and a
// stream of free refusals can't run unbounded.
const trialChecks = 5_000

// defaultLimits is the plan table, for a plan of
// seats people (read only for Team).
func defaultLimits(p Plan, seats int) PlanLimits {
	canonical := CanonicalPlan(p)
	// MaxDomains is left at its zero value (unlimited) on every plan.
	switch canonical {
	case PlanSolo:
		// No KIFF actions: Solo is tafs only.
		return PlanLimits{Plan: p, Canonical: canonical, IncludedTasks: tasksPerSeat,
			Billed: true, Seats: 1, AnchorChain: ChainSepolia}
	case PlanTeam:
		seats = max(seats, TeamMinSeats)
		return PlanLimits{Plan: p, Canonical: canonical, IncludedTasks: tasksPerSeat * seats, IncludedActions: actionsPerSeat * seats,
			Billed: true, Seats: seats, AnchorChain: ChainSepolia}
	case PlanCompany:
		return PlanLimits{Plan: p, Canonical: canonical, AnchorChain: ChainSepolia}
	case PlanInternal:
		return PlanLimits{Plan: p, Canonical: canonical, AnchorChain: ChainSepolia}
	default:
		// PlanTrial, and every legacy or unknown plan. The trial is Solo's
		// one person.
		return PlanLimits{Plan: p, Canonical: PlanTrial, IncludedTasks: 25, IncludedActions: 500, Capped: true, Seats: 1,
			MonthlyProposalLimit: trialChecks, HardCap: true, AnchorChain: ChainSepolia}
	}
}

// PlanFor returns the effective PlanLimits for a tenant, from
// Tenant.Metadata[MetadataKeyPlan] and, for Team, MetadataKeySeats
// (both server-controlled). A missing or unrecognized plan is the trial's.
func PlanFor(t Tenant) PlanLimits {
	return defaultLimits(planFromMetadata(t.Metadata), metaInt(t.Metadata, MetadataKeySeats))
}

// metaInt reads a whole number from metadata, which JSON decodes as
// float64; 0 when absent or not a number.
func metaInt(meta map[string]any, key string) int {
	switch v := meta[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// planFromMetadata extracts the plan identifier from a metadata map.
// Tolerates string, custom-typed strings, and missing keys; anything
// it doesn't recognize is the trial, so a typo on the ops side cannot
// grant more.
func planFromMetadata(meta map[string]any) Plan {
	raw, ok := meta[MetadataKeyPlan]
	if !ok {
		return PlanTrial
	}
	s, ok := raw.(string)
	if !ok {
		return PlanTrial
	}
	switch p := Plan(strings.ToLower(strings.TrimSpace(s))); p {
	case PlanTrial, PlanSolo, PlanTeam, PlanCompany, PlanInternal,
		PlanDeveloper, PlanGrowth, PlanScale, PlanEnterprise,
		PlanFreeDev, PlanLaunchPilot, PlanBuilder, PlanAgencyTeam:
		return p
	default:
		return PlanTrial
	}
}

// TrialEndsAt reads when t's trial ends, stamped when it started.
// False when none is stamped.
func TrialEndsAt(t Tenant) (time.Time, bool) {
	return metaTime(t.Metadata, MetadataKeyTrialEndsAt)
}

// TrialStatus is where an account on the trial stands.
type TrialStatus int

const (
	// TrialNotStarted: no end is stamped. The trial starts with a card at
	// checkout, or with the store's free week.
	TrialNotStarted TrialStatus = iota
	// TrialRunning: started, and its end is still ahead.
	TrialRunning
	// TrialEnded: its end has passed, or a plan ended (an ended plan
	// stamps the trial's end at that moment).
	TrialEnded
)

// TrialStatusAt is where t's trial stands at now. Only meaningful on the
// trial plan: a paid plan's account is never asked.
func TrialStatusAt(t Tenant, now time.Time) TrialStatus {
	end, ok := TrialEndsAt(t)
	switch {
	case !ok:
		return TrialNotStarted
	case now.Before(end):
		return TrialRunning
	}
	return TrialEnded
}

// Runs reports that t's tafs may run at now: any plan but a trial that
// hasn't started or has ended. KIFF refuses such an account's calls
// (meter.Limits), so it has no tasks to add packs to: packs it holds
// count again once a plan starts.
func Runs(t Tenant, now time.Time) bool {
	return PlanFor(t).Canonical != PlanTrial || TrialStatusAt(t, now) == TrialRunning
}

// PeriodTasks is the tafs tasks t has this period: what its plan includes
// (per seat on Team), and packTasks, the pack tasks it has for the period
// (those drawn in it, and those still valid). At that number its tafs stop
// until someone adds a pack. Zero is no limit (internal, company).
func PeriodTasks(t Tenant, packTasks int) int {
	l := PlanFor(t)
	if l.IncludedTasks == 0 {
		return 0
	}
	return l.IncludedTasks + max(packTasks, 0)
}

// PlanResolver implements the usage.PlanResolver contract by
// reading tenant.Metadata via Store.Get. Production wiring uses
// it as the source of truth for the check limiter; tests
// pass either this (with a MemoryStore) or a stub.
//
// Lives in the tenant package because the plan/limit shape lives
// here. The usage package consumes it via its own interface; the
// import direction is usage -> tenant only.
type PlanResolver struct {
	store Store
}

// NewPlanResolver wires a tenant.Store. store must be non-nil.
func NewPlanResolver(store Store) *PlanResolver {
	return &PlanResolver{store: store}
}

// PlanProposalLimit returns the tenant's monthly check bound and
// whether crossing it refuses (every bounded plan does). Returns the
// unlimited sentinel (0) when the tenant is not found — the
// proposal handler treats unknown tenants as unlimited because the
// auth middleware would have already rejected an invalid identity.
func (r *PlanResolver) PlanProposalLimit(ctx context.Context, tenantID string) (limit int, hardCap bool, err error) {
	if r == nil || r.store == nil {
		return 0, false, nil
	}
	t, err := r.store.Get(ctx, tenantID)
	if err != nil {
		// Distinguish ErrNotFound (allow) from other errors
		// (which the limiter treats as allow-when-uncertain).
		if err == ErrNotFound {
			return 0, false, nil
		}
		return 0, false, err
	}
	pl := PlanFor(t)
	return pl.MonthlyProposalLimit, pl.HardCap, nil
}

// MetadataKeyAIGoOn is the start (RFC 3339) of the period the owner's
// go-ons past the AI fair-use bound are for, and MetadataKeyAIGoOns how
// many they gave in it. They hold for that period only.
const (
	MetadataKeyAIGoOn  = "ai_go_on_period"
	MetadataKeyAIGoOns = "ai_go_ons"
)

// MetadataKeyPeriodStart and MetadataKeyPeriodEnd are the paid plan's
// current billing period (RFC 3339), from Stripe's subscription, written
// by the webhooks.
const (
	MetadataKeyPeriodStart = "billing_period_start"
	MetadataKeyPeriodEnd   = "billing_period_end"
)

// Period is the billing period t is in at now, [from, to): the trial; a
// paid plan's Stripe period, rolled forward a month at a time when its
// renewal hasn't been recorded yet, so the period's start (and what counts
// toward it) doesn't jump to the calendar month while a webhook is late;
// else the UTC calendar month.
func Period(t Tenant, now time.Time) (from, to time.Time) {
	now = now.UTC()
	l := PlanFor(t)
	if l.Canonical == PlanTrial {
		if from, to, ok := trialPeriod(t); ok {
			return from, to
		}
	}
	if l.Paid() {
		from, ok1 := metaTime(t.Metadata, MetadataKeyPeriodStart)
		to, ok2 := metaTime(t.Metadata, MetadataKeyPeriodEnd)
		if ok1 && ok2 && from.Before(to) && !now.Before(from) {
			// Each step is counted from the stored end, clamped to the
			// month's last day, so a 31st stays the month's end (31 Jan,
			// 28 Feb, 31 Mar) rather than drifting (3 Mar).
			end := to
			for k := 1; !now.Before(to); k++ {
				from, to = to, addMonths(end, k)
			}
			return from, to
		}
	}
	from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return from, from.AddDate(0, 1, 0)
}

// PeriodAt is the period of t's plan that at falls in, as of now:
// Period's for an instant in or after the current period, and for one
// before it (a run that ended just before the period rolled over, read
// after), the months before it, each stepped back from the current
// period's start, clamped as Period's steps forward are. At most
// maxPeriodsBack months back: older than that is the earliest of them.
func PeriodAt(t Tenant, at, now time.Time) (from, to time.Time) {
	from, to = Period(t, now)
	at = at.UTC()
	if !at.Before(from) {
		if at.Before(to) {
			return from, to
		}
		return Period(t, at)
	}
	start := from
	for k := 1; at.Before(from) && k <= maxPeriodsBack; k++ {
		from, to = addMonths(start, -k), addMonths(start, 1-k)
	}
	return from, to
}

// maxPeriodsBack bounds how far back PeriodAt steps.
const maxPeriodsBack = 36

// PeriodFrom is the period of t's plan that starts at start: the trial,
// else a month.
func PeriodFrom(t Tenant, start time.Time) (from, to time.Time) {
	start = start.UTC()
	if PlanFor(t).Canonical == PlanTrial {
		if from, to, ok := trialPeriod(t); ok && from.Equal(start) {
			return from, to
		}
		return start, start.AddDate(0, 0, TrialDays)
	}
	return start, start.AddDate(0, 1, 0)
}

// trialPeriod is t's trial, [start, end): from its stamped start, else the
// TrialDays before its end. False when no end is stamped.
func trialPeriod(t Tenant) (from, to time.Time, ok bool) {
	to, ok = TrialEndsAt(t)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	from = to.AddDate(0, 0, -TrialDays)
	if start, ok := metaTime(t.Metadata, MetadataKeyTrialStartedAt); ok && start.Before(to) {
		from = start
	}
	return from, to, true
}

// addMonths is t moved by n calendar months, on the same day of the month,
// or the month's last day when it has fewer days.
func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(d, last)-1)
}

func metaTime(meta map[string]any, key string) (time.Time, bool) {
	s, _ := meta[key].(string)
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, false
	}
	return at.UTC(), true
}
