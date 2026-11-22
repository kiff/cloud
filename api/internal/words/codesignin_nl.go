package words

// The code sign-in emails in nl. A draft for the languages' owner to correct.
func init() {
	nl.CodeSignInEmail = CodeSignInEmail{
		CodeSubject:          "Je inlogcode voor KIFF",
		CodeIntro:            "Iemand wil inloggen bij KIFF met een code die naar dit adres wordt gestuurd. Je code:",
		CodeValid:            "Hij werkt één keer, 10 minuten lang, in de browser die erom vroeg.",
		CodeNeverShare:       "Geef deze code aan niemand. KIFF vraagt er nooit om.",
		CodeNotYou:           "Was jij dit niet? Negeer deze e-mail dan. Zonder de code kan niemand inloggen.",
		CodeAskedWhen:        "Wanneer",
		CodeAskedBrowser:     "Webbrowser",
		CodeAskedPlace:       "Geschatte locatie",
		CodeOffSubject:       "Inloggen met een code staat niet aan voor je KIFF-account",
		CodeOffIntro:         "Iemand wil inloggen bij KIFF met een code die naar dit adres wordt gestuurd. Inloggen met een code staat niet aan voor je account, dus er is geen code gestuurd.",
		CodeOffHow:           "Om het te gebruiken, log je op een andere manier in en zet je ‘Inloggen met een code per e-mail’ aan in de inloginstellingen van je account.",
		CodeOffNotYou:        "Was jij dit niet? Negeer deze e-mail dan. Er is niets veranderd.",
		CodeTurnedOnSubject:  "Inloggen met een code staat nu aan voor je KIFF-account",
		CodeTurnedOnIntro:    "Inloggen met een code per e-mail is aangezet voor je KIFF-account.",
		CodeTurnedOffSubject: "Inloggen met een code staat nu uit voor je KIFF-account",
		CodeTurnedOffIntro:   "Inloggen met een code per e-mail is uitgezet voor je KIFF-account.",
		CodeChangedNotYou:    "Was jij dit niet? Log dan op een andere manier in, controleer de inloginstellingen van je account en log apparaten uit die je niet kent.",
		CodeWhy:              "KIFF stuurt deze e-mail wanneer iemand vraagt om in te loggen met een code naar dit adres.",
	}
}
