package words

// A receipt's steps in es. A draft for the languages' owner to correct.
func init() {
	es.ReceiptSteps = ReceiptSteps{
		ReceiptStepLines: map[string]string{
			"decision.allowed":           "Permitida",
			"decision.refused":           "Denegada",
			"decision.held_for_owner":    "Retenida para su propietario",
			"decision.held_for_approval": "Retenida para aprobación",
			"decision.approved_no_room":  "Aprobada, esperando margen",
			"decision.invalid":           "No válida",
			"settled.approved_once":      "Aprobada una vez",
			"settled.rejected":           "Rechazada",
			"settled.rejected_next_step": "Rechazada. Siguiente paso: %s",
			"settled.card_changed":       "En su lugar se cambió la Card",
			"settled.fit_on_retry":       "Nadie respondió; en un nuevo intento cabía en la Card tal como estaba",
			"settled.expired":            "Nadie respondió antes de que venciera la retención",
			"settled.withdrawn":          "Retirada",
			"settled.withdrawn_reason":   "Retirada: %s",
			"tool.reported":              "La herramienta indicó: %s",
			"handoff.handed":             "Entregada a su propietario; KIFF no envía nada en este paso",
			"handoff.done":               "El propietario dice que lo hizo",
			"handoff.skipped":            "El propietario decidió no hacerlo",
			"handoff.no_answer":          "Nadie respondió antes de que venciera; nunca se toma como hecha",
			"handoff.checked":            "Comprobado con el proveedor tras la respuesta del propietario: %s",
			"owner.halted":               "Bloqueado por el propietario: KIFF no decide ninguna de sus llamadas hasta que se reanude",
			"owner.resumed":              "Reanudado por el propietario: las llamadas nuevas se deciden de nuevo, nada de lo pedido antes del bloqueo",
			"state.not_allowed":          "No permitida en su estado actual: %s",
		},
		ReceiptAskedAgain: func(line string) string { return "Pedida otra vez: " + line },
	}
}
