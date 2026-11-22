package words

import "fmt"

// Dutch (je), in the glossary's terms.
var nl = Words{
	DecimalComma: true,

	WindowNextApproval:     "Je vingerafdruk of gezicht bij de volgende goedkeuring",
	WindowAgainUnderMinute: "Over minder dan een minuut weer je vingerafdruk of gezicht",
	WindowNoneNextMinute:   "Geen vingerafdruk of gezicht de komende minuut",
	WindowNoneMinutes:      func(n int) string { return fmt.Sprintf("Geen vingerafdruk of gezicht de komende %d minuten", n) },

	Today:     "vandaag",
	Yesterday: "gisteren",
	Months:    [12]string{"jan", "feb", "mrt", "apr", "mei", "jun", "jul", "aug", "sep", "okt", "nov", "dec"},

	HappenedYes:           "Ja, het is gelukt",
	HappenedYesDetail:     "Het blijft meetellen op de Card.",
	HappenedNo:            "Nee, het is niet gelukt",
	HappenedNoDetail:      "De Card krijgt zijn ruimte terug. Bij de leverancier wordt niets teruggedraaid.",
	HappenedNotSure:       "Niet zeker, help me het na te gaan",
	HappenedNotSureDetail: "Coach bekijkt samen met jou de gegevens van de leverancier. De vraag blijft een dag bij jou.",
	HappenedNoAfter:       "De Card heeft zijn ruimte terug. Bij de leverancier is niets teruggedraaid: kijk daar of de betaling toch is gelukt, en zo ja, laat het Coach weten.",
	WhyLost: func(vendor string, read bool) string {
		of := "van de leverancier"
		if vendor != "" {
			of = "van " + vendor
		}
		if read {
			return "De aanroep is verzonden, maar het antwoord is verloren gegaan, en KIFF heeft het in de gegevens " + of + " nog niet kunnen vaststellen."
		}
		return "De aanroep is verzonden, maar het antwoord is verloren gegaan, en in de gegevens " + of + " kan niet naar deze aanroep worden gezocht."
	},
	WhyRefused: func(vendor string, read bool) string {
		if vendor == "" {
			vendor = "de leverancier"
		}
		if read {
			return Capitalize(vendor) + " heeft de aanroep geweigerd zonder te zeggen of er iets is afgeschreven, en KIFF heeft het in de gegevens nog niet kunnen vaststellen."
		}
		return Capitalize(vendor) + " heeft de aanroep geweigerd zonder te zeggen of er iets is afgeschreven, en in de gegevens kan niet naar deze aanroep worden gezocht."
	},
	WhyProbation: "Een van de eerste aanroepen van een koppeling die KIFF nog met geen enkel testaccount heeft geprobeerd: bevestig wat er is gebeurd.",
	StatusRefused: func(vendor string) string {
		if vendor == "" {
			return "De leverancier heeft hem geweigerd. Kijk bij de leverancier of er geld is overgemaakt."
		}
		return Capitalize(vendor) + " heeft hem geweigerd. Kijk in " + vendor + " of er geld is overgemaakt."
	},
	StatusLost: func(vendor string) string {
		if vendor == "" {
			return "De aanroep is naar de leverancier gestuurd, maar het antwoord ging verloren. Kijk bij de leverancier of het is gelukt."
		}
		return "De aanroep is naar " + vendor + " gestuurd, maar het antwoord ging verloren. Kijk in " + vendor + " of het is gelukt."
	},
	StatusProbation: "Een van de eerste aanroepen via deze koppeling: kijk of hij deed wat je verwachtte.",
	CallAtVendor:    func(action, vendor string) string { return action + " bij " + vendor },
	CallByTaf:       func(taf string) string { return "door " + taf },

	Reasons: map[string]string{
		"mandate_owner_rejected":       "Je hebt het geweigerd.",
		"mandate_owner_expired":        "Niemand heeft op tijd geantwoord.",
		"mandate_withdrawn":            "Het werd ingetrokken voordat iemand antwoordde.",
		"mandate_per_action_exceeded":  "Het ging over de limiet van de Card voor één aanroep.",
		"mandate_limit_reached":        "Het ging over een van de totalen van de Card.",
		"mandate_resource_not_on_card": "Het ging om iets wat de Card niet dekt.",
		"mandate_unreadable_resource":  "KIFF kon niet lezen waar het om ging.",
		"mandate_not_covered":          "Geen enkele Card dekt deze tool.",
		"mandate_expired":              "De Card is verlopen.",
		"no_mandate_issued":            "Geen enkele Card dekt deze tool.",
		"amount_unit_mismatch":         "Het was in een andere valuta dan die van de Card.",
		"amount_unit_unverified":       "KIFF kon de valuta niet controleren.",
		"amount_unverified":            "KIFF kon het bedrag niet lezen.",
		"halted":                       "Je had het stilgezet.",
		"halt_unavailable":             "KIFF kon niet controleren of het was stilgezet.",
	},
	OwnerAsks: map[string]string{
		OwnerAskSealedMoney:  "deze tool verplaatst geld via een koppeling waarvan KIFF de sleutel heeft, dus elke aanroep wacht op goedkeuring",
		OwnerAskSecondRefund: "deze betaling heeft al een terugbetaling, dus een tweede wacht op goedkeuring",
		OwnerAskNoRequestID:  "de agent noemde geen verzoek uit een gekoppelde app, dus deze terugbetaling wacht op goedkeuring",
		OwnerAskOther:        "elke aanroep van deze tool wacht op goedkeuring",
		OwnerAskEachCall:     "elke aanroep van deze tool wacht op goedkeuring",
	},
	ResourceMissing: func(param string) string {
		return fmt.Sprintf("de aanroep noemt geen %s, en deze Card geldt alleen voor de %s die erop staan", param, param)
	},
	ResourceNotOnCard: func(param, value string) string {
		return fmt.Sprintf("%s %q hoort niet bij wat deze Card dekt", param, value)
	},
	OverPerCall: func(draw, limit string) string {
		return fmt.Sprintf("%s ligt boven de limiet van %s per aanroep", draw, limit)
	},
	OverTotal: func(draw, limit, used string) string {
		return fmt.Sprintf("%s zou over de limiet van %s gaan: %s is al gebruikt", draw, limit, used)
	},

	// The held-call and Card-warning emails, and a warning's reason.
	HeldSubject:     "Je agent wacht op je antwoord",
	HeldNothingDone: "Er is niets gedaan.",
	HeldOpen:        "Open het in KIFF om te antwoorden (je wordt gevraagd in te loggen):",
	HeldOptions:     "Je kunt het eenmalig goedkeuren, de Card wijzigen om het toe te staan, of het weigeren.",
	HeldIntro: func(agent, action, amount string) string {
		return agent + " wil " + action + amount + " uitvoeren, wat buiten zijn Card valt."
	},
	HeldSteps: func(steps string) string {
		return "Met de Card kun je het ook weigeren met een volgende stap voor de agent: " + steps + "."
	},
	HeldExpires: func(when string) string {
		return "Als niemand vóór " + when + " antwoordt, wordt het geweigerd en moet de agent het opnieuw vragen."
	},
	HeldNoApprove:   "Via deze e-mail kan niets worden goedgekeurd.",
	HeldButtonsNote: "Elke knop opent deze actie in KIFF, waar je inlogt en je antwoord bevestigt.",
	HeldApproveOnce: "Eenmalig goedkeuren",
	HeldChangeCard:  "Card wijzigen om het toe te staan",
	HeldReject:      "Weigeren",
	HeldWants: func(agent string) string {
		return fmt.Sprintf("%s wil iets doen wat buiten zijn Card valt:", agent)
	},
	HeldRowAction: "Actie", HeldRowAmount: "Bedrag", HeldRowWhy: "Waarom het wordt vastgehouden",
	WarnSpent:  "Dit totaal van de Card is op.",
	WarnBurst:  "Een nieuwe Card heeft aan het begin van deze periode meer dan zijn piekdrempel gebruikt.",
	WarnRunway: "In het recente tempo is dit totaal van de Card bijna op.",
	WarnUnusual: func(ratio string) string {
		return "Het recente gebruik is " + ratio + " keer het vergelijkbare gemiddelde over zeven dagen."
	},
	WarnUsed:         func(pct, period string) string { return pct + "% van dit totaal is " + period + " gebruikt." },
	Periods:          map[string]string{"calendar_day": "vandaag", "rolling_1h": "in het afgelopen uur", "rolling_24h": "in de afgelopen 24 uur", "rolling_7d": "in de afgelopen 7 dagen", "lifetime": "tot nu toe"},
	WarnNeverResets:  "Dit totaal wordt nooit gereset. Voeg ruimte toe, wijzig de Card of pauzeer de agent.",
	WarnReviewWindow: "Bekijk de ruimte van deze periode, wijzig de Card of pauzeer de agent.",
	WarnSubject:      "KIFF: budgetwaarschuwing voor een Card",
	WarnReview:       "Card bekijken",
}
