package words

import "fmt"

// Italian (tu), in the glossary's terms.
var it = Words{
	DecimalComma: true,

	WindowNextApproval:     "La tua impronta o il tuo volto alla prossima approvazione",
	WindowAgainUnderMinute: "Di nuovo la tua impronta o il tuo volto tra meno di un minuto",
	WindowNoneNextMinute:   "Né impronta né volto per il prossimo minuto",
	WindowNoneMinutes:      func(n int) string { return fmt.Sprintf("Né impronta né volto per i prossimi %d minuti", n) },

	Today:     "oggi",
	Yesterday: "ieri",
	Months:    [12]string{"gen", "feb", "mar", "apr", "mag", "giu", "lug", "ago", "set", "ott", "nov", "dic"},

	HappenedYes:           "Sì, è andata a buon fine",
	HappenedYesDetail:     "Resta conteggiata sulla Card.",
	HappenedNo:            "No, non è andata",
	HappenedNoDetail:      "La Card recupera il suo margine. Presso il fornitore non viene annullato nulla.",
	HappenedNotSure:       "Non sono sicuro, aiutami a verificare",
	HappenedNotSureDetail: "Coach controlla con te lo storico del fornitore. La domanda resta a te per un giorno.",
	HappenedNoAfter:       "La Card ha recuperato il suo margine. Presso il fornitore non è stato annullato nulla: controlla lì se il pagamento è andato a buon fine e, se sì, dillo a Coach.",
	WhyLost: func(vendor string, read bool) string {
		of := "del fornitore"
		if vendor != "" {
			of = "di " + vendor
		}
		if read {
			return "È stata inviata, ma la sua risposta è andata persa, e le letture di KIFF dello storico " + of + " non l'hanno ancora chiarito."
		}
		return "È stata inviata, ma la sua risposta è andata persa, e nello storico " + of + " non si può cercare questa chiamata."
	},
	WhyRefused: func(vendor string, read bool) string {
		if vendor == "" {
			vendor = "il fornitore"
		}
		if read {
			return Capitalize(vendor) + " l'ha rifiutata senza dire se è stato addebitato qualcosa, e le letture di KIFF del suo storico non l'hanno ancora chiarito."
		}
		return Capitalize(vendor) + " l'ha rifiutata senza dire se è stato addebitato qualcosa, e nel suo storico non si può cercare questa chiamata."
	},
	WhyProbation: "Una delle prime chiamate di un collegamento che KIFF non ha ancora provato con un account di prova: conferma cosa è successo.",
	StatusRefused: func(vendor string) string {
		if vendor == "" {
			return "Il fornitore l'ha rifiutata. Controlla presso il fornitore se è stato mosso denaro."
		}
		return Capitalize(vendor) + " l'ha rifiutata. Controlla su " + vendor + " se è stato mosso denaro."
	},
	StatusLost: func(vendor string) string {
		if vendor == "" {
			return "È stata inviata al fornitore, ma la risposta è andata persa. Controlla presso il fornitore se è andata a buon fine."
		}
		return "È stata inviata a " + vendor + ", ma la risposta è andata persa. Controlla su " + vendor + " se è andata a buon fine."
	},
	StatusProbation: "Una delle prime chiamate tramite questo collegamento: controlla che abbia fatto quello che ti aspettavi.",
	CallAtVendor:    func(action, vendor string) string { return action + " su " + vendor },
	CallByTaf:       func(taf string) string { return "da " + taf },

	Reasons: map[string]string{
		"mandate_owner_rejected":       "L'hai rifiutata.",
		"mandate_owner_expired":        "Nessuno ha risposto in tempo.",
		"mandate_withdrawn":            "È stata ritirata prima che qualcuno rispondesse.",
		"mandate_per_action_exceeded":  "Superava il limite della Card per una chiamata.",
		"mandate_limit_reached":        "Superava uno dei totali della Card.",
		"mandate_resource_not_on_card": "Riguardava qualcosa che la Card non copre.",
		"mandate_unreadable_resource":  "KIFF non ha potuto leggere su cosa agiva.",
		"mandate_not_covered":          "Nessuna Card copre questo strumento.",
		"mandate_category_closed":      "È chiuso sulla tua Card finché non lo apri.",
		"mandate_expired":              "La sua Card è scaduta.",
		"no_mandate_issued":            "Nessuna Card copre questo strumento.",
		"amount_unit_mismatch":         "Era in una valuta diversa da quella della Card.",
		"amount_unit_unverified":       "KIFF non ha potuto verificarne la valuta.",
		"amount_unverified":            "KIFF non ha potuto leggerne l'importo.",
		"halted":                       "L'avevi bloccata.",
		"halt_unavailable":             "KIFF non ha potuto verificare se fosse bloccata.",
	},
	OwnerAsks: map[string]string{
		OwnerAskSealedMoney:  "questo strumento muove denaro su un collegamento di cui KIFF custodisce la chiave, quindi ogni chiamata attende un'approvazione",
		OwnerAskPurchase:     "questo acquista qualcosa, e la sua carta chiede ogni volta, oppure è fuori dai suoi limiti",
		OwnerAskSecondRefund: "questo addebito ha già un rimborso, quindi un secondo attende un'approvazione",
		OwnerAskNoRequestID:  "l'agente non ha indicato nessuna richiesta da un'app collegata, quindi questo rimborso attende un'approvazione",
		OwnerAskOther:        "ogni chiamata di questo strumento attende un'approvazione",
		OwnerAskEachCall:     "ogni chiamata di questo strumento attende un'approvazione",
		OwnerAskSealedFill:   "questo passaggio compila un accesso salvato nel tuo Chrome, quindi ti aspetta ogni volta, e solo il tuo telefono può sbloccarlo",
	},
	ResourceMissing: func(param string) string {
		return fmt.Sprintf("la chiamata non indica alcun %s, e questa Card si applica solo ai %s che elenca", param, param)
	},
	ResourceNotOnCard: func(param, value string) string {
		return fmt.Sprintf("%s %q non è tra quelli coperti da questa Card", param, value)
	},
	OverPerCall: func(draw, limit string) string {
		return fmt.Sprintf("%s supera il limite di %s per chiamata", draw, limit)
	},
	OverTotal: func(draw, limit, used string) string {
		return fmt.Sprintf("%s supererebbe il limite di %s: sono già stati usati %s", draw, limit, used)
	},

	// The held-call and Card-warning emails, and a warning's reason.
	HeldSubject:     "Il tuo agente aspetta la tua risposta",
	HeldNothingDone: "Non è stato fatto nulla.",
	HeldOpen:        "Aprilo in KIFF per rispondere (ti verrà chiesto di accedere):",
	HeldOptions:     "Puoi approvarlo una volta, modificare la Card per consentirlo o rifiutarlo.",
	HeldIntro: func(agent, action, amount string) string {
		return agent + " vuole eseguire " + action + amount + ", che è fuori dalla sua Card."
	},
	HeldSteps: func(steps string) string {
		return "La Card ti permette anche di rifiutarlo con un passo successivo per l'agente: " + steps + "."
	},
	HeldExpires: func(when string) string {
		return "Se nessuno risponde entro il " + when + ", viene rifiutato e l'agente deve chiedere di nuovo."
	},
	HeldNoApprove:   "Da questa email non si può approvare nulla.",
	HeldButtonsNote: "Ogni pulsante apre questa azione in KIFF, dove accedi e confermi la tua risposta.",
	HeldApproveOnce: "Approva una volta",
	HeldChangeCard:  "Modifica la Card per consentirlo",
	HeldReject:      "Rifiuta",
	HeldWants: func(agent string) string {
		return fmt.Sprintf("%s vuole fare qualcosa che esce dalla sua Card:", agent)
	},
	HeldRowAction: "Azione", HeldRowAmount: "Importo", HeldRowWhy: "Perché è trattenuta",
	WarnSpent:  "Questo totale della Card è esaurito.",
	WarnBurst:  "Una nuova Card ha superato la sua soglia di picco all'inizio di questo periodo.",
	WarnRunway: "Al ritmo recente, questo totale della Card si sta esaurendo.",
	WarnUnusual: func(ratio string) string {
		return "L'utilizzo recente è " + ratio + " volte la sua media equivalente su sette giorni."
	},
	WarnUsed:         func(pct, period string) string { return "È usato il " + pct + "% di questo totale " + period + "." },
	Periods:          map[string]string{"calendar_day": "oggi", "rolling_1h": "nell'ultima ora", "rolling_24h": "nelle ultime 24 ore", "rolling_7d": "negli ultimi 7 giorni", "lifetime": "finora"},
	WarnNeverResets:  "Questo totale non si azzera mai. Aggiungi margine, modifica la Card o metti in pausa il suo agente.",
	WarnReviewWindow: "Controlla il margine di questo periodo, modifica la Card o metti in pausa il suo agente.",
	WarnSubject:      "KIFF: avviso di budget di una Card",
	WarnReview:       "Vedi la Card",
}
