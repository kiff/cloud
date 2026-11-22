package words

// A receipt's steps in it. A draft for the languages' owner to correct.
func init() {
	it.ReceiptSteps = ReceiptSteps{
		ReceiptStepLines: map[string]string{
			"decision.allowed":           "Consentita",
			"decision.refused":           "Rifiutata",
			"decision.held_for_owner":    "Trattenuta per il suo proprietario",
			"decision.held_for_approval": "Trattenuta per l’approvazione",
			"decision.approved_no_room":  "Approvata, in attesa di margine",
			"decision.invalid":           "Non valida",
			"settled.approved_once":      "Approvata una volta",
			"settled.rejected":           "Respinta",
			"settled.rejected_next_step": "Respinta. Passo successivo: %s",
			"settled.card_changed":       "Invece è stata cambiata la Card",
			"settled.fit_on_retry":       "Nessuno ha risposto; a un nuovo tentativo rientrava nella Card com’era allora",
			"settled.expired":            "Nessuno ha risposto prima che la trattenuta scadesse",
			"settled.withdrawn":          "Ritirata",
			"settled.withdrawn_reason":   "Ritirata: %s",
			"tool.reported":              "Lo strumento ha riportato: %s",
			"handoff.handed":             "Affidata al suo proprietario; KIFF non invia nulla per questo passo",
			"handoff.done":               "Il proprietario dice di averlo fatto",
			"handoff.skipped":            "Il proprietario ha scelto di non farlo",
			"handoff.no_answer":          "Nessuno ha risposto prima della scadenza; mai considerata fatta",
			"handoff.checked":            "Verificato con il fornitore dopo la risposta del proprietario: %s",
			"owner.halted":               "Bloccato dal proprietario: KIFF non decide nessuna delle sue chiamate finché non riprende",
			"owner.resumed":              "Ripreso dal proprietario: le nuove chiamate sono di nuovo decise, nulla di quanto chiesto prima del blocco",
			"state.not_allowed":          "Non consentita nel suo stato attuale: %s",
		},
		ReceiptAskedAgain: func(line string) string { return "Chiesta di nuovo: " + line },
	}
}
