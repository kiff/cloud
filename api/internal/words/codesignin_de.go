package words

// The code sign-in emails in de. A draft for the languages' owner to correct.
func init() {
	de.CodeSignInEmail = CodeSignInEmail{
		CodeSubject:          "Dein Anmeldecode für KIFF",
		CodeIntro:            "Jemand möchte sich mit einem Code, der an diese Adresse geht, bei KIFF anmelden. Dein Code:",
		CodeValid:            "Er gilt einmal, 10 Minuten lang, in dem Browser, der ihn angefordert hat.",
		CodeNeverShare:       "Gib diesen Code niemandem weiter. KIFF fragt dich nie danach.",
		CodeNotYou:           "Wenn du das nicht warst, ignoriere diese E-Mail. Ohne den Code kann sich niemand anmelden.",
		CodeAskedWhen:        "Wann",
		CodeAskedBrowser:     "Webbrowser",
		CodeAskedPlace:       "Ungefährer Ort",
		CodeOffSubject:       "Die Anmeldung mit Code ist für dein KIFF-Konto nicht eingeschaltet",
		CodeOffIntro:         "Jemand möchte sich mit einem Code, der an diese Adresse geht, bei KIFF anmelden. Die Anmeldung mit Code ist für dein Konto nicht eingeschaltet, deshalb wurde kein Code gesendet.",
		CodeOffHow:           "Um sie zu nutzen, melde dich anders an und schalte dann in den Anmeldeeinstellungen deines Kontos „Mit einem Code per E-Mail anmelden“ ein.",
		CodeOffNotYou:        "Wenn du das nicht warst, ignoriere diese E-Mail. Es hat sich nichts geändert.",
		CodeTurnedOnSubject:  "Die Anmeldung mit Code ist für dein KIFF-Konto jetzt eingeschaltet",
		CodeTurnedOnIntro:    "Die Anmeldung mit einem Code per E-Mail wurde für dein KIFF-Konto eingeschaltet.",
		CodeTurnedOffSubject: "Die Anmeldung mit Code ist für dein KIFF-Konto jetzt ausgeschaltet",
		CodeTurnedOffIntro:   "Die Anmeldung mit einem Code per E-Mail wurde für dein KIFF-Konto ausgeschaltet.",
		CodeChangedNotYou:    "Wenn du das nicht warst, melde dich anders an, prüfe die Anmeldeeinstellungen deines Kontos und melde Geräte ab, die du nicht kennst.",
		CodeWhy:              "KIFF sendet diese E-Mail, wenn jemand eine Anmeldung mit einem Code an diese Adresse anfordert.",
	}
}
