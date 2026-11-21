package pages

import (
	"html"
	"reflect"
	"strings"
	"testing"
)

// watchLanguages are /tafs/watch's words by language.
var watchLanguages = map[string]watchWords{
	"en": watchEN, "de": watchDE, "fr": watchFR, "es": watchES,
	"it": watchIT, "pt": watchPT, "nl": watchNL, "pl": watchPL,
}

// Every language fills every field of /tafs/watch that English fills, with
// as many items in each list, and its bold spans closed.
func TestEveryLanguageFillsTheWatchPage(t *testing.T) {
	var check func(path string, en, tr reflect.Value)
	check = func(path string, en, tr reflect.Value) {
		switch en.Kind() {
		case reflect.Struct:
			for i := 0; i < en.NumField(); i++ {
				check(path+"."+en.Type().Field(i).Name, en.Field(i), tr.Field(i))
			}
		case reflect.Slice:
			if en.Len() != tr.Len() {
				t.Errorf("%s: %d items, English has %d", path, tr.Len(), en.Len())
				return
			}
			for i := 0; i < en.Len(); i++ {
				check(path+"["+string(rune('0'+i))+"]", en.Index(i), tr.Index(i))
			}
		case reflect.String:
			s := tr.String()
			if en.String() != "" && strings.TrimSpace(s) == "" {
				t.Errorf("%s is empty", path)
			}
			if strings.Count(s, "**")%2 != 0 {
				t.Errorf("%s leaves a ** open: %q", path, s)
			}
		}
	}
	for lang, w := range watchLanguages {
		if w.Lang != lang {
			t.Errorf("%s: Lang is %q", lang, w.Lang)
		}
		check(lang, reflect.ValueOf(watchEN), reflect.ValueOf(w))
	}
	for _, lang := range languagesWith("/tafs/watch") {
		if _, ok := watchLanguages[lang]; !ok {
			t.Errorf("%s has no words for /tafs/watch", lang)
		}
	}
}

// The page names what the watch shows in the app's own words: these are the
// tafs app's strings, verbatim, in each language (app/strings/<lang>/:
// watch.approve_on_phone, needs_lock, refused, already_answered,
// voice_text_only, phone_not_reachable, sign_in_on_phone, open_on_phone;
// alert.needs_you; needs_you.title and refuse; home.your_tafs; halt.now and
// all_now; coach.ask; common.send and cancel). A change to the app's words
// fails here until the page follows.
var watchAppWords = map[string][]string{
	"en": {
		"Approve on your phone",
		"To refuse or halt from this watch, set a screen lock on it and unlock it.",
		"Refused. Nothing was sent.",
		"Already answered, or it ended.",
		"Coach gets your words as text, never your voice.",
		"Your phone isn't in reach. Bring it closer, then try again.",
		"Sign in to tafs on your phone first.",
		"Open on phone",
		"A taf needs you",
		"Needs you",
		"Refuse",
		"Your tafs",
		"Halt now",
		"Halt all now",
		"Ask Coach",
		"Send",
		"Cancel",
	},
	"de": {
		"Genehmige auf deinem Handy",
		"Um mit dieser Uhr abzulehnen oder anzuhalten, richte eine Displaysperre ein und entsperre sie.",
		"Abgelehnt. Nichts wurde gesendet.",
		"Schon beantwortet oder beendet.",
		"Coach bekommt deine Worte als Text, nie deine Stimme.",
		"Dein Handy ist nicht in Reichweite. Bring es näher und versuch es dann erneut.",
		"Melde dich zuerst auf deinem Handy bei tafs an.",
		"Auf dem Handy öffnen",
		"Ein taf braucht dich",
		"Braucht dich",
		"Ablehnen",
		"Deine tafs",
		"Jetzt anhalten",
		"Alle jetzt anhalten",
		"Coach fragen",
		"Senden",
		"Abbrechen",
	},
	"fr": {
		"Approuvez sur votre téléphone",
		"Pour refuser ou bloquer depuis cette montre, définissez un verrouillage de l'écran et déverrouillez-la.",
		"Refusé. Rien n'a été envoyé.",
		"Déjà répondu, ou terminé.",
		"Coach reçoit vos mots en texte, jamais votre voix.",
		"Votre téléphone est hors de portée. Rapprochez-le, puis réessayez.",
		"Connectez-vous d'abord à tafs sur votre téléphone.",
		"Ouvrir sur le téléphone",
		"Un taf vous attend",
		"Vous attend",
		"Refuser",
		"Vos tafs",
		"Bloquer maintenant",
		"Tout bloquer maintenant",
		"Demander à Coach",
		"Envoyer",
		"Annuler",
	},
	"es": {
		"Aprueba en tu teléfono",
		"Para rechazar o bloquear desde este reloj, pon un bloqueo de pantalla y desbloquéalo.",
		"Rechazada. No se envió nada.",
		"Ya respondida, o terminada.",
		"Coach recibe tus palabras como texto, nunca tu voz.",
		"Tu teléfono no está al alcance. Acércalo y vuelve a intentarlo.",
		"Primero inicia sesión en tafs en tu teléfono.",
		"Abrir en el teléfono",
		"Un taf te necesita",
		"Te necesita",
		"Rechazar",
		"Tus tafs",
		"Bloquear ahora",
		"Bloquear todo ahora",
		"Preguntar a Coach",
		"Enviar",
		"Cancelar",
	},
	"it": {
		"Approva sul telefono",
		"Per rifiutare o bloccare da questo orologio, imposta un blocco schermo e sbloccalo.",
		"Rifiutata. Non è stato inviato nulla.",
		"Ha già una risposta, o è terminata.",
		"Coach riceve le tue parole come testo, mai la tua voce.",
		"Il telefono non è raggiungibile. Avvicinalo, poi riprova.",
		"Prima accedi a tafs sul telefono.",
		"Apri sul telefono",
		"Un taf ti aspetta",
		"Ti aspetta",
		"Rifiuta",
		"I tuoi tafs",
		"Blocca ora",
		"Blocca tutto ora",
		"Chiedi a Coach",
		"Invia",
		"Annulla",
	},
	"pt": {
		"Aprove no telemóvel",
		"Para recusar ou bloquear a partir deste relógio, defina um bloqueio de ecrã e desbloqueie-o.",
		"Recusado. Nada foi enviado.",
		"Já respondido, ou terminado.",
		"O Coach recebe as suas palavras como texto, nunca a sua voz.",
		"O telemóvel não está ao alcance. Aproxime-o e tente novamente.",
		"Primeiro, inicie sessão no tafs no telemóvel.",
		"Abrir no telemóvel",
		"Um taf precisa de si",
		"Precisa de si",
		"Recusar",
		"Os seus tafs",
		"Bloquear agora",
		"Bloquear tudo agora",
		"Perguntar ao Coach",
		"Enviar",
		"Cancelar",
	},
	"nl": {
		"Keur goed op je telefoon",
		"Stel een schermvergrendeling in op dit horloge en ontgrendel het om vanaf hier te weigeren of stil te zetten.",
		"Geweigerd. Er is niets verstuurd.",
		"Al beantwoord, of beëindigd.",
		"Coach krijgt je woorden als tekst, nooit je stem.",
		"Je telefoon is buiten bereik. Breng hem dichterbij en probeer het opnieuw.",
		"Log eerst in bij tafs op je telefoon.",
		"Openen op telefoon",
		"Een taf wacht op jou",
		"Wacht op jou",
		"Weigeren",
		"Je tafs",
		"Nu stilzetten",
		"Alles nu stilzetten",
		"Coach vragen",
		"Verzenden",
		"Annuleren",
	},
	"pl": {
		"Zatwierdź na telefonie",
		"Aby odrzucać lub blokować z tego zegarka, ustaw blokadę ekranu i odblokuj go.",
		"Odrzucono. Nic nie zostało wysłane.",
		"Już odpowiedziano albo zakończono.",
		"Coach dostaje Twoje słowa jako tekst, nigdy Twój głos.",
		"Telefon jest poza zasięgiem. Przybliż go i spróbuj ponownie.",
		"Najpierw zaloguj się do tafs na telefonie.",
		"Otwórz na telefonie",
		"Taf czeka na Ciebie",
		"Czeka na Ciebie",
		"Odrzuć",
		"Twoje tafs",
		"Zablokuj teraz",
		"Zablokuj wszystko teraz",
		"Zapytaj Coacha",
		"Wyślij",
		"Anuluj",
	},
}

func TestTheWatchPageQuotesTheWatch(t *testing.T) {
	// The page as it reads once live (KIFF_WATCH_LIVE=on); held back by default, so turned on here, alone, and back off.
	HoldWatch(true)
	defer HoldWatch(false)
	for lang, want := range watchAppWords {
		text := html.UnescapeString(anyTagRE.ReplaceAllString(mainOf(renderPage(t, lang, "/tafs/watch")), ""))
		for _, s := range want {
			if !strings.Contains(text, s) {
				t.Errorf("%s: /tafs/watch doesn't say %q", lang, s)
			}
		}
	}
}

// What the watch can't do yet is said, and nothing on the page approves.
func TestTheWatchPageSaysWhatIsComing(t *testing.T) {
	text := html.UnescapeString(anyTagRE.ReplaceAllString(mainOf(renderPage(t, "en", "/tafs/watch")), ""))
	for _, want := range []string{"Wear OS: Coming to Google Play.", "Apple Watch: Coming.", "Approving is never done on the watch."} {
		if !strings.Contains(text, want) {
			t.Errorf("/tafs/watch doesn't say %q", want)
		}
	}
	if strings.Contains(text, "Get it on Google Play") || strings.Contains(text, "play.google.com") {
		t.Errorf("/tafs/watch links a listing that doesn't exist yet")
	}
}
