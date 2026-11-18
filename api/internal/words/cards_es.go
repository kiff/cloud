package words

import "strconv"

// Spanish Card lines (tú). The call is feminine, as in its other lines.
func init() {
	es.Cards = Cards{
		Money: MoneyStyle{Thousands: ".", CurrencyAfter: true, CurrencyGap: "\u00a0"}, NoCurrency: "(sin moneda)",

		UpToEach:      func(amount string) string { return "hasta " + amount + " por llamada" },
		MoreAsksFirst: "; para más, te pregunta antes",
		ThenAsks:      ", después te pregunta",
		EachCallAsks:  "Cada llamada te pregunta antes",
		CallCount: func(n int64) string {
			if n == 1 {
				return "1 llamada"
			}
			return strconv.FormatInt(n, 10) + " llamadas"
		},
		WindowAmount: map[string]string{"calendar_day": "en total al día", "rolling_24h": "en cualquier periodo de 24 horas",
			"rolling_1h": "en cualquier periodo de una hora", "rolling_7d": "en cualquier periodo de 7 días", "lifetime": "en total"},
		WindowCount: map[string]string{"calendar_day": "al día", "rolling_24h": "en cualquier periodo de 24 horas",
			"rolling_1h": "en cualquier periodo de una hora", "rolling_7d": "en cualquier periodo de 7 días", "lifetime": "en total"},
		ForEach: func(item string) string { return "por cada " + item },

		Ordinal: func(n int64) string { return strconv.FormatInt(n, 10) + ".ª" },
		WouldBe: func(limit, ordinal string) string { return limit + ": esta sería la " + ordinal },
		WouldBeFor: func(limit, ordinal, value string) string {
			return limit + ": esta sería la " + ordinal + " para " + value
		},
		OverCalls: func(limit int64, ordinal string) string {
			return "Por encima de su límite de " + strconv.FormatInt(limit, 10) + " llamadas: esta sería la " + ordinal
		},
		OverForOneCall:  func(amount, limit string) string { return amount + " supera el límite de " + limit + " por llamada" },
		OverTheTotal:    func(amount, limit string) string { return amount + " superaría el total de " + limit },
		OutsideCard:     "Fuera de la Card",
		OverActionLimit: "Por encima del límite de la Card para una acción",
		OverCardTotal:   "Por encima del total de la Card",
		NotCardResource: "No es uno de los recursos de la Card",

		PendingCalls: func(calls int) string {
			if calls == 1 {
				return "1 de ellas es una llamada que la herramienta rechazó. Cuenta hasta que KIFF pueda confirmar que no pasó nada."
			}
			return strconv.Itoa(calls) + " de ellas son llamadas que la herramienta rechazó. Cuentan hasta que KIFF pueda confirmar que no pasó nada."
		},
		PendingNoUnit: func(calls int) string {
			return "Una parte viene de " + esRefused(calls) + ". Cuenta hasta que KIFF pueda confirmar que no se movió nada."
		},
		PendingAmount: func(amount string, calls int) string {
			return amount + " de ello viene de " + esRefused(calls) + ". Cuenta hasta que KIFF pueda confirmar que no se movió nada."
		},

		CardOf:  func(label string) string { return "Card de " + label },
		TafCard: "La Card del taf",
	}
}

func esRefused(n int) string {
	if n == 1 {
		return "1 llamada que la herramienta rechazó"
	}
	return strconv.Itoa(n) + " llamadas que la herramienta rechazó"
}
