package words

import "strconv"

// French Card lines (vous). Thousands and the euro take a non-breaking
// space: "1 250,50 €".
func init() {
	fr.Cards = Cards{
		Money: MoneyStyle{Thousands: " ", CurrencyAfter: true, CurrencyGap: " "}, NoCurrency: "(sans devise)",

		UpToEach:      func(amount string) string { return "jusqu'à " + amount + " par appel" },
		MoreAsksFirst: " ; au-delà, votre accord est demandé d'abord",
		ThenAsks:      ", puis votre accord est demandé",
		EachCallAsks:  "Chaque appel attend d'abord votre accord",
		CallCount: func(n int64) string {
			if n == 1 {
				return "1 appel"
			}
			return strconv.FormatInt(n, 10) + " appels"
		},
		WindowAmount: map[string]string{"calendar_day": "au total par jour", "rolling_24h": "sur 24 heures glissantes",
			"rolling_1h": "sur une heure glissante", "rolling_7d": "sur 7 jours glissants", "lifetime": "au total"},
		WindowCount: map[string]string{"calendar_day": "par jour", "rolling_24h": "sur 24 heures glissantes",
			"rolling_1h": "sur une heure glissante", "rolling_7d": "sur 7 jours glissants", "lifetime": "au total"},
		ForEach: func(item string) string { return "pour chaque " + item },

		Ordinal: func(n int64) string {
			if n == 1 {
				return "1er"
			}
			return strconv.FormatInt(n, 10) + "e"
		},
		WouldBe: func(limit, ordinal string) string { return limit + " : ce serait le " + ordinal },
		WouldBeFor: func(limit, ordinal, value string) string {
			return limit + " : ce serait le " + ordinal + " pour " + value
		},
		OverCalls: func(limit int64, ordinal string) string {
			return "Au-delà de sa limite de " + strconv.FormatInt(limit, 10) + " appels : ce serait le " + ordinal
		},
		OverForOneCall:  func(amount, limit string) string { return amount + " dépasse la limite de " + limit + " par appel" },
		OverTheTotal:    func(amount, limit string) string { return amount + " dépasserait le total de " + limit },
		OutsideCard:     "Hors de la Card",
		OverActionLimit: "Au-delà de la limite de la Card pour une action",
		OverCardTotal:   "Au-delà du total de la Card",
		NotCardResource: "Pas l'une des ressources de la Card",

		PendingCalls: func(calls int) string {
			if calls == 1 {
				return "1 d'entre eux est un appel que l'outil a refusé. Il compte jusqu'à ce que KIFF puisse confirmer que rien ne s'est passé."
			}
			return strconv.Itoa(calls) + " d'entre eux sont des appels que l'outil a refusés. Ils comptent jusqu'à ce que KIFF puisse confirmer que rien ne s'est passé."
		},
		PendingNoUnit: func(calls int) string {
			return "Une partie vient de " + frRefused(calls) + ". Elle compte jusqu'à ce que KIFF puisse confirmer que rien n'a bougé."
		},
		PendingAmount: func(amount string, calls int) string {
			return amount + " en viennent de " + frRefused(calls) + ". Ce montant compte jusqu'à ce que KIFF puisse confirmer que rien n'a bougé."
		},

		CardOf:  func(label string) string { return "Card de " + label },
		TafCard: "La Card du taf",
	}
}

func frRefused(n int) string {
	if n == 1 {
		return "1 appel que l'outil a refusé"
	}
	return strconv.Itoa(n) + " appels que l'outil a refusés"
}
