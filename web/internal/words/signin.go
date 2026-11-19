package words

// SignIn is the sign-in callback's lines (/sign-in/callback): the page
// that hands the browser's Clerk session to KIFF, and the page that stops
// a redirect loop. A tafs app's sign-in passes through it, so it speaks the
// app's language. English is what the pages said before they were
// translated, except that the script's own errors don't name the sign-in
// vendor. The script's errors are written into the page as
// JSON, so they stay plain text.
type SignIn struct {
	// Title, Signing, Failed and TryAgain are the hand-off page.
	Title, Signing, Failed, TryAgain string
	// NoSDK, NoSession and NoToken are the hand-off's own errors.
	NoSDK, NoSession, NoToken string
	// LoopTitle, LoopHead, LoopBody and LoopButton are the page that stops
	// the loop; LoopHintBefore and LoopHintAfter frame its list of hosts.
	LoopTitle, LoopHead, LoopBody, LoopButton string
	LoopHintBefore, LoopHintAfter             string
}

var signIn = map[string]SignIn{
	"en": {
		Title: "Signing in…", Signing: "Signing you in…", Failed: "Sign-in failed:", TryAgain: "Try again",
		NoSDK:     "The sign-in service didn’t load. Ad blocker?",
		NoSession: "No active sign-in session. Please sign in again.",
		NoToken:   "The sign-in service didn’t return a session token.",
		LoopTitle: "Sign-in interrupted", LoopHead: "We couldn’t complete sign-in",
		LoopBody:       "Your previous sign-in session looks stale, so the sign-in step kept restarting. We stopped it so your browser isn’t stuck in a redirect loop.",
		LoopButton:     "Try signing in again",
		LoopHintBefore: "If it keeps happening, clear cookies for this site", LoopHintAfter: "and sign in once more.",
	},
	"de": {
		Title: "Anmeldung…", Signing: "Du wirst angemeldet…", Failed: "Anmeldung fehlgeschlagen:", TryAgain: "Erneut versuchen",
		NoSDK:     "Der Anmeldedienst wurde nicht geladen. Ein Werbeblocker?",
		NoSession: "Keine aktive Anmeldesitzung. Bitte melde dich erneut an.",
		NoToken:   "Der Anmeldedienst hat kein Sitzungstoken zurückgegeben.",
		LoopTitle: "Anmeldung unterbrochen", LoopHead: "Wir konnten die Anmeldung nicht abschließen",
		LoopBody:       "Deine vorherige Anmeldesitzung scheint veraltet zu sein, deshalb hat der Anmeldeschritt immer wieder neu begonnen. Wir haben ihn gestoppt, damit dein Browser nicht in einer Weiterleitungsschleife hängt.",
		LoopButton:     "Erneut anmelden",
		LoopHintBefore: "Wenn es wieder passiert, lösche die Cookies dieser Website", LoopHintAfter: "und melde dich noch einmal an.",
	},
	"fr": {
		Title: "Connexion…", Signing: "Connexion en cours…", Failed: "Échec de la connexion :", TryAgain: "Réessayer",
		NoSDK:     "Le service de connexion ne s’est pas chargé. Un bloqueur de publicité ?",
		NoSession: "Aucune session de connexion active. Veuillez vous reconnecter.",
		NoToken:   "Le service de connexion n’a pas renvoyé de jeton de session.",
		LoopTitle: "Connexion interrompue", LoopHead: "Nous n’avons pas pu terminer la connexion",
		LoopBody:       "Votre session de connexion précédente semble périmée, l’étape de connexion recommençait donc sans cesse. Nous l’avons arrêtée pour que votre navigateur ne reste pas bloqué dans une boucle de redirection.",
		LoopButton:     "Réessayer de vous connecter",
		LoopHintBefore: "Si cela se reproduit, effacez les cookies de ce site", LoopHintAfter: "et connectez-vous à nouveau.",
	},
	"es": {
		Title: "Iniciando sesión…", Signing: "Iniciando tu sesión…", Failed: "No se pudo iniciar sesión:", TryAgain: "Reintentar",
		NoSDK:     "El servicio de inicio de sesión no se cargó. ¿Un bloqueador de anuncios?",
		NoSession: "No hay ninguna sesión activa. Vuelve a iniciar sesión.",
		NoToken:   "El servicio de inicio de sesión no devolvió un token de sesión.",
		LoopTitle: "Inicio de sesión interrumpido", LoopHead: "No pudimos completar el inicio de sesión",
		LoopBody:       "Tu sesión anterior parece caducada, así que el paso de inicio de sesión se reiniciaba una y otra vez. Lo detuvimos para que tu navegador no se quede atrapado en un bucle de redirecciones.",
		LoopButton:     "Volver a iniciar sesión",
		LoopHintBefore: "Si sigue pasando, borra las cookies de este sitio", LoopHintAfter: "y vuelve a iniciar sesión.",
	},
	"it": {
		Title: "Accesso…", Signing: "Accesso in corso…", Failed: "Accesso non riuscito:", TryAgain: "Riprova",
		NoSDK:     "Il servizio di accesso non si è caricato. Un blocco annunci?",
		NoSession: "Nessuna sessione di accesso attiva. Accedi di nuovo.",
		NoToken:   "Il servizio di accesso non ha restituito un token di sessione.",
		LoopTitle: "Accesso interrotto", LoopHead: "Non siamo riusciti a completare l’accesso",
		LoopBody:       "La tua sessione di accesso precedente sembra scaduta, quindi il passaggio di accesso ricominciava di continuo. L’abbiamo fermato perché il tuo browser non resti bloccato in un ciclo di reindirizzamenti.",
		LoopButton:     "Riprova ad accedere",
		LoopHintBefore: "Se continua a succedere, cancella i cookie di questo sito", LoopHintAfter: "e accedi ancora una volta.",
	},
	"pt": {
		Title: "A iniciar sessão…", Signing: "A iniciar a sua sessão…", Failed: "Não foi possível iniciar sessão:", TryAgain: "Tentar novamente",
		NoSDK:     "O serviço de início de sessão não carregou. Um bloqueador de anúncios?",
		NoSession: "Não há nenhuma sessão ativa. Inicie sessão novamente.",
		NoToken:   "O serviço de início de sessão não devolveu um token de sessão.",
		LoopTitle: "Início de sessão interrompido", LoopHead: "Não conseguimos concluir o início de sessão",
		LoopBody:       "A sua sessão anterior parece expirada, por isso o passo de início de sessão recomeçava sem parar. Interrompemo-lo para que o seu navegador não fique preso num ciclo de redirecionamentos.",
		LoopButton:     "Tentar iniciar sessão novamente",
		LoopHintBefore: "Se continuar a acontecer, apague os cookies deste site", LoopHintAfter: "e inicie sessão mais uma vez.",
	},
	"nl": {
		Title: "Inloggen…", Signing: "Je wordt ingelogd…", Failed: "Inloggen mislukt:", TryAgain: "Opnieuw proberen",
		NoSDK:     "De inlogdienst is niet geladen. Een advertentieblokker?",
		NoSession: "Geen actieve inlogsessie. Log opnieuw in.",
		NoToken:   "De inlogdienst gaf geen sessietoken terug.",
		LoopTitle: "Inloggen onderbroken", LoopHead: "We konden het inloggen niet afronden",
		LoopBody:       "Je vorige inlogsessie lijkt verlopen, waardoor de inlogstap steeds opnieuw begon. We hebben die gestopt, zodat je browser niet vastzit in een doorverwijzingslus.",
		LoopButton:     "Opnieuw inloggen",
		LoopHintBefore: "Gebeurt het steeds weer, wis dan de cookies voor deze site", LoopHintAfter: "en log nog een keer in.",
	},
	"pl": {
		Title: "Logowanie…", Signing: "Trwa logowanie…", Failed: "Logowanie nie powiodło się:", TryAgain: "Spróbuj ponownie",
		NoSDK:     "Usługa logowania się nie wczytała. Może bloker reklam?",
		NoSession: "Brak aktywnej sesji logowania. Zaloguj się ponownie.",
		NoToken:   "Usługa logowania nie zwróciła tokenu sesji.",
		LoopTitle: "Logowanie przerwane", LoopHead: "Nie udało się dokończyć logowania",
		LoopBody:       "Twoja poprzednia sesja logowania wygląda na nieaktualną, więc krok logowania wciąż zaczynał się od nowa. Zatrzymaliśmy go, żeby Twoja przeglądarka nie utknęła w pętli przekierowań.",
		LoopButton:     "Zaloguj się ponownie",
		LoopHintBefore: "Jeśli to się powtarza, wyczyść pliki cookie tej witryny", LoopHintAfter: "i zaloguj się jeszcze raz.",
	},
}

// SignInIn is the sign-in callback's lines in lang, or English.
func SignInIn(lang string) SignIn {
	if s, ok := signIn[lang]; ok {
		return s
	}
	return signIn["en"]
}
