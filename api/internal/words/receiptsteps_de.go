package words

// A receipt's steps in de. A draft for the languages' owner to correct.
func init() {
	de.ReceiptSteps = ReceiptSteps{
		ReceiptStepLines: map[string]string{
			"decision.allowed":           "Erlaubt",
			"decision.refused":           "Abgelehnt",
			"decision.held_for_owner":    "Für den Besitzer zurückgehalten",
			"decision.held_for_approval": "Zur Genehmigung zurückgehalten",
			"decision.approved_no_room":  "Genehmigt, wartet auf Spielraum",
			"decision.invalid":           "Ungültig",
			"settled.approved_once":      "Einmal genehmigt",
			"settled.rejected":           "Zurückgewiesen",
			"settled.rejected_next_step": "Zurückgewiesen. Nächster Schritt: %s",
			"settled.card_changed":       "Stattdessen wurde die Card geändert",
			"settled.fit_on_retry":       "Niemand hat geantwortet; bei einem neuen Versuch passte es zur Card, wie sie da war",
			"settled.expired":            "Niemand hat geantwortet, bevor die Zurückhaltung ablief",
			"settled.withdrawn":          "Zurückgezogen",
			"settled.withdrawn_reason":   "Zurückgezogen: %s",
			"tool.reported":              "Das Werkzeug meldete: %s",
			"handoff.handed":             "An den Besitzer übergeben; KIFF sendet für diesen Schritt nichts",
			"handoff.done":               "Der Besitzer sagt, er hat es erledigt",
			"handoff.skipped":            "Der Besitzer hat sich dagegen entschieden",
			"handoff.no_answer":          "Niemand hat geantwortet, bevor es ablief; nie als erledigt gewertet",
			"handoff.checked":            "Nach der Antwort des Besitzers beim Anbieter geprüft: %s",
			"owner.halted":               "Vom Besitzer angehalten: KIFF entscheidet keinen seiner Aufrufe, bis es fortgesetzt wird",
			"owner.resumed":              "Vom Besitzer fortgesetzt: neue Aufrufe werden wieder entschieden, nichts von vor dem Anhalten",
			"state.not_allowed":          "In seinem aktuellen Zustand nicht erlaubt: %s",
		},
		ReceiptAskedAgain: func(line string) string { return "Erneut angefragt: " + line },
	}
}
