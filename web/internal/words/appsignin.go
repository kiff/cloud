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
	// Expired: the request lapsed or was used. Failed: anything else. Off:
	// signing in inside the app isn't on.
	Expired, Failed, Off string
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
		Off:          "Signing in inside the app isn’t on yet. Sign in from the app as before.",
	},
	"de": {
		Title:        "Bei tafs anmelden",
		Lead:         "Nutze die E-Mail-Adresse, mit der du dich registriert hast, damit du zum selben Konto zurückkommst.",
		ContinueAsF:  "Weiter als %s",
		OtherAccount: "Anderes Konto verwenden",
		Cancel:       "Abbrechen",
		Expired:      "Diese Anmeldung ist abgelaufen. Fang in der App neu an.",
		Failed:       "Die Anmeldung hat gerade nicht geklappt. Fang in der App neu an.",
		Off:          "Die Anmeldung in der App ist noch nicht eingeschaltet. Melde dich wie bisher über die App an.",
	},
	"fr": {
		Title:        "Se connecter à tafs",
		Lead:         "Utilisez l’adresse e-mail de votre inscription, pour retrouver le même compte.",
		ContinueAsF:  "Continuer en tant que %s",
		OtherAccount: "Utiliser un autre compte",
		Cancel:       "Annuler",
		Expired:      "Cette connexion a expiré. Recommencez depuis l’app.",
		Failed:       "Nous n’avons pas pu vous connecter pour le moment. Recommencez depuis l’app.",
		Off:          "La connexion dans l’app n’est pas encore activée. Connectez-vous depuis l’app comme avant.",
	},
	"es": {
		Title:        "Iniciar sesión en tafs",
		Lead:         "Usa el correo con el que te registraste, para volver a la misma cuenta.",
		ContinueAsF:  "Continuar como %s",
		OtherAccount: "Usar otra cuenta",
		Cancel:       "Cancelar",
		Expired:      "Este inicio de sesión ha caducado. Vuelve a empezar desde la app.",
		Failed:       "No hemos podido iniciar tu sesión ahora mismo. Vuelve a empezar desde la app.",
		Off:          "Iniciar sesión dentro de la app aún no está activado. Inicia sesión desde la app como antes.",
	},
	"it": {
		Title:        "Accedi a tafs",
		Lead:         "Usa l’email usata per la registrazione, così torni allo stesso account.",
		ContinueAsF:  "Continua come %s",
		OtherAccount: "Usa un altro account",
		Cancel:       "Annulla",
		Expired:      "Questo accesso è scaduto. Ricomincia dall’app.",
		Failed:       "Non siamo riusciti a farti accedere in questo momento. Ricomincia dall’app.",
		Off:          "L’accesso dentro l’app non è ancora attivo. Accedi dall’app come prima.",
	},
	"pt": {
		Title:        "Iniciar sessão no tafs",
		Lead:         "Use o email com que se registou, para voltar à mesma conta.",
		ContinueAsF:  "Continuar como %s",
		OtherAccount: "Usar outra conta",
		Cancel:       "Cancelar",
		Expired:      "Este início de sessão expirou. Comece de novo a partir da app.",
		Failed:       "Não conseguimos iniciar a sua sessão agora. Comece de novo a partir da app.",
		Off:          "O início de sessão dentro da app ainda não está ativo. Inicie sessão a partir da app como antes.",
	},
	"nl": {
		Title:        "Inloggen bij tafs",
		Lead:         "Gebruik het e-mailadres waarmee je je hebt aangemeld, zodat je bij hetzelfde account terugkomt.",
		ContinueAsF:  "Doorgaan als %s",
		OtherAccount: "Ander account gebruiken",
		Cancel:       "Annuleren",
		Expired:      "Deze inlogpoging is verlopen. Begin opnieuw vanuit de app.",
		Failed:       "Inloggen lukte nu niet. Begin opnieuw vanuit de app.",
		Off:          "Inloggen in de app staat nog niet aan. Log zoals eerder in vanuit de app.",
	},
	"pl": {
		Title:        "Zaloguj się do tafs",
		Lead:         "Użyj adresu e-mail, którego użyto przy rejestracji, aby wrócić do tego samego konta.",
		ContinueAsF:  "Kontynuuj jako %s",
		OtherAccount: "Użyj innego konta",
		Cancel:       "Anuluj",
		Expired:      "To logowanie wygasło. Zacznij od nowa w aplikacji.",
		Failed:       "Nie udało się teraz Cię zalogować. Zacznij od nowa w aplikacji.",
		Off:          "Logowanie w aplikacji nie jest jeszcze włączone. Zaloguj się z aplikacji tak jak dotąd.",
	},
}

// AppSignInIn is the page's lines in lang, English when it has none.
func AppSignInIn(lang string) AppSignIn {
	if a, ok := appSignIn[lang]; ok {
		return a
	}
	return appSignIn["en"]
}
