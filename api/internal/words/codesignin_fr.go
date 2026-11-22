package words

// The code sign-in emails in fr. A draft for the languages' owner to correct.
func init() {
	fr.CodeSignInEmail = CodeSignInEmail{
		CodeSubject:          "Votre code de connexion KIFF",
		CodeIntro:            "Quelqu’un a demandé à se connecter à KIFF avec un code envoyé à cette adresse. Votre code :",
		CodeValid:            "Il fonctionne une fois, pendant 10 minutes, dans le navigateur qui l’a demandé.",
		CodeNeverShare:       "Ne donnez ce code à personne. KIFF ne vous le demandera jamais.",
		CodeNotYou:           "Si ce n’était pas vous, ignorez cet e-mail. Personne ne peut se connecter sans le code.",
		CodeAskedWhen:        "Quand",
		CodeAskedBrowser:     "Navigateur",
		CodeAskedPlace:       "Lieu approximatif",
		CodeOffSubject:       "La connexion par code n’est pas activée sur votre compte KIFF",
		CodeOffIntro:         "Quelqu’un a demandé à se connecter à KIFF avec un code envoyé à cette adresse. La connexion par code n’est pas activée sur votre compte, donc aucun code n’a été envoyé.",
		CodeOffHow:           "Pour l’utiliser, connectez-vous autrement, puis activez « Se connecter avec un code envoyé par e-mail » dans les réglages de connexion de votre compte.",
		CodeOffNotYou:        "Si ce n’était pas vous, ignorez cet e-mail. Rien n’a changé.",
		CodeTurnedOnSubject:  "La connexion par code est maintenant activée sur votre compte KIFF",
		CodeTurnedOnIntro:    "La connexion avec un code envoyé par e-mail a été activée sur votre compte KIFF.",
		CodeTurnedOffSubject: "La connexion par code est maintenant désactivée sur votre compte KIFF",
		CodeTurnedOffIntro:   "La connexion avec un code envoyé par e-mail a été désactivée sur votre compte KIFF.",
		CodeChangedNotYou:    "Si ce n’était pas vous, connectez-vous autrement, vérifiez les réglages de connexion de votre compte et déconnectez les appareils que vous ne connaissez pas.",
		CodeWhy:              "KIFF envoie cet e-mail quand quelqu’un demande à se connecter avec un code envoyé à cette adresse.",
	}
}
