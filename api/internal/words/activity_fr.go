package words

// French Activity lines (vous).
func init() {
	why := func(w string) string {
		if w == "" {
			return ""
		}
		return " : " + w
	}
	fr.Activity = Activity{
		ByYou: "par vous", BySomeone: "par une personne du compte",
		ByRole:      map[string]string{"admin": "par un administrateur", "editor": "par un éditeur", "approver": "par un approbateur", "owner": "par le propriétaire"},
		ByOtherRole: func(string) string { return "par une personne du compte" },

		WaitingForYou:  "Vous attend",
		NotSent:        "Non envoyé",
		NotSentBecause: func(reason string) string { return "Non envoyé · " + reason },
		ApprovedOnceBy: func(by string) string { return "Approuvé une fois " + by },
		ExtraRoomBy:    func(by string) string { return "Marge supplémentaire accordée " + by },
		RefusedBy:      func(by string) string { return "Refusé " + by },

		AllowedByCard:    "Autorisé par sa Card",
		ApprovedBy:       func(by string) string { return "Approuvé " + by },
		ApprovedThenDone: func(by string) string { return "Approuvé " + by + ", puis terminé" },
		DoneWithinCard:   "Terminé, dans les limites de sa Card",
		ConnectionRefused: func(w string) string {
			return "Non envoyé : la connexion l'a refusé avant l'envoi" + why(w) + ". Il compte encore dans les limites du jour jusqu'à ce que KIFF puisse confirmer que rien n'a été fait."
		},
		AppRefusedCounts: func(w string) string {
			return "L'app l'a refusé, et il compte encore dans les limites du jour" + why(w)
		},
		AppRefusedMoney: "L'app l'a refusé. Il compte dans le total du jour jusqu'à ce que KIFF puisse confirmer qu'aucun argent n'a bougé.",
		AppRefusedOther: "L'app l'a refusé. Il compte dans le total du jour jusqu'à ce que KIFF puisse confirmer que rien n'a été fait.",
		NotSentToTool: func(allowed, w string) string {
			return allowed + ", mais il n'a pas pu être envoyé : rien n'a atteint l'outil" + why(w)
		},
		Sending: func(allowed string) string { return allowed + ", et envoyé : en attente de la réponse de l'outil" },
		AnswerLost: func(allowed string) string {
			return allowed + ", et envoyé, mais la réponse de l'outil a été perdue : vérifiez les enregistrements de l'outil lui-même"
		},
		FoundAtVendor: func(allowed string) string {
			return allowed + ", et envoyé. KIFF ne savait pas comment cela s'était terminé ; il a vérifié les enregistrements de l'outil lui-même et l'y a trouvé : c'est passé"
		},
		NotRecorded: func(allowed string) string {
			return allowed + ", mais rien n'indique si l'outil l'a fait : vérifiez les enregistrements de l'outil lui-même"
		},
		ConnectionWhy: map[string]string{
			"halt_unknown":        "elle n'a pas pu vérifier si l'agent était bloqué",
			"halted":              "l'agent ou le compte est bloqué",
			"halted_before":       "l'appel a été approuvé avant le dernier blocage",
			"not_granted":         "la connexion n'inclut pas cette action",
			"permit_invalid":      "le permis de KIFF ne correspondait pas à l'appel",
			"permit_expired":      "le permis de KIFF a expiré avant le départ de l'appel",
			"bad_arguments":       "les arguments de l'appel n'ont pas pu être lus",
			"record_unavailable":  "elle n'a pas pu garder une trace de l'appel",
			"profile_not_applied": "KIFF n'a pas encore appliqué les réglages approuvés",
			"definition_invalid":  "l'action nomme un champ que l'app n'accepte pas",
			"definition_unread":   "elle n'a pas pu vérifier l'action auprès de l'app",
			"operation_changed":   "l'app a modifié cette action depuis son approbation ; la connexion n'envoie rien tant qu'elle n'est pas reconnectée",
			"signed_out":          "l'app a mis fin à la session ; la connexion n'envoie rien tant que vous ne vous reconnectez pas",
		},
	}
}
