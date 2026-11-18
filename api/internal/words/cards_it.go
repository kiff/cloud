package words

import "strconv"

// Italian Card lines (tu). The call is feminine: "la 21ª".
func init() {
	it.Cards = Cards{
		Money: MoneyStyle{Thousands: ".", CurrencyAfter: true, CurrencyGap: "\u00a0"}, NoCurrency: "(senza valuta)",

		UpToEach:      func(amount string) string { return "fino a " + amount + " per chiamata" },
		MoreAsksFirst: "; oltre, ti chiede prima",
		ThenAsks:      ", poi ti chiede",
		EachCallAsks:  "Ogni chiamata ti chiede prima",
		CallCount: func(n int64) string {
			if n == 1 {
				return "1 chiamata"
			}
			return strconv.FormatInt(n, 10) + " chiamate"
		},
		WindowAmount: map[string]string{"calendar_day": "in totale al giorno", "rolling_24h": "in qualsiasi arco di 24 ore",
			"rolling_1h": "in qualsiasi arco di un'ora", "rolling_7d": "in qualsiasi arco di 7 giorni", "lifetime": "in totale"},
		WindowCount: map[string]string{"calendar_day": "al giorno", "rolling_24h": "in qualsiasi arco di 24 ore",
			"rolling_1h": "in qualsiasi arco di un'ora", "rolling_7d": "in qualsiasi arco di 7 giorni", "lifetime": "in totale"},
		ForEach: func(item string) string { return "per ogni " + item },

		Ordinal: func(n int64) string { return strconv.FormatInt(n, 10) + "ª" },
		WouldBe: func(limit, ordinal string) string { return limit + ": questa sarebbe la " + ordinal },
		WouldBeFor: func(limit, ordinal, value string) string {
			return limit + ": questa sarebbe la " + ordinal + " per " + value
		},
		OverCalls: func(limit int64, ordinal string) string {
			return "Oltre il suo limite di " + strconv.FormatInt(limit, 10) + " chiamate: questa sarebbe la " + ordinal
		},
		OverForOneCall:  func(amount, limit string) string { return amount + " supera il limite di " + limit + " per chiamata" },
		OverTheTotal:    func(amount, limit string) string { return amount + " supererebbe il totale di " + limit },
		OutsideCard:     "Fuori dalla Card",
		OverActionLimit: "Oltre il limite della Card per un'azione",
		OverCardTotal:   "Oltre il totale della Card",
		NotCardResource: "Non è una delle risorse della Card",

		PendingCalls: func(calls int) string {
			if calls == 1 {
				return "1 di esse è una chiamata che lo strumento ha rifiutato. Conta finché KIFF non può confermare che non è successo nulla."
			}
			return strconv.Itoa(calls) + " di esse sono chiamate che lo strumento ha rifiutato. Contano finché KIFF non può confermare che non è successo nulla."
		},
		PendingNoUnit: func(calls int) string {
			return "Una parte viene da " + itRefused(calls) + ". Conta finché KIFF non può confermare che non si è mosso nulla."
		},
		PendingAmount: func(amount string, calls int) string {
			return amount + " ne vengono da " + itRefused(calls) + ". Conta finché KIFF non può confermare che non si è mosso nulla."
		},

		CardOf:  func(label string) string { return "Card di " + label },
		TafCard: "La Card del taf",
	}
}

func itRefused(n int) string {
	if n == 1 {
		return "1 chiamata che lo strumento ha rifiutato"
	}
	return strconv.Itoa(n) + " chiamate che lo strumento ha rifiutato"
}
