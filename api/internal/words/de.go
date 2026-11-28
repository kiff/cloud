package words

import "fmt"

// German (du), in the glossary's terms.
var de = Words{
	DecimalComma: true,

	WindowNextApproval:     "Dein Fingerabdruck oder Gesicht bei der nächsten Genehmigung",
	WindowAgainUnderMinute: "Dein Fingerabdruck oder Gesicht wieder in weniger als einer Minute",
	WindowNoneNextMinute:   "Kein Fingerabdruck oder Gesicht in der nächsten Minute",
	WindowNoneMinutes: func(n int) string {
		return fmt.Sprintf("Kein Fingerabdruck oder Gesicht in den nächsten %d Minuten", n)
	},

	Today:     "heute",
	Yesterday: "gestern",
	Months:    [12]string{"Jan.", "Feb.", "März", "Apr.", "Mai", "Juni", "Juli", "Aug.", "Sept.", "Okt.", "Nov.", "Dez."},
	DayDot:    true,

	HappenedYes:           "Ja, es ist durchgegangen",
	HappenedYesDetail:     "Es bleibt auf der Card angerechnet.",
	HappenedNo:            "Nein, ist es nicht",
	HappenedNoDetail:      "Die Card bekommt ihren Spielraum zurück. Beim Anbieter wird nichts rückgängig gemacht.",
	HappenedNotSure:       "Nicht sicher, hilf mir beim Prüfen",
	HappenedNotSureDetail: "Coach sieht sich mit dir die Aufzeichnungen des Anbieters an. Die Frage bleibt einen Tag bei dir.",
	HappenedNoAfter:       "Die Card hat ihren Spielraum zurück. Beim Anbieter wurde nichts rückgängig gemacht: Prüfe dort, ob die Zahlung doch durchgegangen ist, und wenn ja, sag es Coach.",
	WhyLost: func(vendor string, read bool) string {
		of := "des Anbieters"
		if vendor != "" {
			of = "von " + vendor
		}
		if read {
			return "Der Aufruf wurde gesendet, aber seine Antwort ging verloren, und KIFFs Abfragen der Aufzeichnungen " + of + " haben es noch nicht geklärt."
		}
		return "Der Aufruf wurde gesendet, aber seine Antwort ging verloren, und die Aufzeichnungen " + of + " lassen sich nicht nach diesem Aufruf durchsuchen."
	},
	WhyRefused: func(vendor string, read bool) string {
		if vendor == "" {
			vendor = "der Anbieter"
		}
		if read {
			return Capitalize(vendor) + " hat ihn abgelehnt, ohne zu sagen, ob etwas abgebucht wurde, und KIFFs Abfragen der Aufzeichnungen haben es noch nicht geklärt."
		}
		return Capitalize(vendor) + " hat ihn abgelehnt, ohne zu sagen, ob etwas abgebucht wurde, und die Aufzeichnungen lassen sich nicht nach diesem Aufruf durchsuchen."
	},
	WhyProbation: "Einer der ersten Aufrufe einer Verbindung, die KIFF noch mit keinem Testkonto ausprobiert hat: Bestätige, was passiert ist.",
	StatusRefused: func(vendor string) string {
		if vendor == "" {
			return "Der Anbieter hat ihn abgelehnt. Prüfe beim Anbieter, ob Geld geflossen ist."
		}
		return Capitalize(vendor) + " hat ihn abgelehnt. Prüfe bei " + vendor + ", ob Geld geflossen ist."
	},
	StatusLost: func(vendor string) string {
		if vendor == "" {
			return "Er wurde an den Anbieter gesendet, aber die Antwort ging verloren. Prüfe beim Anbieter, ob er durchgegangen ist."
		}
		return "Er wurde an " + vendor + " gesendet, aber die Antwort ging verloren. Prüfe bei " + vendor + ", ob er durchgegangen ist."
	},
	StatusProbation: "Einer der ersten Aufrufe über diese Verbindung: Prüfe, ob er getan hat, was du erwartet hast.",
	CallAtVendor:    func(action, vendor string) string { return action + " bei " + vendor },
	CallByTaf:       func(taf string) string { return "von " + taf },

	Reasons: map[string]string{
		"mandate_owner_rejected":       "Du hast es abgelehnt.",
		"mandate_owner_expired":        "Niemand hat rechtzeitig geantwortet.",
		"mandate_withdrawn":            "Es wurde zurückgezogen, bevor jemand geantwortet hat.",
		"mandate_per_action_exceeded":  "Es lag über dem Limit der Card für einen Aufruf.",
		"mandate_limit_reached":        "Es lag über einer der Summen der Card.",
		"mandate_resource_not_on_card": "Es betraf etwas, das die Card nicht abdeckt.",
		"mandate_unreadable_resource":  "KIFF konnte nicht lesen, worauf es sich bezog.",
		"mandate_not_covered":          "Keine Card deckt dieses Tool ab.",
		"mandate_category_closed":      "Es ist auf deiner Card geschlossen, bis du es öffnest.",
		"mandate_expired":              "Seine Card ist abgelaufen.",
		"no_mandate_issued":            "Keine Card deckt dieses Tool ab.",
		"amount_unit_mismatch":         "Es war in einer anderen Währung als der der Card.",
		"amount_unit_unverified":       "KIFF konnte die Währung nicht prüfen.",
		"amount_unverified":            "KIFF konnte den Betrag nicht lesen.",
		"halted":                       "Du hattest es angehalten.",
		"halt_unavailable":             "KIFF konnte nicht prüfen, ob es angehalten war.",
	},
	OwnerAsks: map[string]string{
		OwnerAskSealedMoney:  "dieses Tool bewegt Geld über eine Verbindung, deren Schlüssel KIFF hält, daher wartet jeder Aufruf auf Genehmigung",
		OwnerAskPurchase:     "dies kauft etwas, und die Karte fragt dich jedes Mal, oder es liegt außerhalb ihrer Grenzen",
		OwnerAskSecondRefund: "diese Zahlung hat schon eine Erstattung, daher wartet eine zweite auf Genehmigung",
		OwnerAskNoRequestID:  "der Agent hat keine Anfrage aus einer verbundenen App genannt, daher wartet diese Erstattung auf Genehmigung",
		OwnerAskOther:        "jeder Aufruf dieses Tools wartet auf Genehmigung",
		OwnerAskEachCall:     "jeder Aufruf dieses Tools wartet auf Genehmigung",
		OwnerAskSealedFill:   "dieser Schritt füllt eine in deinem Chrome gespeicherte Anmeldung aus, daher wartet er jedes Mal auf dich, und nur dein Telefon kann ihn freigeben",
	},
	ResourceMissing: func(param string) string {
		return fmt.Sprintf("der Aufruf nennt kein %s, und diese Card gilt nur für die %s, die sie aufführt", param, param)
	},
	ResourceNotOnCard: func(param, value string) string {
		return fmt.Sprintf("%s %q gehört nicht zu denen, die diese Card abdeckt", param, value)
	},
	OverPerCall: func(draw, limit string) string {
		return fmt.Sprintf("%s liegt über dem Limit von %s pro Aufruf", draw, limit)
	},
	OverTotal: func(draw, limit, used string) string {
		return fmt.Sprintf("%s würde das Limit von %s überschreiten: %s sind schon verbraucht", draw, limit, used)
	},

	// The held-call and Card-warning emails, and a warning's reason.
	HeldSubject:     "Dein Agent wartet auf deine Antwort",
	HeldNothingDone: "Es wurde nichts ausgeführt.",
	HeldOpen:        "Öffne es in KIFF, um zu antworten (du wirst gebeten, dich anzumelden):",
	HeldOptions:     "Du kannst es einmal genehmigen, die Card so ändern, dass sie es erlaubt, oder es ablehnen.",
	HeldIntro: func(agent, action, amount string) string {
		return agent + " möchte " + action + amount + " ausführen, was außerhalb seiner Card liegt."
	},
	HeldSteps: func(steps string) string {
		return "Die Card lässt dich es auch mit einem nächsten Schritt für den Agenten ablehnen: " + steps + "."
	},
	HeldExpires: func(when string) string {
		return "Wenn bis " + when + " niemand antwortet, wird es abgelehnt und der Agent muss erneut fragen."
	},
	HeldNoApprove:   "Über diese E-Mail kann nichts genehmigt werden.",
	HeldButtonsNote: "Jede Schaltfläche öffnet diese Aktion in KIFF, wo du dich anmeldest und deine Antwort bestätigst.",
	HeldApproveOnce: "Einmal genehmigen",
	HeldChangeCard:  "Card ändern, um es zu erlauben",
	HeldReject:      "Ablehnen",
	HeldWants: func(agent string) string {
		return fmt.Sprintf("%s möchte etwas tun, das außerhalb der Card liegt:", agent)
	},
	HeldRowAction: "Aktion", HeldRowAmount: "Betrag", HeldRowWhy: "Warum es zurückgehalten wurde",
	WarnSpent:  "Diese Summe der Card ist aufgebraucht.",
	WarnBurst:  "Eine neue Card hat zu Beginn dieses Zeitraums mehr als ihre Spitzenschwelle verbraucht.",
	WarnRunway: "Beim aktuellen Tempo ist diese Summe der Card bald aufgebraucht.",
	WarnUnusual: func(ratio string) string {
		return "Die jüngste Nutzung liegt beim " + ratio + "-Fachen ihres vergleichbaren Durchschnitts über sieben Tage."
	},
	WarnUsed:         func(pct, period string) string { return pct + " % dieser Summe sind " + period + " verbraucht." },
	Periods:          map[string]string{"calendar_day": "heute", "rolling_1h": "in der letzten Stunde", "rolling_24h": "in den letzten 24 Stunden", "rolling_7d": "in den letzten 7 Tagen", "lifetime": "bisher"},
	WarnNeverResets:  "Diese Summe wird nie zurückgesetzt. Füge Spielraum hinzu, ändere die Card oder pausiere ihren Agenten.",
	WarnReviewWindow: "Prüfe den Spielraum dieses Zeitraums, ändere die Card oder pausiere ihren Agenten.",
	WarnSubject:      "KIFF: Budgetwarnung für eine Card",
	WarnReview:       "Card ansehen",
}
