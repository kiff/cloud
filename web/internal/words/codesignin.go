package words

import "fmt"

// CodeSignIn is the lines of signing in with a code sent by email: the link
// under the sign-in form, /sign-in/code (the email, then the code), and the
// setting in Settings › Sign-in. Every language but English is a draft for
// the languages' owner to correct.
type CodeSignIn struct {
	// Link is under the provider's form.
	Link       string
	Title      string
	Lead       string
	EmailLabel string
	Send       string
	// SentF is "%s", the email: the same whatever the address.
	SentF       string
	CodeLabel   string
	Verify      string
	Wrong       string
	SlowDown    string
	Unavailable string
	Lapsed      string
	Again       string
	OtherWays   string
	Finishing   string
	NotLoaded   string
	// The setting's page.
	SettingTitle        string
	SettingLabel        string
	SettingHelp         string
	SettingOn           string
	SettingOff          string
	TurnOn              string
	TurnOff             string
	SettingNotAvailable string
	SettingSaved        string
}

// Sent is the line after asking for a code, the same for every address.
func (c CodeSignIn) Sent(email string) string { return fmt.Sprintf(c.SentF, email) }

var codeSignIn = map[string]CodeSignIn{
	"en": {
		Link:                "Email me a code instead",
		Title:               "Sign in with a code",
		Lead:                "Enter the email you sign in with. If signing in with a code is on for it, we send you a code.",
		EmailLabel:          "Email",
		Send:                "Email me a code",
		SentF:               "If signing in with a code is on for %s, a code is on its way. It works once, for 10 minutes, in this browser.",
		CodeLabel:           "Code",
		Verify:              "Sign in",
		Wrong:               "That code didn’t work. Check it, or ask for a new one.",
		SlowDown:            "Too many tries. Wait a while, then try again.",
		Unavailable:         "We couldn’t sign you in just now. Try again.",
		Lapsed:              "This sign-in has lapsed. Ask for a new code.",
		Again:               "Send a new code",
		OtherWays:           "Sign in another way",
		Finishing:           "Signing you in…",
		NotLoaded:           "The sign-in didn’t finish. Ask for a new code.",
		SettingTitle:        "Sign-in",
		SettingLabel:        "Sign in with a code sent by email",
		SettingHelp:         "When it’s on, you can sign in with a code sent to your email instead of Apple, Google or GitHub. It’s off unless you turn it on, and turning it on needs a recent sign-in.",
		SettingOn:           "On",
		SettingOff:          "Off",
		TurnOn:              "Turn on",
		TurnOff:             "Turn off",
		SettingNotAvailable: "Signing in with a code isn’t available yet.",
		SettingSaved:        "Saved. We emailed you about the change.",
	},
	"de": {
		Link:                "Stattdessen einen Code per E-Mail",
		Title:               "Mit einem Code anmelden",
		Lead:                "Gib die E-Mail-Adresse ein, mit der du dich anmeldest. Wenn die Anmeldung mit Code für sie eingeschaltet ist, senden wir dir einen Code.",
		EmailLabel:          "E-Mail",
		Send:                "Code per E-Mail senden",
		SentF:               "Wenn die Anmeldung mit Code für %s eingeschaltet ist, ist ein Code unterwegs. Er gilt einmal, 10 Minuten lang, in diesem Browser.",
		CodeLabel:           "Code",
		Verify:              "Anmelden",
		Wrong:               "Dieser Code hat nicht funktioniert. Prüfe ihn oder fordere einen neuen an.",
		SlowDown:            "Zu viele Versuche. Warte eine Weile und versuch es dann erneut.",
		Unavailable:         "Die Anmeldung hat gerade nicht geklappt. Versuch es erneut.",
		Lapsed:              "Diese Anmeldung ist abgelaufen. Fordere einen neuen Code an.",
		Again:               "Neuen Code senden",
		OtherWays:           "Anders anmelden",
		Finishing:           "Du wirst angemeldet …",
		NotLoaded:           "Die Anmeldung wurde nicht abgeschlossen. Fordere einen neuen Code an.",
		SettingTitle:        "Anmeldung",
		SettingLabel:        "Mit einem Code per E-Mail anmelden",
		SettingHelp:         "Wenn das eingeschaltet ist, kannst du dich statt mit Apple, Google oder GitHub mit einem Code an deine E-Mail-Adresse anmelden. Es ist aus, bis du es einschaltest, und zum Einschalten brauchst du eine frische Anmeldung.",
		SettingOn:           "Ein",
		SettingOff:          "Aus",
		TurnOn:              "Einschalten",
		TurnOff:             "Ausschalten",
		SettingNotAvailable: "Die Anmeldung mit Code ist noch nicht verfügbar.",
		SettingSaved:        "Gespeichert. Wir haben dir eine E-Mail zur Änderung geschickt.",
	},
	"fr": {
		Link:                "Recevoir plutôt un code par e-mail",
		Title:               "Se connecter avec un code",
		Lead:                "Saisissez l’adresse e-mail de connexion. Si la connexion par code est activée pour elle, nous vous envoyons un code.",
		EmailLabel:          "E-mail",
		Send:                "Recevoir un code par e-mail",
		SentF:               "Si la connexion par code est activée pour %s, un code est en route. Il fonctionne une fois, pendant 10 minutes, dans ce navigateur.",
		CodeLabel:           "Code",
		Verify:              "Se connecter",
		Wrong:               "Ce code n’a pas fonctionné. Vérifiez-le ou demandez-en un nouveau.",
		SlowDown:            "Trop d’essais. Patientez un peu, puis réessayez.",
		Unavailable:         "Nous n’avons pas pu vous connecter pour l’instant. Réessayez.",
		Lapsed:              "Cette connexion a expiré. Demandez un nouveau code.",
		Again:               "Envoyer un nouveau code",
		OtherWays:           "Se connecter autrement",
		Finishing:           "Connexion en cours…",
		NotLoaded:           "La connexion n’a pas abouti. Demandez un nouveau code.",
		SettingTitle:        "Connexion",
		SettingLabel:        "Se connecter avec un code envoyé par e-mail",
		SettingHelp:         "Une fois activée, vous pouvez vous connecter avec un code envoyé à votre adresse e-mail au lieu d’Apple, Google ou GitHub. Elle est désactivée tant que vous ne l’activez pas, et l’activer demande une connexion récente.",
		SettingOn:           "Activée",
		SettingOff:          "Désactivée",
		TurnOn:              "Activer",
		TurnOff:             "Désactiver",
		SettingNotAvailable: "La connexion par code n’est pas encore disponible.",
		SettingSaved:        "Enregistré. Nous vous avons écrit au sujet de ce changement.",
	},
	"es": {
		Link:                "Recibir un código por correo",
		Title:               "Iniciar sesión con un código",
		Lead:                "Escribe el correo con el que inicias sesión. Si el inicio de sesión con código está activado para él, te enviamos un código.",
		EmailLabel:          "Correo",
		Send:                "Enviarme un código",
		SentF:               "Si el inicio de sesión con código está activado para %s, va un código en camino. Funciona una vez, durante 10 minutos, en este navegador.",
		CodeLabel:           "Código",
		Verify:              "Iniciar sesión",
		Wrong:               "Ese código no funcionó. Revísalo o pide uno nuevo.",
		SlowDown:            "Demasiados intentos. Espera un poco y vuelve a intentarlo.",
		Unavailable:         "No pudimos iniciar tu sesión ahora. Inténtalo de nuevo.",
		Lapsed:              "Este inicio de sesión caducó. Pide un código nuevo.",
		Again:               "Enviar un código nuevo",
		OtherWays:           "Iniciar sesión de otra forma",
		Finishing:           "Iniciando sesión…",
		NotLoaded:           "El inicio de sesión no terminó. Pide un código nuevo.",
		SettingTitle:        "Inicio de sesión",
		SettingLabel:        "Iniciar sesión con un código enviado por correo",
		SettingHelp:         "Si está activado, puedes iniciar sesión con un código enviado a tu correo en lugar de Apple, Google o GitHub. Está desactivado hasta que lo actives, y activarlo requiere un inicio de sesión reciente.",
		SettingOn:           "Activado",
		SettingOff:          "Desactivado",
		TurnOn:              "Activar",
		TurnOff:             "Desactivar",
		SettingNotAvailable: "El inicio de sesión con código aún no está disponible.",
		SettingSaved:        "Guardado. Te escribimos sobre el cambio.",
	},
	"it": {
		Link:                "Ricevi invece un codice per email",
		Title:               "Accedi con un codice",
		Lead:                "Inserisci l’email con cui accedi. Se l’accesso con un codice è attivo per essa, ti inviamo un codice.",
		EmailLabel:          "Indirizzo email",
		Send:                "Inviami un codice",
		SentF:               "Se l’accesso con un codice è attivo per %s, un codice è in arrivo. Funziona una volta, per 10 minuti, in questo browser.",
		CodeLabel:           "Codice",
		Verify:              "Accedi",
		Wrong:               "Quel codice non ha funzionato. Controllalo o chiedine uno nuovo.",
		SlowDown:            "Troppi tentativi. Aspetta un po’ e riprova.",
		Unavailable:         "Non siamo riusciti a farti accedere ora. Riprova.",
		Lapsed:              "Questo accesso è scaduto. Chiedi un nuovo codice.",
		Again:               "Invia un nuovo codice",
		OtherWays:           "Accedi in un altro modo",
		Finishing:           "Accesso in corso…",
		NotLoaded:           "L’accesso non si è concluso. Chiedi un nuovo codice.",
		SettingTitle:        "Accesso",
		SettingLabel:        "Accedi con un codice inviato per email",
		SettingHelp:         "Se è attivo, puoi accedere con un codice inviato alla tua email invece che con Apple, Google o GitHub. È disattivato finché non lo attivi, e per attivarlo serve un accesso recente.",
		SettingOn:           "Attivo",
		SettingOff:          "Disattivato",
		TurnOn:              "Attiva",
		TurnOff:             "Disattiva",
		SettingNotAvailable: "L’accesso con un codice non è ancora disponibile.",
		SettingSaved:        "Salvato. Ti abbiamo scritto riguardo alla modifica.",
	},
	"pt": {
		Link:                "Receber antes um código por email",
		Title:               "Iniciar sessão com um código",
		Lead:                "Escreva o email com que inicia sessão. Se o início de sessão com código estiver ativo para ele, enviamos-lhe um código.",
		EmailLabel:          "Endereço de email",
		Send:                "Enviar-me um código",
		SentF:               "Se o início de sessão com código estiver ativo para %s, vai a caminho um código. Funciona uma vez, durante 10 minutos, neste navegador.",
		CodeLabel:           "Código",
		Verify:              "Iniciar sessão",
		Wrong:               "Esse código não funcionou. Verifique-o ou peça um novo.",
		SlowDown:            "Demasiadas tentativas. Aguarde um pouco e tente novamente.",
		Unavailable:         "Não foi possível iniciar a sua sessão agora. Tente novamente.",
		Lapsed:              "Este início de sessão expirou. Peça um novo código.",
		Again:               "Enviar um novo código",
		OtherWays:           "Iniciar sessão de outra forma",
		Finishing:           "A iniciar sessão…",
		NotLoaded:           "O início de sessão não terminou. Peça um novo código.",
		SettingTitle:        "Início de sessão",
		SettingLabel:        "Iniciar sessão com um código enviado por email",
		SettingHelp:         "Quando está ativo, pode iniciar sessão com um código enviado para o seu email em vez de Apple, Google ou GitHub. Está desativado até o ativar, e ativá-lo exige um início de sessão recente.",
		SettingOn:           "Ativo",
		SettingOff:          "Desativado",
		TurnOn:              "Ativar",
		TurnOff:             "Desativar",
		SettingNotAvailable: "O início de sessão com código ainda não está disponível.",
		SettingSaved:        "Guardado. Enviámos-lhe um email sobre a alteração.",
	},
	"nl": {
		Link:                "Liever een code per e-mail",
		Title:               "Inloggen met een code",
		Lead:                "Vul het e-mailadres in waarmee je inlogt. Als inloggen met een code ervoor aanstaat, sturen we je een code.",
		EmailLabel:          "E-mail",
		Send:                "Stuur me een code",
		SentF:               "Als inloggen met een code aanstaat voor %s, is er een code onderweg. Hij werkt één keer, 10 minuten lang, in deze browser.",
		CodeLabel:           "Code",
		Verify:              "Inloggen",
		Wrong:               "Die code werkte niet. Controleer hem of vraag een nieuwe aan.",
		SlowDown:            "Te veel pogingen. Wacht even en probeer het dan opnieuw.",
		Unavailable:         "We konden je nu niet inloggen. Probeer het opnieuw.",
		Lapsed:              "Deze inlog is verlopen. Vraag een nieuwe code aan.",
		Again:               "Stuur een nieuwe code",
		OtherWays:           "Op een andere manier inloggen",
		Finishing:           "Je wordt ingelogd…",
		NotLoaded:           "Het inloggen is niet afgerond. Vraag een nieuwe code aan.",
		SettingTitle:        "Inloggen",
		SettingLabel:        "Inloggen met een code per e-mail",
		SettingHelp:         "Als dit aanstaat, kun je inloggen met een code naar je e-mail in plaats van met Apple, Google of GitHub. Het staat uit tot je het aanzet, en aanzetten vraagt om een recente inlog.",
		SettingOn:           "Aan",
		SettingOff:          "Uit",
		TurnOn:              "Aanzetten",
		TurnOff:             "Uitzetten",
		SettingNotAvailable: "Inloggen met een code is nog niet beschikbaar.",
		SettingSaved:        "Opgeslagen. We hebben je over de wijziging gemaild.",
	},
	"pl": {
		Link:                "Zamiast tego wyślij mi kod e-mailem",
		Title:               "Zaloguj się kodem",
		Lead:                "Wpisz adres e-mail, którym się logujesz. Jeśli logowanie kodem jest dla niego włączone, wyślemy Ci kod.",
		EmailLabel:          "E-mail",
		Send:                "Wyślij mi kod",
		SentF:               "Jeśli logowanie kodem jest włączone dla %s, kod jest już w drodze. Działa raz, przez 10 minut, w tej przeglądarce.",
		CodeLabel:           "Kod",
		Verify:              "Zaloguj się",
		Wrong:               "Ten kod nie zadziałał. Sprawdź go albo poproś o nowy.",
		SlowDown:            "Zbyt wiele prób. Odczekaj chwilę i spróbuj ponownie.",
		Unavailable:         "Nie udało się Cię teraz zalogować. Spróbuj ponownie.",
		Lapsed:              "To logowanie wygasło. Poproś o nowy kod.",
		Again:               "Wyślij nowy kod",
		OtherWays:           "Zaloguj się inaczej",
		Finishing:           "Logowanie…",
		NotLoaded:           "Logowanie nie zostało ukończone. Poproś o nowy kod.",
		SettingTitle:        "Logowanie",
		SettingLabel:        "Logowanie kodem wysłanym e-mailem",
		SettingHelp:         "Gdy jest włączone, możesz zalogować się kodem wysłanym na Twój e-mail zamiast przez Apple, Google lub GitHub. Jest wyłączone, dopóki go nie włączysz, a włączenie wymaga niedawnego logowania.",
		SettingOn:           "Włączone",
		SettingOff:          "Wyłączone",
		TurnOn:              "Włącz",
		TurnOff:             "Wyłącz",
		SettingNotAvailable: "Logowanie kodem nie jest jeszcze dostępne.",
		SettingSaved:        "Zapisano. Wysłaliśmy Ci e-mail o tej zmianie.",
	},
}

// CodeSignInIn is the lines in lang, English when it has none.
func CodeSignInIn(lang string) CodeSignIn {
	if c, ok := codeSignIn[lang]; ok {
		return c
	}
	return codeSignIn["en"]
}
