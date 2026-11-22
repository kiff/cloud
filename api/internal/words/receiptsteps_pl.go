package words

// A receipt's steps in pl. A draft for the languages' owner to correct.
func init() {
	pl.ReceiptSteps = ReceiptSteps{
		ReceiptStepLines: map[string]string{
			"decision.allowed":           "Dozwolone",
			"decision.refused":           "Odmówione",
			"decision.held_for_owner":    "Oczekujące na właściciela",
			"decision.held_for_approval": "Oczekujące na zatwierdzenie",
			"decision.approved_no_room":  "Zatwierdzone, czeka na zapas",
			"decision.invalid":           "Nieprawidłowe",
			"settled.approved_once":      "Zatwierdzone raz",
			"settled.rejected":           "Odrzucone",
			"settled.rejected_next_step": "Odrzucone. Następny krok: %s",
			"settled.card_changed":       "Zamiast tego zmieniono Card",
			"settled.fit_on_retry":       "Nikt nie odpowiedział; przy ponownej próbie mieściło się w Card w ówczesnej postaci",
			"settled.expired":            "Nikt nie odpowiedział przed wygaśnięciem wstrzymania",
			"settled.withdrawn":          "Wycofane",
			"settled.withdrawn_reason":   "Wycofane: %s",
			"tool.reported":              "Narzędzie zgłosiło: %s",
			"handoff.handed":             "Przekazane właścicielowi; KIFF nic nie wysyła w tym kroku",
			"handoff.done":               "Właściciel mówi, że to zrobił",
			"handoff.skipped":            "Właściciel postanowił tego nie robić",
			"handoff.no_answer":          "Nikt nie odpowiedział przed wygaśnięciem; nigdy nie uznane za zrobione",
			"handoff.checked":            "Sprawdzone u dostawcy po odpowiedzi właściciela: %s",
			"owner.halted":               "Zablokowane przez właściciela: KIFF nie rozstrzyga żadnych jego wywołań do czasu wznowienia",
			"owner.resumed":              "Wznowione przez właściciela: nowe wywołania znów są rozstrzygane, nic sprzed blokady",
			"state.not_allowed":          "Niedozwolone w obecnym stanie: %s",
		},
		ReceiptAskedAgain: func(line string) string { return "Ponownie zapytano: " + line },
	}
}
