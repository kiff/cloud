package words

import "strconv"

// European Portuguese Card lines (formal, as its other lines). The call
// is feminine: "a 21.ª". Thousands and the euro take a non-breaking space.
func init() {
	pt.Cards = Cards{
		Money: MoneyStyle{Thousands: " ", CurrencyAfter: true, CurrencyGap: " "}, NoCurrency: "(sem moeda)",

		UpToEach:      func(amount string) string { return "até " + amount + " por chamada" },
		MoreAsksFirst: "; acima disso, pergunta-lhe primeiro",
		ThenAsks:      ", depois pergunta-lhe",
		EachCallAsks:  "Cada chamada pergunta-lhe primeiro",
		CallCount: func(n int64) string {
			if n == 1 {
				return "1 chamada"
			}
			return strconv.FormatInt(n, 10) + " chamadas"
		},
		WindowAmount: map[string]string{"calendar_day": "no total por dia", "rolling_24h": "em qualquer período de 24 horas",
			"rolling_1h": "em qualquer período de uma hora", "rolling_7d": "em qualquer período de 7 dias", "lifetime": "no total"},
		WindowCount: map[string]string{"calendar_day": "por dia", "rolling_24h": "em qualquer período de 24 horas",
			"rolling_1h": "em qualquer período de uma hora", "rolling_7d": "em qualquer período de 7 dias", "lifetime": "no total"},
		ForEach: func(item string) string { return "por cada " + item },

		Ordinal: func(n int64) string { return strconv.FormatInt(n, 10) + ".ª" },
		WouldBe: func(limit, ordinal string) string { return limit + ": esta seria a " + ordinal },
		WouldBeFor: func(limit, ordinal, value string) string {
			return limit + ": esta seria a " + ordinal + " para " + value
		},
		OverCalls: func(limit int64, ordinal string) string {
			return "Acima do seu limite de " + strconv.FormatInt(limit, 10) + " chamadas: esta seria a " + ordinal
		},
		OverForOneCall:  func(amount, limit string) string { return amount + " ultrapassa o limite de " + limit + " por chamada" },
		OverTheTotal:    func(amount, limit string) string { return amount + " ultrapassaria o total de " + limit },
		OutsideCard:     "Fora da Card",
		OverActionLimit: "Acima do limite da Card para uma ação",
		OverCardTotal:   "Acima do total da Card",
		NotCardResource: "Não é um dos recursos da Card",

		PendingCalls: func(calls int) string {
			if calls == 1 {
				return "1 delas é uma chamada que a ferramenta recusou. Conta até o KIFF poder confirmar que nada aconteceu."
			}
			return strconv.Itoa(calls) + " delas são chamadas que a ferramenta recusou. Contam até o KIFF poder confirmar que nada aconteceu."
		},
		PendingNoUnit: func(calls int) string {
			return "Uma parte vem de " + ptRefused(calls) + ". Conta até o KIFF poder confirmar que nada se moveu."
		},
		PendingAmount: func(amount string, calls int) string {
			return amount + " vêm de " + ptRefused(calls) + ". Conta até o KIFF poder confirmar que nada se moveu."
		},

		CardOf:  func(label string) string { return "Card de " + label },
		TafCard: "A Card do taf",
	}
}

func ptRefused(n int) string {
	if n == 1 {
		return "1 chamada que a ferramenta recusou"
	}
	return strconv.Itoa(n) + " chamadas que a ferramenta recusou"
}
