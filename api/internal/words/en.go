package words

import "fmt"

// English: the source of every line.
var en = Words{
	WindowNextApproval:     "Your fingerprint or face at the next approval",
	WindowAgainUnderMinute: "Your fingerprint or face again in under a minute",
	WindowNoneNextMinute:   "No fingerprint or face for the next minute",
	WindowNoneMinutes:      func(n int) string { return fmt.Sprintf("No fingerprint or face for the next %d minutes", n) },

	Today:     "today",
	Yesterday: "yesterday",
	Months:    [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},

	HappenedYes:           "Yes, it went through",
	HappenedYesDetail:     "It stays counted on the Card.",
	HappenedNo:            "No, it didn't",
	HappenedNoDetail:      "The Card gets its room back. Nothing is reversed at the vendor.",
	HappenedNotSure:       "Not sure, help me check",
	HappenedNotSureDetail: "Coach reads the vendor's records with you. The ask stays with you for a day.",
	HappenedNoAfter:       "The Card has its room back. Nothing was reversed at the vendor: check there whether the payment went through after all, and if it did, tell Coach.",
	WhyLost: func(vendor string, read bool) string {
		if vendor == "" {
			vendor = "the vendor"
		}
		if read {
			return "It was sent, but its answer was lost, and KIFF's reads of " + vendor + "'s records haven't settled it."
		}
		return "It was sent, but its answer was lost, and " + vendor + "'s records can't be searched for this call."
	},
	WhyRefused: func(vendor string, read bool) string {
		if vendor == "" {
			vendor = "the vendor"
		}
		if read {
			return Capitalize(vendor) + " refused it without saying whether anything was taken, and KIFF's reads of its records haven't settled it."
		}
		return Capitalize(vendor) + " refused it without saying whether anything was taken, and its records can't be searched for this call."
	},
	WhyProbation: "One of the first calls of a connection KIFF hasn't tried with a test account: confirm what happened.",
	StatusRefused: func(vendor string) string {
		if vendor == "" {
			return "The app refused it. Check with the app whether any money moved."
		}
		return Capitalize(vendor) + " refused it. Check in " + vendor + " whether any money moved."
	},
	StatusLost: func(vendor string) string {
		if vendor == "" {
			return "It was sent, but its answer was lost. Check with the app whether it happened."
		}
		return "It was sent to " + vendor + ", but its answer was lost. Check in " + vendor + " whether it happened."
	},
	StatusProbation: "One of the first calls through this connection: check that it did what you expected.",
	CallAtVendor:    func(action, vendor string) string { return action + " at " + vendor },
	CallByTaf:       func(taf string) string { return "by " + taf },

	Reasons: map[string]string{
		"mandate_owner_rejected":       "You refused it.",
		"mandate_owner_expired":        "Nobody answered it in time.",
		"mandate_withdrawn":            "It was withdrawn before anyone answered.",
		"mandate_per_action_exceeded":  "It was over the Card's limit for one call.",
		"mandate_limit_reached":        "It was over one of the Card's totals.",
		"mandate_resource_not_on_card": "It acted on something the Card doesn't cover.",
		"mandate_unreadable_resource":  "KIFF couldn't read what it acted on.",
		"mandate_not_covered":          "No Card covers this tool.",
		"mandate_expired":              "Its Card has expired.",
		"no_mandate_issued":            "No Card covers this tool.",
		"amount_unit_mismatch":         "It was in another currency than the Card's.",
		"amount_unit_unverified":       "KIFF couldn't check its currency.",
		"amount_unverified":            "KIFF couldn't read its amount.",
		"halted":                       "You had halted it.",
		"halt_unavailable":             "KIFF couldn't check whether it was halted.",
	},
	OwnerAsks: map[string]string{
		OwnerAskSealedMoney:  "this tool moves money on a connection whose key KIFF holds, so every call waits for approval",
		OwnerAskSecondRefund: "this charge already has a refund, so a second one waits for approval",
		OwnerAskNoRequestID:  "the agent named no request from a connected app, so this refund waits for approval",
		OwnerAskOther:        "every call of this tool waits for approval",
		OwnerAskEachCall:     "every call of this tool waits for approval",
	},
	ResourceMissing: func(param string) string {
		return fmt.Sprintf("the call names no %s, and this Card only applies to the %s it lists", param, param)
	},
	ResourceNotOnCard: func(param, value string) string { return fmt.Sprintf("%s %q isn't one this Card covers", param, value) },
	OverPerCall: func(draw, limit string) string {
		return fmt.Sprintf("%s is above the %s limit for one call", draw, limit)
	},
	OverTotal: func(draw, limit, used string) string {
		return fmt.Sprintf("%s would go over the %s limit: %s is used", draw, limit, used)
	},

	// The held-call and Card-warning emails, and a warning's reason.
	HeldSubject:     "Your agent is waiting for your answer",
	HeldNothingDone: "Nothing has been done.",
	HeldOpen:        "Open it in KIFF to answer (you will be asked to sign in):",
	HeldOptions:     "You can approve it once, change the Card to allow it, or reject it.",
	HeldIntro: func(agent, action, amount string) string {
		return agent + " wants to " + action + amount + ", which is outside its Card."
	},
	HeldSteps: func(steps string) string {
		return "The Card also lets you reject it with a next step for the agent: " + steps + "."
	},
	HeldExpires: func(when string) string {
		return "If nobody answers by " + when + ", it is refused and the agent has to ask again."
	},
	HeldNoApprove:   "Nothing can be approved from this email.",
	HeldButtonsNote: "Each button opens this action in KIFF, where you sign in and confirm your answer.",
	HeldApproveOnce: "Approve once",
	HeldChangeCard:  "Change the Card to allow it",
	HeldReject:      "Reject",
	HeldWants: func(agent string) string {
		return fmt.Sprintf("%s wants to do something outside its Card:", agent)
	},
	HeldRowAction: "Action", HeldRowAmount: "Amount", HeldRowWhy: "Why it's held",
	WarnSpent:        "This Card total is spent.",
	WarnBurst:        "A new Card used more than its burst threshold at the start of this window.",
	WarnRunway:       "At the recent rate, this Card total is approaching exhaustion.",
	WarnUnusual:      func(ratio string) string { return "Recent usage is " + ratio + "x its equivalent seven-day average." },
	WarnUsed:         func(pct, period string) string { return pct + "% of this total is used " + period + "." },
	Periods:          map[string]string{"calendar_day": "today", "rolling_1h": "over the last hour", "rolling_24h": "over the last 24 hours", "rolling_7d": "over the last 7 days", "lifetime": "so far"},
	WarnNeverResets:  "This total never resets. Add room, change the Card, or pause its agent.",
	WarnReviewWindow: "Review this window’s room, change the Card, or pause its agent.",
	WarnSubject:      "KIFF Card budget warning",
	WarnReview:       "Review the Card",
}
