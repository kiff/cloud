package words

import "strconv"

// Dutch Card lines (je). The euro goes before the amount: "€ 1.250,50".
func init() {
	nl.Cards = Cards{
		Money: MoneyStyle{Thousands: ".", CurrencyGap: "\u00a0"}, NoCurrency: "(geen valuta)",

		UpToEach:      func(amount string) string { return "tot " + amount + " per aanroep" },
		MoreAsksFirst: "; voor meer vraagt het je eerst",
		ThenAsks:      ", daarna vraagt het je",
		EachCallAsks:  "Elke aanroep vraagt je eerst",
		CallCount: func(n int64) string {
			if n == 1 {
				return "1 aanroep"
			}
			return strconv.FormatInt(n, 10) + " aanroepen"
		},
		WindowAmount: map[string]string{"calendar_day": "in totaal per dag", "rolling_24h": "in elke periode van 24 uur",
			"rolling_1h": "in elke periode van een uur", "rolling_7d": "in elke periode van 7 dagen", "lifetime": "in totaal"},
		WindowCount: map[string]string{"calendar_day": "per dag", "rolling_24h": "in elke periode van 24 uur",
			"rolling_1h": "in elke periode van een uur", "rolling_7d": "in elke periode van 7 dagen", "lifetime": "in totaal"},
		ForEach: func(item string) string { return "per " + item },

		Ordinal: func(n int64) string { return strconv.FormatInt(n, 10) + "e" },
		WouldBe: func(limit, ordinal string) string { return limit + ": dit zou de " + ordinal + " zijn" },
		WouldBeFor: func(limit, ordinal, value string) string {
			return limit + ": dit zou de " + ordinal + " voor " + value + " zijn"
		},
		OverCalls: func(limit int64, ordinal string) string {
			return "Boven de limiet van " + strconv.FormatInt(limit, 10) + " aanroepen: dit zou de " + ordinal + " zijn"
		},
		OverForOneCall: func(amount, limit string) string {
			return amount + " ligt boven de limiet van " + limit + " per aanroep"
		},
		OverTheTotal:    func(amount, limit string) string { return amount + " zou boven het totaal van " + limit + " gaan" },
		OutsideCard:     "Buiten de Card",
		OverActionLimit: "Boven de limiet van de Card voor één actie",
		OverCardTotal:   "Boven het totaal van de Card",
		NotCardResource: "Niet een van de bronnen van de Card",

		PendingCalls: func(calls int) string {
			if calls == 1 {
				return "1 daarvan is een aanroep die de tool weigerde. Die telt mee tot KIFF kan bevestigen dat er niets is gebeurd."
			}
			return strconv.Itoa(calls) + " daarvan zijn aanroepen die de tool weigerde. Die tellen mee tot KIFF kan bevestigen dat er niets is gebeurd."
		},
		PendingNoUnit: func(calls int) string {
			return "Een deel komt van " + nlRefused(calls) + ". Dat telt mee tot KIFF kan bevestigen dat er niets is verplaatst."
		},
		PendingAmount: func(amount string, calls int) string {
			return amount + " daarvan komt van " + nlRefused(calls) + ". Dat telt mee tot KIFF kan bevestigen dat er niets is verplaatst."
		},

		CardOf:  func(label string) string { return "Card van " + label },
		TafCard: "De Card van de taf",
	}
}

func nlRefused(n int) string {
	if n == 1 {
		return "1 aanroep die de tool weigerde"
	}
	return strconv.Itoa(n) + " aanroepen die de tool weigerde"
}
