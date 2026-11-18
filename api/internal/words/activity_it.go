package words

// Italian Activity lines (tu). The call is feminine.
func init() {
	why := func(w string) string {
		if w == "" {
			return ""
		}
		return ": " + w
	}
	it.Activity = Activity{
		ByYou: "da te", BySomeone: "da una persona dell'account",
		ByRole:      map[string]string{"admin": "da un amministratore", "editor": "da un editor", "approver": "da un approvatore", "owner": "dal proprietario"},
		ByOtherRole: func(string) string { return "da una persona dell'account" },

		WaitingForYou:  "Ti aspetta",
		NotSent:        "Non inviato",
		NotSentBecause: func(reason string) string { return "Non inviato · " + reason },
		ApprovedOnceBy: func(by string) string { return "Approvata una volta " + by },
		ExtraRoomBy:    func(by string) string { return "Margine extra concesso " + by },
		RefusedBy:      func(by string) string { return "Rifiutata " + by },

		AllowedByCard:    "Consentita dalla sua Card",
		ApprovedBy:       func(by string) string { return "Approvata " + by },
		ApprovedThenDone: func(by string) string { return "Approvata " + by + ", poi fatta" },
		DoneWithinCard:   "Fatto, entro la sua Card",
		ConnectionRefused: func(w string) string {
			return "Non inviato: il collegamento l'ha rifiutata prima dell'invio" + why(w) + ". Conta ancora per i limiti di oggi finché KIFF non può confermare che non è stato fatto nulla."
		},
		AppRefusedCounts: func(w string) string { return "L'app l'ha rifiutata, e conta ancora per i limiti di oggi" + why(w) },
		AppRefusedMoney:  "L'app l'ha rifiutata. Conta nel totale di oggi finché KIFF non può confermare che non si è mosso denaro.",
		AppRefusedOther:  "L'app l'ha rifiutata. Conta nel totale di oggi finché KIFF non può confermare che non è stato fatto nulla.",
		NotSentToTool: func(allowed, w string) string {
			return allowed + ", ma non è stato possibile inviarla: nulla è arrivato allo strumento" + why(w)
		},
		Sending: func(allowed string) string { return allowed + ", e inviata: in attesa della risposta dello strumento" },
		AnswerLost: func(allowed string) string {
			return allowed + ", e inviata, ma la risposta dello strumento è andata persa: controlla i registri dello strumento stesso"
		},
		FoundAtVendor: func(allowed string) string {
			return allowed + ", e inviata. KIFF non sapeva com'era finita, quindi ha controllato i registri dello strumento stesso e l'ha trovata lì: è andata a buon fine"
		},
		NotRecorded: func(allowed string) string {
			return allowed + ", ma non è stato registrato se lo strumento l'ha fatto: controlla i registri dello strumento stesso"
		},
		ConnectionWhy: map[string]string{
			"halt_unknown":        "non ha potuto verificare se l'agente era bloccato",
			"halted":              "l'agente o l'account è bloccato",
			"halted_before":       "la chiamata è stata approvata prima dell'ultimo blocco",
			"not_granted":         "il collegamento non include questa azione",
			"permit_invalid":      "il permesso di KIFF non corrispondeva alla chiamata",
			"permit_expired":      "il permesso di KIFF è scaduto prima che la chiamata partisse",
			"bad_arguments":       "non è stato possibile leggere gli argomenti della chiamata",
			"record_unavailable":  "non ha potuto tenere traccia della chiamata",
			"profile_not_applied": "KIFF non ha ancora applicato le impostazioni approvate",
			"definition_invalid":  "l'azione indica un campo che l'app non accetta",
			"definition_unread":   "non ha potuto verificare l'azione con l'app",
			"operation_changed":   "l'app ha modificato questa azione da quando è stata approvata; il collegamento non invia nulla finché non viene ricollegato",
			"signed_out":          "l'app ha chiuso l'accesso; il collegamento non invia nulla finché non accedi di nuovo",
		},
	}
}
