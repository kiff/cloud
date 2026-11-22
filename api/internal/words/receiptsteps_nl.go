package words

// A receipt's steps in nl. A draft for the languages' owner to correct.
func init() {
	nl.ReceiptSteps = ReceiptSteps{
		ReceiptStepLines: map[string]string{
			"decision.allowed":           "Toegestaan",
			"decision.refused":           "Geweigerd",
			"decision.held_for_owner":    "Vastgehouden voor de eigenaar",
			"decision.held_for_approval": "Vastgehouden voor goedkeuring",
			"decision.approved_no_room":  "Goedgekeurd, wacht op ruimte",
			"decision.invalid":           "Ongeldig",
			"settled.approved_once":      "Eenmalig goedgekeurd",
			"settled.rejected":           "Afgewezen",
			"settled.rejected_next_step": "Afgewezen. Volgende stap: %s",
			"settled.card_changed":       "In plaats daarvan is de Card gewijzigd",
			"settled.fit_on_retry":       "Niemand antwoordde; bij een nieuwe poging paste het in de Card zoals die toen was",
			"settled.expired":            "Niemand antwoordde voordat het vasthouden verliep",
			"settled.withdrawn":          "Ingetrokken",
			"settled.withdrawn_reason":   "Ingetrokken: %s",
			"tool.reported":              "De tool meldde: %s",
			"handoff.handed":             "Overgedragen aan de eigenaar; KIFF stuurt niets voor deze stap",
			"handoff.done":               "De eigenaar zegt dat hij het heeft gedaan",
			"handoff.skipped":            "De eigenaar koos ervoor het niet te doen",
			"handoff.no_answer":          "Niemand antwoordde voordat het verliep; nooit als gedaan gelezen",
			"handoff.checked":            "Na het antwoord van de eigenaar bij de leverancier gecontroleerd: %s",
			"owner.halted":               "Stilgezet door de eigenaar: KIFF beslist geen van de aanroepen tot het wordt hervat",
			"owner.resumed":              "Hervat door de eigenaar: nieuwe aanroepen worden weer beslist, niets van vóór het stilzetten",
			"state.not_allowed":          "Niet toegestaan in de huidige staat: %s",
		},
		ReceiptAskedAgain: func(line string) string { return "Opnieuw gevraagd: " + line },
	}
}
