package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// §8 (How long we keep it) names Coach's short notes and how long they're
// kept (until Coach's next answer takes them, at most 7 days, then as part of
// that chat; a 24-hour copy for a resent message), in every language.
func TestTafsPrivacyRetentionNamesCoachNotes(t *testing.T) {
	pages := map[string]struct {
		page templ.Component
		line string
	}{
		"en": {TafsPrivacy(), "for as long as the chat is kept. A copy is kept 24 hours so that a resent message gets the same note. A chat's note is only used in that chat."},
		"de": {TafsPrivacyDE(), "so lange der Chat aufbewahrt wird. Eine Kopie bleibt 24 Stunden, damit eine erneut gesendete Nachricht dieselbe Notiz erhält. Die Notiz eines Chats wird nur in diesem Chat genutzt."},
		"es": {TafsPrivacyES(), "mientras se conserve el chat. Una copia se guarda 24 horas para que un mensaje reenviado reciba la misma nota. La nota de un chat solo se usa en ese chat."},
		"fr": {TafsPrivacyFR(), "aussi longtemps que le chat est conservé. Une copie est gardée 24 heures pour qu'un message renvoyé reçoive la même note. La note d'un chat ne sert que dans ce chat."},
		"it": {TafsPrivacyIT(), "finché la chat è conservata. Una copia resta 24 ore perché un messaggio inviato di nuovo riceva la stessa nota. La nota di una chat si usa solo in quella chat."},
		"nl": {TafsPrivacyNL(), "zolang de chat wordt bewaard. Een kopie blijft 24 uur, zodat een opnieuw verzonden bericht dezelfde notitie krijgt. De notitie van een chat wordt alleen in die chat gebruikt."},
		"pl": {TafsPrivacyPL(), "tak długo, jak przechowywany jest czat. Kopia zostaje na 24 godziny, aby ponownie wysłana wiadomość otrzymała tę samą notatkę. Notatka z czatu jest używana tylko w tym czacie."},
		"pt": {TafsPrivacyPT(), "enquanto o chat for guardado. Uma cópia fica 24 horas para que uma mensagem reenviada receba a mesma nota. A nota de um chat só é usada nesse chat."},
	}
	for lang, p := range pages {
		var b bytes.Buffer
		if err := p.page.Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		s := strings.ReplaceAll(b.String(), "&#39;", "'")
		start := strings.Index(s, `id="retention"`)
		if start < 0 {
			t.Fatalf("%s: no #retention section", lang)
		}
		sec := s[start : start+strings.Index(s[start:], "</section>")]
		if strings.Count(sec, p.line) != 1 {
			t.Errorf("%s: §8 doesn't name Coach's notes once", lang)
		}
	}
}

// §8 names how long the files a person attaches in a chat are kept, in every
// language: the owner's yes of 18 Nov 2026 (kiff/agents' attachments bucket).
func TestTafsPrivacyRetentionNamesChatAttachments(t *testing.T) {
	pages := map[string]struct {
		page templ.Component
		line string
	}{
		"en": {TafsPrivacy(), "and in any case within 38 days. A file no message uses is removed about an hour after you upload it. Deleting your account deletes them."},
		"de": {TafsPrivacyDE(), "auf jeden Fall innerhalb von 38 Tagen. Eine Datei, die keine Nachricht nutzt, wird etwa eine Stunde nach dem Hochladen entfernt."},
		"es": {TafsPrivacyES(), "en todo caso en un plazo de 38 días. Un archivo que ningún mensaje usa se elimina aproximadamente una hora después de subirlo."},
		"fr": {TafsPrivacyFR(), "dans tous les cas sous 38 jours. Un fichier qu'aucun message n'utilise est supprimé environ une heure après son envoi."},
		"it": {TafsPrivacyIT(), "in ogni caso entro 38 giorni. Un file che nessun messaggio usa viene rimosso circa un'ora dopo averlo caricato."},
		"nl": {TafsPrivacyNL(), "in elk geval binnen 38 dagen. Een bestand dat geen bericht gebruikt, wordt ongeveer een uur na het uploaden verwijderd."},
		"pl": {TafsPrivacyPL(), "w każdym razie w ciągu 38 dni. Plik, którego nie używa żadna wiadomość, jest usuwany mniej więcej godzinę po przesłaniu."},
		"pt": {TafsPrivacyPT(), "em qualquer caso no prazo de 38 dias. Um ficheiro que nenhuma mensagem usa é removido cerca de uma hora depois de o carregar."},
	}
	for lang, p := range pages {
		var b bytes.Buffer
		if err := p.page.Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		s := strings.ReplaceAll(b.String(), "&#39;", "'")
		start := strings.Index(s, `id="retention"`)
		if start < 0 {
			t.Fatalf("%s: no #retention section", lang)
		}
		sec := s[start : start+strings.Index(s[start:], "</section>")]
		if strings.Count(sec, p.line) != 1 {
			t.Errorf("%s: §8 doesn't name chat attachments once", lang)
		}
	}
}
