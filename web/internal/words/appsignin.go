package words

import "fmt"

// AppSignIn is the lines of the tafs apps' sign-in page (/sign-in/app), the
// page the app's sheet opens on: the form, "Continue as", and why it stopped.
// It speaks the app's language (ui_locales).
type AppSignIn struct {
	// Title heads the page; Lead is under it.
	Title, Lead string
	// ContinueAsF is "Continue as %s", the signed-in email; OtherAccount and
	// Cancel are beside it.
	ContinueAsF, OtherAccount, Cancel string
	// Expired: the request lapsed or was used. Failed: anything else.
	Expired, Failed string
}

// ContinueAs is the button that signs the app in as email.
func (a AppSignIn) ContinueAs(email string) string { return fmt.Sprintf(a.ContinueAsF, email) }

var appSignIn = map[string]AppSignIn{
	"en": {
		Title:        "Sign in to tafs",
		Lead:         "Use the email you signed up with, so you come back to the same account.",
		ContinueAsF:  "Continue as %s",
		OtherAccount: "Use another account",
		Cancel:       "Cancel",
		Expired:      "This sign-in has expired. Start again from the app.",
		Failed:       "We couldn’t sign you in just now. Start again from the app.",
	},
	"de": {
		Title:        "Bei tafs anmelden",
		Lead:         "Nutze die E-Mail-Adresse, mit der du dich registriert hast, damit du zum selben Konto zurückkommst.",
		ContinueAsF:  "Weiter als %s",
		OtherAccount: "Anderes Konto verwenden",
		Cancel:       "Abbrechen",
	},
	"fr": {
		Title:        "Se connecter à tafs",
		Lead:         "Utilisez l’adresse e-mail de votre inscription, pour retrouver le même compte.",
		ContinueAsF:  "Continuer en tant que %s",
		OtherAccount: "Utiliser un autre compte",
		Cancel:       "Annuler",
	},
	"es": {
		Title:        "Iniciar sesión en tafs",
		Lead:         "Usa el correo con el que te registraste, para volver a la misma cuenta.",
		ContinueAsF:  "Continuar como %s",
		OtherAccount: "Usar otra cuenta",
		Cancel:       "Cancelar",
	},
	"it": {
		Title:        "Accedi a tafs",
		Lead:         "Usa l’email usata per la registrazione, così torni allo stesso account.",
		ContinueAsF:  "Continua come %s",
		OtherAccount: "Usa un altro account",
		Cancel:       "Annulla",
	},
	"pt": {
		Title:        "Iniciar sessão no tafs",
		Lead:         "Use o email com que se registou, para voltar à mesma conta.",
		ContinueAsF:  "Continuar como %s",
		OtherAccount: "Usar outra conta",
		Cancel:       "Cancelar",
	},
	"nl": {
		Title:        "Inloggen bij tafs",
		Lead:         "Gebruik het e-mailadres waarmee je je hebt aangemeld, zodat je bij hetzelfde account terugkomt.",
		ContinueAsF:  "Doorgaan als %s",
		OtherAccount: "Ander account gebruiken",
		Cancel:       "Annuleren",
	},
	"pl": {
		Title:        "Zaloguj się do tafs",
		Lead:         "Użyj adresu e-mail, którego użyto przy rejestracji, aby wrócić do tego samego konta.",
		ContinueAsF:  "Kontynuuj jako %s",
		OtherAccount: "Użyj innego konta",
		Cancel:       "Anuluj",
	},
}

// AppSignInIn is the page's lines in lang, English when it has none.
func AppSignInIn(lang string) AppSignIn {
	if a, ok := appSignIn[lang]; ok {
		return a
	}
	return appSignIn["en"]
}
