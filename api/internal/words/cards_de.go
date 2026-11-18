package words

import "strconv"

// German Card lines (du).
func init() {
	de.Cards = Cards{
		Money: MoneyStyle{Thousands: ".", CurrencyAfter: true, CurrencyGap: "\u00a0"}, NoCurrency: "(keine Währung)",

		UpToEach:      func(amount string) string { return "bis zu " + amount + " pro Aufruf" },
		MoreAsksFirst: "; bei mehr wirst du zuerst gefragt",
		ThenAsks:      ", danach wirst du gefragt",
		EachCallAsks:  "Bei jedem Aufruf wirst du zuerst gefragt",
		CallCount: func(n int64) string {
			if n == 1 {
				return "1 Aufruf"
			}
			return strconv.FormatInt(n, 10) + " Aufrufe"
		},
		WindowAmount: map[string]string{"calendar_day": "insgesamt pro Tag", "rolling_24h": "in jedem Zeitraum von 24 Stunden",
			"rolling_1h": "in jedem Zeitraum von einer Stunde", "rolling_7d": "in jedem Zeitraum von 7 Tagen", "lifetime": "insgesamt"},
		WindowCount: map[string]string{"calendar_day": "pro Tag", "rolling_24h": "in jedem Zeitraum von 24 Stunden",
			"rolling_1h": "in jedem Zeitraum von einer Stunde", "rolling_7d": "in jedem Zeitraum von 7 Tagen", "lifetime": "insgesamt"},
		ForEach: func(item string) string { return "je " + item },

		Ordinal: func(n int64) string { return strconv.FormatInt(n, 10) + "." },
		WouldBe: func(limit, ordinal string) string { return limit + ": das wäre der " + ordinal },
		WouldBeFor: func(limit, ordinal, value string) string {
			return limit + ": das wäre der " + ordinal + " für " + value
		},
		OverCalls: func(limit int64, ordinal string) string {
			return "Über dem Limit von " + strconv.FormatInt(limit, 10) + " Aufrufen: das wäre der " + ordinal
		},
		OverForOneCall: func(amount, limit string) string {
			return amount + " liegt über dem Limit von " + limit + " pro Aufruf"
		},
		OverTheTotal: func(amount, limit string) string {
			return amount + " würde die Summe von " + limit + " überschreiten"
		},
		OutsideCard:     "Außerhalb der Card",
		OverActionLimit: "Über dem Limit der Card für eine Aktion",
		OverCardTotal:   "Über der Summe der Card",
		NotCardResource: "Keine der Ressourcen der Card",

		PendingCalls: func(calls int) string {
			if calls == 1 {
				return "1 davon ist ein Aufruf, den das Tool abgelehnt hat. Er zählt, bis KIFF bestätigen kann, dass nichts passiert ist."
			}
			return strconv.Itoa(calls) + " davon sind Aufrufe, die das Tool abgelehnt hat. Sie zählen, bis KIFF bestätigen kann, dass nichts passiert ist."
		},
		PendingNoUnit: func(calls int) string {
			return "Ein Teil davon stammt aus " + deRefused(calls) + ". Er zählt, bis KIFF bestätigen kann, dass nichts bewegt wurde."
		},
		PendingAmount: func(amount string, calls int) string {
			return amount + " davon stammen aus " + deRefused(calls) + ". Das zählt, bis KIFF bestätigen kann, dass nichts bewegt wurde."
		},

		CardOf:  func(label string) string { return "Card von " + label },
		TafCard: "Die Card des tafs",
	}
}

// deRefused is "1 Aufruf, den das Tool abgelehnt hat", in the dative after "aus".
func deRefused(n int) string {
	if n == 1 {
		return "1 Aufruf, den das Tool abgelehnt hat"
	}
	return strconv.Itoa(n) + " Aufrufen, die das Tool abgelehnt hat"
}
