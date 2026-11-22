package words

// A receipt's steps in fr. A draft for the languages' owner to correct.
func init() {
	fr.ReceiptSteps = ReceiptSteps{
		ReceiptStepLines: map[string]string{
			"decision.allowed":           "Autorisé",
			"decision.refused":           "Refusé",
			"decision.held_for_owner":    "Retenu pour son propriétaire",
			"decision.held_for_approval": "Retenu pour approbation",
			"decision.approved_no_room":  "Approuvé, en attente de marge",
			"decision.invalid":           "Invalide",
			"settled.approved_once":      "Approuvé une fois",
			"settled.rejected":           "Rejeté",
			"settled.rejected_next_step": "Rejeté. Étape suivante : %s",
			"settled.card_changed":       "La Card a été modifiée à la place",
			"settled.fit_on_retry":       "Personne n’a répondu ; lors d’un nouvel essai, cela rentrait dans la Card telle qu’elle était",
			"settled.expired":            "Personne n’a répondu avant l’expiration de la retenue",
			"settled.withdrawn":          "Retiré",
			"settled.withdrawn_reason":   "Retiré : %s",
			"tool.reported":              "L’outil a indiqué : %s",
			"handoff.handed":             "Confié à son propriétaire ; KIFF n’envoie rien pour cette étape",
			"handoff.done":               "Le propriétaire dit l’avoir fait",
			"handoff.skipped":            "Le propriétaire a choisi de ne pas le faire",
			"handoff.no_answer":          "Personne n’a répondu avant l’expiration ; jamais considéré comme fait",
			"handoff.checked":            "Vérifié auprès du fournisseur après la réponse du propriétaire : %s",
			"owner.halted":               "Bloqué par le propriétaire : KIFF ne décide aucun de ses appels jusqu’à ce qu’il reprenne",
			"owner.resumed":              "Repris par le propriétaire : les nouveaux appels sont de nouveau décidés, rien de ce qui a été demandé avant le blocage",
			"state.not_allowed":          "Non autorisé dans son état actuel : %s",
		},
		ReceiptAskedAgain: func(line string) string { return "Redemandé : " + line },
	}
}
