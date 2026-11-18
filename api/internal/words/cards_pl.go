package words

import "strconv"

// Polish Card lines (ty). Counts take Polish's plural forms; thousands and
// the euro take a non-breaking space: "1 250,50 €".
func init() {
	pl.Cards = Cards{
		Money: MoneyStyle{Thousands: " ", CurrencyAfter: true, CurrencyGap: " "}, NoCurrency: "(bez waluty)",

		UpToEach:      func(amount string) string { return "do " + amount + " na wywołanie" },
		MoreAsksFirst: "; powyżej tego najpierw Cię zapyta",
		ThenAsks:      ", potem Cię zapyta",
		EachCallAsks:  "Każde wywołanie wymaga najpierw Twojej zgody",
		CallCount:     func(n int64) string { return strconv.FormatInt(n, 10) + " " + plCalls(n) },
		WindowAmount: map[string]string{"calendar_day": "łącznie dziennie", "rolling_24h": "w dowolnych 24 godzinach",
			"rolling_1h": "w dowolnej godzinie", "rolling_7d": "w dowolnych 7 dniach", "lifetime": "łącznie"},
		WindowCount: map[string]string{"calendar_day": "dziennie", "rolling_24h": "w dowolnych 24 godzinach",
			"rolling_1h": "w dowolnej godzinie", "rolling_7d": "w dowolnych 7 dniach", "lifetime": "łącznie"},
		ForEach: func(item string) string { return "dla każdej wartości " + item },

		Ordinal: func(n int64) string { return strconv.FormatInt(n, 10) + "." },
		WouldBe: func(limit, ordinal string) string { return limit + ": to byłoby " + ordinal + " wywołanie" },
		WouldBeFor: func(limit, ordinal, value string) string {
			return limit + ": to byłoby " + ordinal + " wywołanie dla " + value
		},
		OverCalls: func(limit int64, ordinal string) string {
			of := "wywołań"
			if limit == 1 {
				of = "wywołania"
			}
			return "Ponad limit " + strconv.FormatInt(limit, 10) + " " + of + ": to byłoby " + ordinal + " wywołanie"
		},
		OverForOneCall: func(amount, limit string) string {
			return amount + " przekracza limit " + limit + " na jedno wywołanie"
		},
		OverTheTotal:    func(amount, limit string) string { return amount + " przekroczyłoby sumę " + limit },
		OutsideCard:     "Poza Card",
		OverActionLimit: "Ponad limit Card dla jednej akcji",
		OverCardTotal:   "Ponad sumę Card",
		NotCardResource: "Nie jest jednym z zasobów Card",

		PendingCalls: func(calls int) string {
			if calls == 1 {
				return "1 z nich to wywołanie, które narzędzie odrzuciło. Liczy się, dopóki KIFF nie potwierdzi, że nic się nie stało."
			}
			return strconv.Itoa(calls) + " z nich to " + plCalls(int64(calls)) + ", które narzędzie odrzuciło. Liczą się, dopóki KIFF nie potwierdzi, że nic się nie stało."
		},
		PendingNoUnit: func(calls int) string {
			return "Część pochodzi z " + plRefused(calls) + ". Liczy się, dopóki KIFF nie potwierdzi, że nic się nie zmieniło."
		},
		PendingAmount: func(amount string, calls int) string {
			return amount + " z tego pochodzi z " + plRefused(calls) + ". Liczy się, dopóki KIFF nie potwierdzi, że nic się nie zmieniło."
		},

		CardOf:  func(label string) string { return "Card – " + label },
		TafCard: "Card tafa",
	}
}

// plCalls is "wywołanie" in the form n takes: 1 wywołanie, 2 wywołania,
// 5 wywołań.
func plCalls(n int64) string {
	switch {
	case n == 1:
		return "wywołanie"
	case plFew(int(n % 1000)):
		return "wywołania"
	}
	return "wywołań"
}

// plRefused is the refused calls after "z", in the genitive: "z 1
// wywołania", "z 3 wywołań".
func plRefused(n int) string {
	if n == 1 {
		return "1 wywołania, które narzędzie odrzuciło"
	}
	return strconv.Itoa(n) + " wywołań, które narzędzie odrzuciło"
}
