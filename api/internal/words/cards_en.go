package words

import (
	"strconv"
	"strings"
)

// English Card lines: word for word as KIFF wrote them before they were
// translated.
func init() {
	en.Cards = Cards{
		Money: MoneyStyle{Thousands: ",", CurrencyGap: ""}, NoCurrency: "(no currency)",

		UpToEach:      func(amount string) string { return "up to " + amount + " each" },
		MoreAsksFirst: "; more asks you first",
		ThenAsks:      ", then it asks you",
		EachCallAsks:  "Each call asks you first",
		CallCount: func(n int64) string {
			if n == 1 {
				return "1 call"
			}
			return strconv.FormatInt(n, 10) + " calls"
		},
		// A rolling window says it rolls; it never reads as a day that
		// resets at midnight.
		WindowAmount: map[string]string{"calendar_day": "in total a day", "rolling_24h": "in any 24 hours",
			"rolling_1h": "in any hour", "rolling_7d": "in any 7 days", "lifetime": "in total"},
		WindowCount: map[string]string{"calendar_day": "a day", "rolling_24h": "in any 24 hours",
			"rolling_1h": "in any hour", "rolling_7d": "in any 7 days", "lifetime": "in total"},
		ForEach: func(item string) string { return "for each " + item },

		Ordinal: func(n int64) string {
			s := strconv.FormatInt(n, 10)
			switch {
			case n%100 >= 11 && n%100 <= 13:
				return s + "th"
			case n%10 == 1:
				return s + "st"
			case n%10 == 2:
				return s + "nd"
			case n%10 == 3:
				return s + "rd"
			}
			return s + "th"
		},
		WouldBe: func(limit, ordinal string) string { return limit + ": this would be the " + ordinal },
		WouldBeFor: func(limit, ordinal, value string) string {
			return limit + ": this would be the " + ordinal + " for " + value
		},
		OverCalls: func(limit int64, ordinal string) string {
			return "Over its limit of " + strconv.FormatInt(limit, 10) + " calls: this would be the " + ordinal
		},
		OverForOneCall:  func(amount, limit string) string { return amount + " is over the " + limit + " limit for one call" },
		OverTheTotal:    func(amount, limit string) string { return amount + " would go over the " + limit + " total" },
		OutsideCard:     "Outside the Card",
		OverActionLimit: "Over the Card's limit for one action",
		OverCardTotal:   "Over the Card's total",
		NotCardResource: "Not one of the Card's resources",

		PendingCalls: func(calls int) string {
			if calls == 1 {
				return "1 of them is a call the tool refused. It counts until KIFF can confirm nothing happened."
			}
			return strconv.Itoa(calls) + " of them are calls the tool refused. They count until KIFF can confirm nothing happened."
		},
		PendingNoUnit: func(calls int) string {
			return "Part of it is from " + enCalls(calls) + " the tool refused. It counts until KIFF can confirm nothing moved."
		},
		PendingAmount: func(amount string, calls int) string {
			return amount + " of it is from " + enCalls(calls) + " the tool refused. It counts until KIFF can confirm nothing moved."
		},

		CardOf: func(label string) string {
			if strings.HasSuffix(label, "s") {
				return label + "' Card" // "Stripe refunds' Card"
			}
			return label + "'s Card"
		},
		TafCard: "The taf's Card",
	}
}

func enCalls(n int) string {
	if n == 1 {
		return "1 call"
	}
	return strconv.Itoa(n) + " calls"
}
