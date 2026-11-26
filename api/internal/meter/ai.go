package meter

// MicroUSDPerCredit is the AI cost a credit pays for, in millionths of a US
// dollar: the inverse of Credits' rate (a euro cent is CreditMultiplier
// credits, a euro is 1.08 dollars), so 2,700, or $0.0027.
const MicroUSDPerCredit = 1_000_000 * usdPerEURHundredths / (100 * 100 * CreditMultiplier)

// AI is an account's plan as the allowance answer says it ("ai"), for the
// agents runtime's share of the AI provider's day: the plan's kind, and the
// most AI its period can pay for. The share is only a bound against a
// runaway; KIFF's balance stays the gate.
type AI struct {
	// Kind is the balance's: solo, company, unbounded or none.
	Kind BalanceKind `json:"kind"`
	// PeriodMicroUSD is, on Solo and Company, everything the period's
	// balance can pay for, at MicroUSDPerCredit, plus Setup's (Coach's) fair
	// use: on Solo the included credits and the packs' (drawn this period,
	// and left now); on Company each seat's included credits and the spend
	// limit beyond them. Solo's free overshoot isn't counted. Nil where
	// nothing bounds the plan (unbounded) or there's no plan (none).
	PeriodMicroUSD *int64 `json:"period_micro_usd"`
}

// AIOf is b's plan as the allowance answer says it.
func AIOf(b Balance) AI {
	a := AI{Kind: b.Kind}
	var credits int64
	switch b.Kind {
	case BalanceSolo:
		credits = b.Included + b.PacksDrawn + b.PacksLeft
	case BalanceCompany:
		credits = b.Included + b.LimitCents/CentsPerCredit
	default:
		if b.Kind != BalanceUnbounded {
			a.Kind = BalanceNone
		}
		return a
	}
	n := credits*MicroUSDPerCredit + SetupAllowanceMicroUSD
	a.PeriodMicroUSD = &n
	return a
}
