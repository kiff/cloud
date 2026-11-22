package words

import "fmt"

// French (vous), in the glossary's terms.
var fr = Words{
	DecimalComma: true,

	WindowNextApproval:     "Votre empreinte ou votre visage à la prochaine approbation",
	WindowAgainUnderMinute: "Votre empreinte ou votre visage à nouveau dans moins d'une minute",
	WindowNoneNextMinute:   "Ni empreinte ni visage pendant la prochaine minute",
	WindowNoneMinutes:      func(n int) string { return fmt.Sprintf("Ni empreinte ni visage pendant les %d prochaines minutes", n) },

	Today:     "aujourd'hui",
	Yesterday: "hier",
	Months:    [12]string{"janv.", "févr.", "mars", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.", "déc."},

	HappenedYes:           "Oui, c'est passé",
	HappenedYesDetail:     "Cela reste compté sur la Card.",
	HappenedNo:            "Non, ce n'est pas passé",
	HappenedNoDetail:      "La Card retrouve sa marge. Rien n'est annulé chez le fournisseur.",
	HappenedNotSure:       "Je ne sais pas, aidez-moi à vérifier",
	HappenedNotSureDetail: "Coach consulte avec vous l'historique du fournisseur. La question reste chez vous pendant un jour.",
	HappenedNoAfter:       "La Card retrouve sa marge. Rien n'a été annulé chez le fournisseur : vérifiez-y si le paiement a bien eu lieu et, si c'est le cas, dites-le à Coach.",
	WhyLost: func(vendor string, read bool) string {
		of := "du fournisseur"
		if vendor != "" {
			of = "de " + vendor
		}
		if read {
			return "L'appel a été envoyé, mais sa réponse s'est perdue, et les lectures par KIFF de l'historique " + of + " ne l'ont pas encore tranché."
		}
		return "L'appel a été envoyé, mais sa réponse s'est perdue, et l'historique " + of + " ne permet pas de retrouver cet appel."
	},
	WhyRefused: func(vendor string, read bool) string {
		if vendor == "" {
			vendor = "le fournisseur"
		}
		if read {
			return Capitalize(vendor) + " l'a refusé sans dire si quelque chose a été prélevé, et les lectures par KIFF de son historique ne l'ont pas encore tranché."
		}
		return Capitalize(vendor) + " l'a refusé sans dire si quelque chose a été prélevé, et son historique ne permet pas de retrouver cet appel."
	},
	WhyProbation: "L'un des premiers appels d'une connexion que KIFF n'a encore essayée avec aucun compte de test : confirmez ce qui s'est passé.",
	StatusRefused: func(vendor string) string {
		if vendor == "" {
			return "Le fournisseur l'a refusé. Vérifiez auprès du fournisseur si de l'argent a bougé."
		}
		return Capitalize(vendor) + " l'a refusé. Vérifiez dans " + vendor + " si de l'argent a bougé."
	},
	StatusLost: func(vendor string) string {
		if vendor == "" {
			return "Il a été envoyé au fournisseur, mais sa réponse s'est perdue. Vérifiez auprès du fournisseur s'il est passé."
		}
		return "Il a été envoyé à " + vendor + ", mais sa réponse s'est perdue. Vérifiez dans " + vendor + " s'il est passé."
	},
	StatusProbation: "L'un des premiers appels par cette connexion : vérifiez qu'il a fait ce que vous attendiez.",
	CallAtVendor:    func(action, vendor string) string { return action + " chez " + vendor },
	CallByTaf:       func(taf string) string { return "par " + taf },

	Reasons: map[string]string{
		"mandate_owner_rejected":       "Vous l'avez refusé.",
		"mandate_owner_expired":        "Personne n'a répondu à temps.",
		"mandate_withdrawn":            "Il a été retiré avant que quelqu'un ne réponde.",
		"mandate_per_action_exceeded":  "Il dépassait la limite de la Card pour un appel.",
		"mandate_limit_reached":        "Il dépassait l'un des totaux de la Card.",
		"mandate_resource_not_on_card": "Il portait sur quelque chose que la Card ne couvre pas.",
		"mandate_unreadable_resource":  "KIFF n'a pas pu lire sur quoi il portait.",
		"mandate_not_covered":          "Aucune Card ne couvre cet outil.",
		"mandate_expired":              "Sa Card a expiré.",
		"no_mandate_issued":            "Aucune Card ne couvre cet outil.",
		"amount_unit_mismatch":         "Il était dans une autre devise que celle de la Card.",
		"amount_unit_unverified":       "KIFF n'a pas pu vérifier sa devise.",
		"amount_unverified":            "KIFF n'a pas pu lire son montant.",
		"halted":                       "Vous l'aviez bloqué.",
		"halt_unavailable":             "KIFF n'a pas pu vérifier s'il était bloqué.",
	},
	OwnerAsks: map[string]string{
		OwnerAskSealedMoney:  "cet outil déplace de l'argent via une connexion dont KIFF détient la clé : chaque appel attend donc une approbation",
		OwnerAskSecondRefund: "ce paiement a déjà un remboursement, donc un deuxième attend une approbation",
		OwnerAskNoRequestID:  "l'agent n'a indiqué aucune demande venant d'une app connectée, donc ce remboursement attend une approbation",
		OwnerAskOther:        "chaque appel de cet outil attend une approbation",
		OwnerAskEachCall:     "chaque appel de cet outil attend une approbation",
	},
	ResourceMissing: func(param string) string {
		return fmt.Sprintf("l'appel n'indique aucun %s, et cette Card ne s'applique qu'aux %s qu'elle liste", param, param)
	},
	ResourceNotOnCard: func(param, value string) string {
		return fmt.Sprintf("%s %q ne fait pas partie de ceux que couvre cette Card", param, value)
	},
	OverPerCall: func(draw, limit string) string {
		return fmt.Sprintf("%s dépasse la limite de %s par appel", draw, limit)
	},
	OverTotal: func(draw, limit, used string) string {
		return fmt.Sprintf("%s dépasserait la limite de %s : %s sont déjà utilisés", draw, limit, used)
	},

	// The held-call and Card-warning emails, and a warning's reason.
	HeldSubject:     "Votre agent attend votre réponse",
	HeldNothingDone: "Rien n'a été fait.",
	HeldOpen:        "Ouvrez-le dans KIFF pour répondre (il vous sera demandé de vous connecter) :",
	HeldOptions:     "Vous pouvez l'approuver une fois, modifier la Card pour l'autoriser, ou le refuser.",
	HeldIntro: func(agent, action, amount string) string {
		return agent + " veut exécuter " + action + amount + ", ce qui sort de sa Card."
	},
	HeldSteps: func(steps string) string {
		return "La Card vous permet aussi de le refuser avec une étape suivante pour l'agent : " + steps + "."
	},
	HeldExpires: func(when string) string {
		return "Si personne ne répond avant le " + when + ", il est refusé et l'agent doit redemander."
	},
	HeldNoApprove:   "Rien ne peut être approuvé depuis cet e-mail.",
	HeldButtonsNote: "Chaque bouton ouvre cette action dans KIFF, où vous vous connectez et confirmez votre réponse.",
	HeldApproveOnce: "Approuver une fois",
	HeldChangeCard:  "Modifier la Card pour l'autoriser",
	HeldReject:      "Refuser",
	HeldWants: func(agent string) string {
		return fmt.Sprintf("%s veut faire quelque chose qui sort de sa Card :", agent)
	},
	HeldRowAction: "Action demandée", HeldRowAmount: "Montant", HeldRowWhy: "Pourquoi elle est retenue",
	WarnSpent:  "Ce total de la Card est épuisé.",
	WarnBurst:  "Une nouvelle Card a dépassé son seuil de pic au début de cette période.",
	WarnRunway: "Au rythme récent, ce total de la Card sera bientôt épuisé.",
	WarnUnusual: func(ratio string) string {
		return "L'utilisation récente atteint " + ratio + " fois sa moyenne équivalente sur sept jours."
	},
	WarnUsed:         func(pct, period string) string { return pct + " % de ce total est utilisé " + period + "." },
	Periods:          map[string]string{"calendar_day": "aujourd'hui", "rolling_1h": "sur la dernière heure", "rolling_24h": "sur les dernières 24 heures", "rolling_7d": "sur les 7 derniers jours", "lifetime": "jusqu'ici"},
	WarnNeverResets:  "Ce total ne se réinitialise jamais. Ajoutez de la marge, modifiez la Card ou mettez son agent en pause.",
	WarnReviewWindow: "Vérifiez la marge de cette période, modifiez la Card ou mettez son agent en pause.",
	WarnSubject:      "KIFF : alerte de budget d'une Card",
	WarnReview:       "Voir la Card",
}
