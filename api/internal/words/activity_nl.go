package words

// Dutch Activity lines (je).
func init() {
	why := func(w string) string {
		if w == "" {
			return ""
		}
		return ": " + w
	}
	nl.Activity = Activity{
		ByYou: "door jou", BySomeone: "door iemand van het account",
		ByRole:      map[string]string{"admin": "door een beheerder", "editor": "door een bewerker", "approver": "door een goedkeurder", "owner": "door de eigenaar"},
		ByOtherRole: func(string) string { return "door iemand van het account" },

		WaitingForYou:  "Wacht op jou",
		NotSent:        "Niet verzonden",
		NotSentBecause: func(reason string) string { return "Niet verzonden · " + reason },
		ApprovedOnceBy: func(by string) string { return "Eenmalig goedgekeurd " + by },
		ExtraRoomBy:    func(by string) string { return "Extra ruimte gegeven " + by },
		RefusedBy:      func(by string) string { return "Geweigerd " + by },

		AllowedByCard:    "Toegestaan door de Card",
		ApprovedBy:       func(by string) string { return "Goedgekeurd " + by },
		ApprovedThenDone: func(by string) string { return "Goedgekeurd " + by + ", daarna gedaan" },
		DoneWithinCard:   "Klaar, binnen de Card",
		ConnectionRefused: func(w string) string {
			return "Niet verzonden: de koppeling weigerde het vóór verzending" + why(w) + ". Het telt nog mee voor de limieten van vandaag tot KIFF kan bevestigen dat er niets is gedaan."
		},
		AppRefusedCounts: func(w string) string {
			return "De app weigerde het, en het telt nog mee voor de limieten van vandaag" + why(w)
		},
		AppRefusedMoney: "De app weigerde het. Het telt mee voor het totaal van vandaag tot KIFF kan bevestigen dat er geen geld is verplaatst.",
		AppRefusedOther: "De app weigerde het. Het telt mee voor het totaal van vandaag tot KIFF kan bevestigen dat er niets is gedaan.",
		NotSentToTool: func(allowed, w string) string {
			return allowed + ", maar het kon niet worden verzonden: er kwam niets bij de tool aan" + why(w)
		},
		Sending: func(allowed string) string { return allowed + ", en verzonden: wacht op het antwoord van de tool" },
		AnswerLost: func(allowed string) string {
			return allowed + ", en verzonden, maar het antwoord van de tool ging verloren: kijk in de eigen gegevens van de tool"
		},
		FoundAtVendor: func(allowed string) string {
			return allowed + ", en verzonden. KIFF kon niet zien hoe het afliep, dus keek het in de eigen gegevens van de tool en vond het daar: het is gelukt"
		},
		NotRecorded: func(allowed string) string {
			return allowed + ", maar of de tool het deed, is niet vastgelegd: kijk in de eigen gegevens van de tool"
		},
		ConnectionWhy: map[string]string{
			"halt_unknown":           "die kon niet controleren of de agent stilgezet was",
			"halted":                 "de agent of het account is stilgezet",
			"halted_before":          "de aanroep werd goedgekeurd vóór de laatste stilzetting",
			"not_granted":            "de koppeling bevat deze actie niet",
			"permit_invalid":         "de vergunning van KIFF paste niet bij de aanroep",
			"permit_expired":         "de vergunning van KIFF verliep voordat de aanroep vertrok",
			"bad_arguments":          "de argumenten van de aanroep konden niet worden gelezen",
			"record_unavailable":     "die kon de aanroep niet vastleggen",
			"profile_not_applied":    "KIFF heeft de goedgekeurde instellingen nog niet toegepast",
			"definition_invalid":     "de actie noemt een veld dat de app niet accepteert",
			"definition_unread":      "die kon de actie niet controleren bij de app",
			"operation_changed":      "de app heeft deze actie gewijzigd sinds de goedkeuring; de koppeling verstuurt niets tot ze opnieuw gekoppeld is",
			"signed_out":             "de app heeft de aanmelding beëindigd; de koppeling verstuurt niets tot je je opnieuw aanmeldt",
			"app_closed":             "deze app wordt niet meer aangeboden, dus de koppeling is gesloten",
			"connection_unavailable": "de koppeling, of het account bij de app, kon op dat moment niet worden gebruikt",
			"tool_not_served":        "deze actie wordt nooit uitgevoerd voor deze app",
			"waits_for_key":          "deze actie heeft de sleutel van de app nodig, en die is nog niet toegevoegd",
			"waits_for_approval":     "deze actie is aan de koppeling toegevoegd en wacht nog op je goedkeuring",
		},
	}
}
