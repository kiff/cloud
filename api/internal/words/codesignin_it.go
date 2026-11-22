package words

// The code sign-in emails in it. A draft for the languages' owner to correct.
func init() {
	it.CodeSignInEmail = CodeSignInEmail{
		CodeSubject:          "Il tuo codice di accesso a KIFF",
		CodeIntro:            "Qualcuno ha chiesto di accedere a KIFF con un codice inviato a questo indirizzo. Il tuo codice:",
		CodeValid:            "Funziona una volta, per 10 minuti, nel browser che l’ha chiesto.",
		CodeNeverShare:       "Non dare questo codice a nessuno. KIFF non te lo chiederà mai.",
		CodeNotYou:           "Se non sei stato tu, ignora questa email. Nessuno può accedere senza il codice.",
		CodeAskedWhen:        "Quando",
		CodeAskedBrowser:     "Programma di navigazione",
		CodeAskedPlace:       "Posizione approssimativa",
		CodeOffSubject:       "L’accesso con un codice non è attivo per il tuo account KIFF",
		CodeOffIntro:         "Qualcuno ha chiesto di accedere a KIFF con un codice inviato a questo indirizzo. L’accesso con un codice non è attivo per il tuo account, quindi non è stato inviato nessun codice.",
		CodeOffHow:           "Per usarlo, accedi in un altro modo, poi attiva «Accedi con un codice inviato per email» nelle impostazioni di accesso del tuo account.",
		CodeOffNotYou:        "Se non sei stato tu, ignora questa email. Non è cambiato nulla.",
		CodeTurnedOnSubject:  "L’accesso con un codice ora è attivo per il tuo account KIFF",
		CodeTurnedOnIntro:    "L’accesso con un codice inviato per email è stato attivato per il tuo account KIFF.",
		CodeTurnedOffSubject: "L’accesso con un codice ora è disattivato per il tuo account KIFF",
		CodeTurnedOffIntro:   "L’accesso con un codice inviato per email è stato disattivato per il tuo account KIFF.",
		CodeChangedNotYou:    "Se non sei stato tu, accedi in un altro modo, controlla le impostazioni di accesso del tuo account e disconnetti i dispositivi che non conosci.",
		CodeWhy:              "KIFF invia questa email quando qualcuno chiede di accedere con un codice inviato a questo indirizzo.",
	}
}
