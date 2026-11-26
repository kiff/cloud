package meter

import "github.com/kiff/cloud/api/internal/tenant"

// The plans' figures in credits and euro cents: KIFF's config, kept here and
// never in the packages KIFF exports.
const (
	// SoloIncludedCredits are Solo's credits each period.
	SoloIncludedCredits = 500
	// CompanyCreditsPerSeat are each Company seat's credits each period,
	// pooled across the account.
	CompanyCreditsPerSeat = 500
	// CentsPerCredit is what each credit beyond the included ones costs on
	// Company's invoice, in euro cents.
	CentsPerCredit = 1
	// SpendLimitStepCents is Company's spend limit beyond its included
	// credits until someone raises it, and what each raise adds.
	SpendLimitStepCents = 5000
	// MaxSpendLimitSteps is how many steps above the default a raise from
	// the phone may take the limit.
	MaxSpendLimitSteps = 10
	// NoPlanSetupMicroUSD is what Setup's (Coach's) runs may cost an account
	// with no plan, once: about 0.50 euro at the rate credits are read at.
	// KIFF pays for them.
	NoPlanSetupMicroUSD = 540_000
	// WarnPct is the first warning, as a share of what the period has; the
	// second is at 100.
	WarnPct = 80
)

// BalanceKind is how an account's plan bounds its credits.
type BalanceKind string

const (
	// BalanceSolo: the period's included credits, then the packs' credits.
	BalanceSolo BalanceKind = "solo"
	// BalanceCompany: each seat's included credits, pooled, then credits on
	// the invoice up to the spend limit.
	BalanceCompany BalanceKind = "company"
	// BalanceUnbounded: an agreement, or internal. Nothing bounds it.
	BalanceUnbounded BalanceKind = "unbounded"
	// BalanceNone: no plan. No taf runs.
	BalanceNone BalanceKind = "none"
)

// A balance's states.
const (
	StateOK      = "ok"
	StateWarning = "warning"
	StateWaiting = "waiting"
)

// Why the tafs wait: the codes a refused run carries too.
const (
	WhyUsedUp = "credits_used_up"
	WhyLimit  = "limit_reached"
	WhyNoPlan = "no_plan"
)

// BalanceIn is what a balance is worked out from.
type BalanceIn struct {
	Kind BalanceKind
	// Seats are Company's seats.
	Seats int
	// Used are the period's counted credits. Over is Solo's free overshoot
	// settled in the period: credits runs used past the balance, which
	// nothing pays for.
	Used, Over int64
	// PacksDrawn are the pack credits drawn for the period's runs, and
	// PacksLeft those left on the account's packs now (Solo).
	PacksDrawn, PacksLeft int64
	// LimitCents is Company's spend limit beyond its included credits; 0
	// reads as the default.
	LimitCents int64
}

// Balance is an account's credits in the current period.
type Balance struct {
	Kind  BalanceKind `json:"kind"`
	State string      `json:"state"`
	// Why is set while waiting: WhyUsedUp, WhyLimit or WhyNoPlan.
	Why string `json:"why,omitempty"`
	// MeterPct is how much is used, 0 to 100: on Solo of the included and
	// pack credits, on Company of the included credits until they're used,
	// then of the spend limit. Nil where nothing bounds it.
	MeterPct *int `json:"meter_pct"`
	// Included are the period's included credits; Used its counted ones.
	Included int64 `json:"included"`
	Used     int64 `json:"used"`
	// Left is what's left to use: on Solo the included and pack credits,
	// on Company the included ones. Nil where nothing bounds it.
	Left *int64 `json:"left"`
	// PacksLeft and Over are Solo's: the packs' credits left now, and the
	// period's free overshoot. PacksDrawn are the pack credits drawn for the
	// period's runs (for AIOf; not shown).
	PacksLeft  int64 `json:"packs_left"`
	Over       int64 `json:"over"`
	PacksDrawn int64 `json:"-"`
	// Beyond is Company's: credits used past the included ones, and what
	// they cost (BeyondCents), against LimitCents.
	Beyond      int64 `json:"beyond"`
	BeyondCents int64 `json:"beyond_cents"`
	LimitCents  int64 `json:"limit_cents"`
	Seats       int   `json:"seats"`
}

// Waiting reports that no new run starts.
func (b Balance) Waiting() bool { return b.State == StateWaiting }

// ComputeBalance works out the balance from in.
func ComputeBalance(in BalanceIn) Balance {
	b := Balance{Kind: in.Kind, State: StateOK, Used: max(in.Used, 0), Seats: in.Seats}
	switch in.Kind {
	case BalanceUnbounded:
	case BalanceSolo:
		b.Included = SoloIncludedCredits
		b.PacksLeft, b.Over, b.PacksDrawn = max(in.PacksLeft, 0), max(in.Over, 0), max(in.PacksDrawn, 0)
		// What the period has had or has, and what of it the runs took:
		// a run's credits past the balance are the free overshoot, which
		// nothing pays for, so they don't take a pack bought later.
		has := b.Included + b.PacksDrawn + b.PacksLeft
		took := b.Used - b.Over
		left := max(has-took, 0)
		b.Left = &left
		b.setMeter(took, has, WhyUsedUp)
	case BalanceCompany:
		b.Included = CompanyCreditsPerSeat * int64(max(in.Seats, 1))
		b.LimitCents = in.LimitCents
		if b.LimitCents <= 0 {
			b.LimitCents = SpendLimitStepCents
		}
		left := max(b.Included-b.Used, 0)
		b.Left = &left
		b.Beyond = max(b.Used-b.Included, 0)
		b.BeyondCents = b.Beyond * CentsPerCredit
		if b.Beyond == 0 {
			// The included credits: a bar, never a warning.
			pct := percent(b.Used, b.Included)
			b.MeterPct = &pct
			break
		}
		b.setMeter(b.BeyondCents, b.LimitCents, WhyLimit)
	default:
		b.Kind = BalanceNone
		b.State, b.Why = StateWaiting, WhyNoPlan
	}
	return b
}

// setMeter sets the bar at used of has, and the state: waiting, for why,
// once it's all used; a warning from WarnPct.
func (b *Balance) setMeter(used, has int64, why string) {
	pct := percent(used, has)
	b.MeterPct = &pct
	switch {
	case used >= has:
		b.State, b.Why = StateWaiting, why
	case pct >= WarnPct:
		b.State = StateWarning
	}
}

// percent is used of has, 0 to 100, rounded down, so 79.9% isn't 80.
func percent(used, has int64) int {
	switch {
	case has <= 0, used >= has:
		return 100
	case used <= 0:
		return 0
	}
	return int(used * 100 / has)
}

// Allow is whether a new run may start on b, and why not. A taf's run
// waits with the balance. Setup's (Coach's) doesn't while a plan runs and
// Setup is within its fair use (setupSpent, the period's), and on an account
// with no plan it may start until Setup's runs there have cost
// NoPlanSetupMicroUSD (noPlanSpent, all of them).
func Allow(b Balance, setup bool, setupSpent, noPlanSpent int64) (bool, string) {
	if setup {
		if b.Kind == BalanceNone {
			if noPlanSpent < NoPlanSetupMicroUSD {
				return true, ""
			}
			return false, WhyNoPlan
		}
		if setupSpent < SetupAllowanceMicroUSD {
			return true, ""
		}
	}
	if b.Waiting() {
		return false, b.Why
	}
	return true, ""
}

// KindOf is how t's plan bounds its credits, and its seats: Solo, Team
// (Company, by seat), an agreement (KIFF's company) or internal (no
// limit), or no plan, which the trial and an unknown plan read as.
func KindOf(t tenant.Tenant) (BalanceKind, int) {
	l := tenant.PlanFor(t)
	switch l.Canonical {
	case tenant.PlanSolo:
		return BalanceSolo, 1
	case tenant.PlanTeam:
		return BalanceCompany, l.Seats
	case tenant.PlanCompany, tenant.PlanInternal:
		return BalanceUnbounded, 0
	}
	return BalanceNone, 0
}
