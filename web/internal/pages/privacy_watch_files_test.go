package pages

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// The watch paragraph (the owner's "yes privacy", 19 Nov 2026 09:31 Paris)
// and Files part (a) are on both /tafs/privacy and /privacy, in every
// language, once each: the Apple Watch and Wear OS facts, "never an amount",
// no recording kept on the watch, and that delivered files are kept 30 days,
// reachable only by the account's members, and not shown in the apps yet.

type watchFilesLang struct {
	tafs, privacy           templ.Component
	shared, apple, wear     string
	filesItem, filesRetains string
	old                     string
	theFiles                string
}

var watchFilesLangs = map[string]watchFilesLang{
	"en": {
		tafs: TafsPrivacy(), privacy: Privacy(),
		shared:       "a watch holds no password or key, talks only to your paired phone, and never approves. It sends what you say to Coach only as text, through the phone, once you've allowed the AI. The watch's own speech service, Apple's or Google's, turns your words into that text. It can refuse, halt or send to Coach only while it has a screen lock or passcode and is unlocked.",
		apple:        "it shows your tafs and their pages without keeping them. It keeps only your Inbox, in a file it can't read while it's locked: each held call as its taf's name and what it asks, never an amount, and Coach's replies to what you sent from the watch, as text. It keeps nothing unless the watch has a passcode. The Inbox goes when your iPhone signs out, or the next time the app opens or closes without a passcode. It makes no recording, and the only notifications it shows are your iPhone's.",
		wear:         "it shows your account's email, today's calendar and your tafs' pages without keeping them. It keeps your Inbox, where each line is a taf's name and what it says, never an amount, and one message kept for later, as text. Signing out, on the watch or on the phone, clears all of it. For its tile, it also keeps the count of what waits for you, shown only while the watch is unlocked, and cleared when you sign out, or the next time the tile shows while the watch is locked or has no screen lock. After you sign out on the watch, it keeps a note that you did, until you sign in again. The Inbox also goes the next time the app opens or closes while the watch is locked or has no screen lock. A message kept for later stays until you send or delete it, since you asked for it to be sent. The watch keeps no recording. A taf's result can stay in the watch's notifications until you dismiss it or sign out on the watch.",
		filesItem:    "Files your tafs deliver: 30 days from when the run made them, then deleted automatically within a day. Anyone on your account can delete one sooner with Delete now: the file itself is gone at once, with no copy kept; its name, size and the taf and run that made it can stay in the encrypted point-in-time copies of tafs's store for up to 35 days, used only to restore it after a failure. Removing a taf or deleting your account deletes its files. Sharing a file sends the file itself, and the copy you share stays with the app you shared it to. To download a file, we make a link for a signed-in member of your account that stops working after one minute; we make no other link to it.",
		filesRetains: "the files it delivers 30 days from the run that made them, then deleted automatically within a day, or sooner if someone on your account deletes one;",
		old:          "keeps nothing on disk",
		theFiles:     "and the files they deliver",
	},
	"de": {
		tafs: TafsPrivacyDE(), privacy: PrivacyDE(),
		shared:       "Eine Uhr hält kein Passwort und keinen Schlüssel, spricht nur mit deinem gekoppelten Telefon und genehmigt nie. Sie sendet, was du zu Coach sagst, nur als Text, über das Telefon, sobald du die KI erlaubt hast. Der eigene Sprachdienst der Uhr, der von Apple oder von Google, wandelt deine Worte in diesen Text um. Sie kann nur ablehnen, anhalten oder an Coach senden, solange sie eine Displaysperre oder einen Code hat und entsperrt ist.",
		apple:        "Sie zeigt deine tafs und ihre Seiten an, ohne sie aufzubewahren. Sie bewahrt nur deinen Eingang auf, in einer Datei, die sie nicht lesen kann, solange sie gesperrt ist: jeden zurückgehaltenen Aufruf als Namen seines tafs und das, worum er bittet, nie einen Betrag, und die Antworten von Coach auf das, was du von der Uhr gesendet hast, als Text. Sie bewahrt nichts auf, solange die Uhr keinen Code hat. Der Eingang wird gelöscht, wenn sich dein iPhone abmeldet, oder beim nächsten Öffnen oder Schließen der App ohne Code. Sie macht keine Aufnahme, und die einzigen Benachrichtigungen, die sie zeigt, sind die deines iPhone.",
		wear:         "Sie zeigt die E-Mail-Adresse deines Kontos, den heutigen Kalender und die Seiten deiner tafs an, ohne sie aufzubewahren. Sie bewahrt deinen Eingang auf, in dem jede Zeile der Name eines tafs ist und das, was er sagt, nie ein Betrag, sowie eine für später aufbewahrte Nachricht, als Text. Abmelden, auf der Uhr oder auf dem Telefon, löscht all das. Für ihre Kachel bewahrt sie außerdem die Anzahl dessen auf, was auf dich wartet, angezeigt nur, solange die Uhr entsperrt ist, und gelöscht, wenn du dich abmeldest, oder beim nächsten Anzeigen der Kachel, während die Uhr gesperrt ist oder keine Displaysperre hat. Nachdem du dich auf der Uhr abgemeldet hast, bewahrt sie einen Vermerk darüber auf, bis du dich wieder anmeldest. Der Eingang wird außerdem beim nächsten Öffnen oder Schließen der App gelöscht, wenn die Uhr gesperrt ist oder keine Displaysperre hat. Eine für später aufbewahrte Nachricht bleibt, bis du sie sendest oder löschst, da du darum gebeten hast, dass sie gesendet wird. Die Uhr bewahrt keine Aufnahme auf. Das Ergebnis eines tafs kann in den Benachrichtigungen der Uhr bleiben, bis du es schließt oder dich auf der Uhr abmeldest.",
		filesItem:    "Dateien, die deine tafs liefern: 30 Tage ab dem Lauf, der sie erstellt hat, dann innerhalb eines Tages automatisch gelöscht. Alle in deinem Konto können eine Datei mit „Jetzt löschen“ früher löschen: Die Datei selbst ist sofort weg, ohne dass eine Kopie bleibt; ihr Name, ihre Größe sowie der taf und der Lauf, die sie erstellt haben, können bis zu 35 Tage in den verschlüsselten Point-in-Time-Kopien des Speichers von tafs bleiben, die nur dazu dienen, ihn nach einem Ausfall wiederherzustellen. Wenn ein taf entfernt oder dein Konto gelöscht wird, werden seine Dateien gelöscht. Beim Teilen einer Datei wird die Datei selbst gesendet, und die geteilte Kopie bleibt bei der App, mit der du sie geteilt hast. Zum Herunterladen erstellen wir für ein angemeldetes Mitglied deines Kontos einen Link, der nach einer Minute nicht mehr funktioniert; einen anderen Link dazu erstellen wir nicht.",
		filesRetains: "die Dateien, die er liefert, 30 Tage ab dem Lauf, der sie erstellt hat, dann innerhalb eines Tages automatisch gelöscht, oder früher, wenn jemand in deinem Konto eine löscht;",
		old:          "speichert nichts auf dem Datenträger",
		theFiles:     "und die Dateien, die er liefert",
	},
	"es": {
		tafs: TafsPrivacyES(), privacy: PrivacyES(),
		shared:       "un reloj no guarda ninguna contraseña ni clave, solo habla con tu teléfono emparejado y nunca aprueba. Envía lo que le dices a Coach solo como texto, a través del teléfono, una vez que has permitido la IA. El propio servicio de voz del reloj, el de Apple o el de Google, convierte tus palabras en ese texto. Puede rechazar, detener o enviar a Coach solo mientras tiene un bloqueo de pantalla o un código y está desbloqueado.",
		apple:        "muestra tus tafs y sus páginas sin guardarlos. Guarda solo tu Bandeja, en un archivo que no puede leer mientras está bloqueado: cada llamada retenida como el nombre de su taf y lo que pide, nunca un importe, y las respuestas de Coach a lo que enviaste desde el reloj, como texto. No guarda nada si el reloj no tiene código. La Bandeja se borra cuando tu iPhone cierra sesión, o la próxima vez que la app se abre o se cierra sin código. No graba nada, y las únicas notificaciones que muestra son las de tu iPhone.",
		wear:         "muestra el correo de tu cuenta, el calendario de hoy y las páginas de tus tafs sin guardarlos. Guarda tu Bandeja, donde cada línea es el nombre de un taf y lo que dice, nunca un importe, y un mensaje guardado para más tarde, como texto. Cerrar sesión, en el reloj o en el teléfono, lo borra todo. Para su tarjeta, también guarda el número de lo que te espera, que solo se muestra mientras el reloj está desbloqueado y se borra cuando cierras sesión, o la próxima vez que la tarjeta se muestra mientras el reloj está bloqueado o no tiene bloqueo de pantalla. Después de que cierres sesión en el reloj, guarda una nota de que lo hiciste, hasta que vuelvas a iniciar sesión. La Bandeja también se borra la próxima vez que la app se abre o se cierra mientras el reloj está bloqueado o no tiene bloqueo de pantalla. Un mensaje guardado para más tarde se queda hasta que lo envías o lo eliminas, ya que pediste que se enviara. El reloj no guarda ninguna grabación. El resultado de un taf puede quedarse en las notificaciones del reloj hasta que lo descartes o cierres sesión en el reloj.",
		filesItem:    "Archivos que entregan tus tafs: 30 días desde la ejecución que los creó, y después se eliminan automáticamente en el plazo de un día. Cualquier miembro de tu cuenta puede eliminar uno antes con «Eliminar ahora»: el archivo desaparece al momento, sin que quede ninguna copia; su nombre, su tamaño y el taf y la ejecución que lo crearon pueden quedarse hasta 35 días en las copias cifradas para la recuperación a un momento dado del almacén de tafs, que solo se usan para restaurarlo tras un fallo. Eliminar un taf o eliminar tu cuenta elimina sus archivos. Compartir un archivo envía el propio archivo, y la copia que compartes se queda en la app con la que la compartiste. Para descargar un archivo, creamos para un miembro de tu cuenta con sesión iniciada un enlace que deja de funcionar al cabo de un minuto; no creamos ningún otro enlace a él.",
		filesRetains: "los archivos que entrega, 30 días desde la ejecución que los creó, y después se eliminan automáticamente en el plazo de un día, o antes si alguien de tu cuenta elimina uno;",
		old:          "no guarda nada en el disco",
		theFiles:     "y los archivos que entrega",
	},
	"fr": {
		tafs: TafsPrivacyFR(), privacy: PrivacyFR(),
		shared:       "une montre ne détient ni mot de passe ni clé, ne communique qu'avec votre téléphone associé, et n'approuve jamais. Elle n'envoie ce que vous dites à Coach que sous forme de texte, via le téléphone, une fois que vous avez autorisé l'IA. C'est le service de reconnaissance vocale de la montre elle-même, celui d'Apple ou de Google, qui transforme vos paroles en ce texte. Elle ne peut refuser, bloquer ou envoyer à Coach que lorsqu'elle a un verrouillage d'écran ou un code et qu'elle est déverrouillée.",
		apple:        "elle affiche vos tafs et leurs pages sans les garder. Elle ne garde que votre Boîte de réception, dans un fichier qu'elle ne peut pas lire tant qu'elle est verrouillée : chaque appel retenu sous la forme du nom de son taf et de ce qu'il demande, jamais un montant, et les réponses de Coach à ce que vous avez envoyé depuis la montre, sous forme de texte. Elle ne garde rien si la montre n'a pas de code. La Boîte de réception est effacée quand votre iPhone se déconnecte, ou la prochaine fois que l'app s'ouvre ou se ferme sans code. Elle ne fait aucun enregistrement, et les seules notifications qu'elle affiche sont celles de votre iPhone.",
		wear:         "elle affiche l'adresse e-mail de votre compte, le calendrier du jour et les pages de vos tafs sans les garder. Elle garde votre Boîte de réception, où chaque ligne est le nom d'un taf et ce qu'il dit, jamais un montant, et un message gardé pour plus tard, sous forme de texte. Vous déconnecter, sur la montre ou sur le téléphone, efface tout cela. Pour sa carte, elle garde aussi le nombre de ce qui vous attend, affiché seulement quand la montre est déverrouillée, et effacé quand vous vous déconnectez, ou la prochaine fois que la carte s'affiche alors que la montre est verrouillée ou n'a pas de verrouillage d'écran. Après une déconnexion sur la montre, elle garde une trace de cette déconnexion, jusqu'à ce que vous vous reconnectiez. La Boîte de réception est aussi effacée la prochaine fois que l'app s'ouvre ou se ferme alors que la montre est verrouillée ou n'a pas de verrouillage d'écran. Un message gardé pour plus tard reste jusqu'à ce que vous l'envoyiez ou le supprimiez, puisque vous avez demandé qu'il soit envoyé. La montre ne garde aucun enregistrement. Le résultat d'un taf peut rester dans les notifications de la montre jusqu'à ce que vous le fermiez ou que vous vous déconnectiez sur la montre.",
		filesItem:    "Fichiers livrés par vos tafs : 30 jours à partir de l'exécution qui les a créés, puis supprimés automatiquement dans un délai d'un jour. Tout membre de votre compte peut en supprimer un plus tôt avec « Supprimer maintenant » : le fichier lui-même disparaît aussitôt, sans qu'aucune copie ne soit gardée ; son nom, sa taille, ainsi que le taf et l'exécution qui l'ont créé peuvent rester jusqu'à 35 jours dans les copies chiffrées à un instant donné du stockage de tafs, qui ne servent qu'à le restaurer après une panne. Supprimer un taf ou supprimer votre compte supprime ses fichiers. Partager un fichier envoie le fichier lui-même, et la copie partagée reste dans l'app avec laquelle vous l'avez partagée. Pour télécharger un fichier, nous créons pour un membre connecté de votre compte un lien qui cesse de fonctionner au bout d'une minute ; nous ne créons aucun autre lien vers lui.",
		filesRetains: "les fichiers qu'il livre 30 jours à partir de l'exécution qui les a créés, puis supprimés automatiquement dans un délai d'un jour, ou plus tôt si quelqu'un de votre compte en supprime un ;",
		old:          "ne garde rien sur disque",
		theFiles:     "et les fichiers qu'il livre",
	},
	"it": {
		tafs: TafsPrivacyIT(), privacy: PrivacyIT(),
		shared:       "un orologio non contiene password né chiavi, comunica solo con il tuo telefono associato e non approva mai. Invia ciò che dici a Coach solo come testo, tramite il telefono, dopo che hai consentito l'IA. Il servizio vocale dell'orologio stesso, di Apple o di Google, trasforma le tue parole in quel testo. Può rifiutare, bloccare o inviare a Coach solo mentre ha un blocco schermo o un codice ed è sbloccato.",
		apple:        "mostra i tuoi taf e le loro pagine senza conservarli. Conserva solo la tua sezione In arrivo, in un file che non può leggere mentre è bloccato: ogni chiamata trattenuta come il nome del suo taf e ciò che chiede, mai un importo, e le risposte di Coach a ciò che hai inviato dall'orologio, come testo. Non conserva nulla se l'orologio non ha un codice. La sezione In arrivo viene cancellata quando il tuo iPhone si disconnette, o la volta successiva che l'app si apre o si chiude senza codice. Non fa registrazioni, e le uniche notifiche che mostra sono quelle del tuo iPhone.",
		wear:         "mostra l'email del tuo account, il calendario di oggi e le pagine dei tuoi taf senza conservarli. Conserva la tua sezione In arrivo, dove ogni riga è il nome di un taf e ciò che dice, mai un importo, e un messaggio conservato per dopo, come testo. Disconnetterti, sull'orologio o sul telefono, cancella tutto. Per il suo riquadro, conserva anche il numero di ciò che ti aspetta, mostrato solo mentre l'orologio è sbloccato, e cancellato quando ti disconnetti, o la volta successiva che il riquadro viene mostrato mentre l'orologio è bloccato o non ha un blocco schermo. Dopo una disconnessione sull'orologio, ne conserva una nota, finché non accedi di nuovo. La sezione In arrivo viene cancellata anche la volta successiva che l'app si apre o si chiude mentre l'orologio è bloccato o non ha un blocco schermo. Un messaggio conservato per dopo resta finché non lo invii o lo elimini, perché hai chiesto che venga inviato. L'orologio non conserva alcuna registrazione. Il risultato di un taf può restare nelle notifiche dell'orologio finché non lo chiudi o ti disconnetti sull'orologio.",
		filesItem:    "File consegnati dai tuoi taf: 30 giorni dall'esecuzione che li ha creati, poi cancellati automaticamente entro un giorno. Chiunque nel tuo account può eliminarne uno prima con «Elimina ora»: il file stesso sparisce subito, senza che ne resti alcuna copia; il suo nome, la sua dimensione e il taf e l'esecuzione che lo hanno creato possono restare fino a 35 giorni nelle copie point-in-time cifrate dell'archivio di tafs, usate solo per ripristinarlo dopo un guasto. Rimuovere un taf o eliminare il tuo account elimina i suoi file. Condividere un file invia il file stesso, e la copia condivisa resta nell'app con cui l'hai condivisa. Per scaricare un file, creiamo per un membro del tuo account che ha effettuato l'accesso un link che smette di funzionare dopo un minuto; non creiamo nessun altro link al file.",
		filesRetains: "i file che consegna per 30 giorni dall'esecuzione che li ha creati, poi cancellati automaticamente entro un giorno, o prima se qualcuno nel tuo account ne elimina uno;",
		old:          "non conserva nulla sul disco",
		theFiles:     "e i file che consegna",
	},
	"nl": {
		tafs: TafsPrivacyNL(), privacy: PrivacyNL(),
		shared:       "een horloge bevat geen wachtwoord of sleutel, praat alleen met je gekoppelde telefoon en keurt nooit goed. Het stuurt wat je tegen Coach zegt alleen als tekst, via de telefoon, zodra je de AI hebt toegestaan. De eigen spraakdienst van het horloge, die van Apple of van Google, zet je woorden om in die tekst. Het kan alleen weigeren, stilzetten of naar Coach sturen zolang het een schermvergrendeling of toegangscode heeft en ontgrendeld is.",
		apple:        "het toont je tafs en hun pagina's zonder ze te bewaren. Het bewaart alleen je Inbox, in een bestand dat het niet kan lezen zolang het vergrendeld is: elke vastgehouden aanroep als de naam van zijn taf en wat die vraagt, nooit een bedrag, en de antwoorden van Coach op wat je vanaf het horloge stuurde, als tekst. Het bewaart niets als het horloge geen toegangscode heeft. De Inbox verdwijnt als je iPhone uitlogt, of de volgende keer dat de app opent of sluit zonder toegangscode. Het neemt niets op, en de enige meldingen die het toont zijn die van je iPhone.",
		wear:         "het toont het e-mailadres van je account, de agenda van vandaag en de pagina's van je tafs zonder ze te bewaren. Het bewaart je Inbox, waarin elke regel de naam van een taf is en wat het zegt, nooit een bedrag, en één bericht dat voor later is bewaard, als tekst. Uitloggen, op het horloge of op de telefoon, wist dat allemaal. Voor de tegel bewaart het ook hoeveel er op je wacht, alleen getoond zolang het horloge ontgrendeld is, en gewist als je uitlogt, of de volgende keer dat de tegel wordt getoond terwijl het horloge vergrendeld is of geen schermvergrendeling heeft. Nadat je op het horloge hebt uitgelogd, bewaart het een notitie daarvan, tot je weer inlogt. De Inbox verdwijnt ook de volgende keer dat de app opent of sluit terwijl het horloge vergrendeld is of geen schermvergrendeling heeft. Een bericht dat voor later is bewaard, blijft tot je het verstuurt of verwijdert, omdat je hebt gevraagd het te versturen. Het horloge bewaart geen opname. Het resultaat van een taf kan in de meldingen van het horloge blijven tot je het wegveegt of op het horloge uitlogt.",
		filesItem:    "Bestanden die je tafs afleveren: 30 dagen vanaf de run die ze maakte, daarna binnen een dag automatisch verwijderd. Iedereen in je account kan er eerder een verwijderen met ‘Nu verwijderen’: het bestand zelf is meteen weg, zonder dat er een kopie blijft; de naam, de grootte en de taf en de run die het maakten, kunnen tot 35 dagen blijven in de versleutelde point-in-time-kopieën van de opslag van tafs, die alleen worden gebruikt om die na een storing te herstellen. Een taf verwijderen of je account verwijderen, verwijdert de bestanden ervan. Een bestand delen verstuurt het bestand zelf, en de gedeelde kopie blijft bij de app waarmee je het deelde. Om een bestand te downloaden, maken we voor een ingelogd lid van je account een link die na één minuut niet meer werkt; een andere link ernaar maken we niet.",
		filesRetains: "de bestanden die het aflevert 30 dagen vanaf de run die ze maakte, daarna binnen een dag automatisch verwijderd, of eerder als iemand in je account er een verwijdert;",
		old:          "bewaart niets op schijf",
		theFiles:     "en de bestanden die het aflevert",
	},
	"pl": {
		tafs: TafsPrivacyPL(), privacy: PrivacyPL(),
		shared:       "zegarek nie przechowuje hasła ani klucza, komunikuje się tylko z Twoim sparowanym telefonem i nigdy nie zatwierdza. To, co mówisz do Coacha, wysyła tylko jako tekst, przez telefon, gdy już zezwolisz na AI. Własna usługa rozpoznawania mowy zegarka, Apple lub Google, zamienia Twoje słowa na ten tekst. Może odrzucić, zablokować lub wysłać do Coacha tylko wtedy, gdy ma blokadę ekranu lub kod i jest odblokowany.",
		apple:        "pokazuje Twoje tafy i ich strony, nie przechowując ich. Przechowuje tylko Twoją Skrzynkę, w pliku, którego nie może odczytać, gdy jest zablokowany: każde oczekujące wywołanie jako nazwę jego tafa i to, o co prosi, nigdy kwotę, oraz odpowiedzi Coacha na to, co wysłano z zegarka, jako tekst. Nie przechowuje niczego, jeśli zegarek nie ma kodu. Skrzynka znika, gdy Twój iPhone się wyloguje, albo przy następnym otwarciu lub zamknięciu aplikacji bez kodu. Niczego nie nagrywa, a jedyne powiadomienia, jakie pokazuje, to powiadomienia Twojego iPhone’a.",
		wear:         "pokazuje adres e-mail Twojego konta, dzisiejszy kalendarz i strony Twoich tafów, nie przechowując ich. Przechowuje Twoją Skrzynkę, w której każdy wiersz to nazwa tafa i to, co mówi, nigdy kwota, oraz jedną wiadomość zachowaną na później, jako tekst. Wylogowanie, na zegarku lub na telefonie, czyści to wszystko. Na potrzeby kafelka przechowuje też liczbę tego, co czeka na Ciebie, pokazywaną tylko wtedy, gdy zegarek jest odblokowany, i czyszczoną przy wylogowaniu albo przy następnym wyświetleniu kafelka, gdy zegarek jest zablokowany lub nie ma blokady ekranu. Po wylogowaniu na zegarku przechowuje informację o tym wylogowaniu, dopóki nie zalogujesz się ponownie. Skrzynka znika też przy następnym otwarciu lub zamknięciu aplikacji, gdy zegarek jest zablokowany albo nie ma blokady ekranu. Wiadomość zachowana na później zostaje, dopóki jej nie wyślesz lub nie usuniesz, bo jej wysłanie zostało zlecone. Zegarek nie przechowuje żadnego nagrania. Wynik tafa może zostać w powiadomieniach zegarka, dopóki go nie zamkniesz lub nie wylogujesz się na zegarku.",
		filesItem:    "Pliki dostarczane przez Twoje tafy: 30 dni od uruchomienia, które je utworzyło, a potem automatycznie usuwane w ciągu doby. Każdy członek Twojego konta może usunąć plik wcześniej przyciskiem „Usuń teraz”: sam plik znika od razu i nie zostaje żadna kopia; jego nazwa, rozmiar oraz taf i uruchomienie, które go utworzyły, mogą pozostać do 35 dni w szyfrowanych kopiach do odtwarzania do punktu w czasie magazynu tafs, używanych wyłącznie do odtworzenia go po awarii. Usunięcie tafa lub usunięcie Twojego konta usuwa jego pliki. Udostępnienie pliku wysyła sam plik, a udostępniona kopia zostaje w aplikacji, w której ją udostępniono. Aby pobrać plik, tworzymy dla zalogowanego członka Twojego konta link, który przestaje działać po minucie; nie tworzymy do niego żadnego innego linku.",
		filesRetains: "pliki, które dostarcza, 30 dni od uruchomienia, które je utworzyło, a potem automatycznie usuwane w ciągu doby albo wcześniej, jeśli ktoś z Twojego konta usunie plik;",
		old:          "nie przechowuje niczego na dysku",
		theFiles:     "każdego tafa oraz pli",
	},
	"pt": {
		tafs: TafsPrivacyPT(), privacy: PrivacyPT(),
		shared:       "um relógio não tem palavra-passe nem chave, comunica apenas com o telemóvel emparelhado e nunca aprova. Envia o que diz ao Coach apenas como texto, através do telemóvel, depois de ter autorizado a IA. O próprio serviço de voz do relógio, da Apple ou da Google, converte as suas palavras nesse texto. Só pode recusar, bloquear ou enviar ao Coach enquanto tiver um bloqueio de ecrã ou um código de acesso e estiver desbloqueado.",
		apple:        "mostra os seus tafs e as respetivas páginas sem os guardar. Guarda apenas a sua Caixa de entrada, num ficheiro que não consegue ler enquanto está bloqueado: cada chamada retida como o nome do seu taf e o que pede, nunca um montante, e as respostas do Coach ao que enviou a partir do relógio, como texto. Não guarda nada se o relógio não tiver código de acesso. A Caixa de entrada é apagada quando o iPhone termina a sessão, ou da próxima vez que a app abre ou fecha sem código de acesso. Não faz gravações, e as únicas notificações que mostra são as do iPhone.",
		wear:         "mostra o email da sua conta, o calendário de hoje e as páginas dos seus tafs sem os guardar. Guarda a sua Caixa de entrada, onde cada linha é o nome de um taf e o que diz, nunca um montante, e uma mensagem guardada para mais tarde, como texto. Terminar a sessão, no relógio ou no telemóvel, apaga tudo isso. Para o seu mosaico, guarda também o número do que espera por si, mostrado apenas enquanto o relógio está desbloqueado, e apagado quando termina a sessão, ou da próxima vez que o mosaico é mostrado enquanto o relógio está bloqueado ou não tem bloqueio de ecrã. Depois de terminar a sessão no relógio, guarda uma nota disso, até voltar a iniciar sessão. A Caixa de entrada também é apagada da próxima vez que a app abre ou fecha enquanto o relógio está bloqueado ou não tem bloqueio de ecrã. Uma mensagem guardada para mais tarde fica até a enviar ou eliminar, já que pediu que fosse enviada. O relógio não guarda nenhuma gravação. O resultado de um taf pode ficar nas notificações do relógio até o dispensar ou terminar a sessão no relógio.",
		filesItem:    "Ficheiros entregues pelos seus tafs: 30 dias a partir da execução que os criou, depois apagados automaticamente no prazo de um dia. Qualquer membro da sua conta pode eliminar um mais cedo com «Eliminar agora»: o próprio ficheiro desaparece de imediato, sem ficar nenhuma cópia; o seu nome, o seu tamanho e o taf e a execução que o criaram podem ficar até 35 dias nas cópias encriptadas de recuperação para um ponto no tempo do armazenamento do tafs, usadas apenas para o restaurar após uma falha. Remover um taf ou eliminar a sua conta elimina os seus ficheiros. Partilhar um ficheiro envia o próprio ficheiro, e a cópia partilhada fica na app com que a partilhou. Para descarregar um ficheiro, criamos para um membro da sua conta com sessão iniciada uma ligação que deixa de funcionar ao fim de um minuto; não criamos nenhuma outra ligação para ele.",
		filesRetains: "os ficheiros que entrega durante 30 dias a partir da execução que os criou, depois apagados automaticamente no prazo de um dia, ou mais cedo se alguém da sua conta eliminar um;",
		old:          "não guarda nada no disco",
		theFiles:     "e os ficheiros que entrega",
	},
}

var watchFilesSpace = regexp.MustCompile(`\s+`)

// renderText renders a page and returns its text with tags removed and
// whitespace collapsed, so wrapped source lines compare as one sentence.
func renderText(t *testing.T, c templ.Component) string {
	t.Helper()
	var b bytes.Buffer
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer("&#39;", "'", "&#34;", `"`, "&amp;", "&").Replace(b.String())
	s = regexp.MustCompile(`<[^>]*>`).ReplaceAllString(s, " ")
	return watchFilesSpace.ReplaceAllString(s, " ")
}

func TestPrivacyPagesCarryWatchAndFilesText(t *testing.T) {
	for lang, l := range watchFilesLangs {
		for name, page := range map[string]templ.Component{"/tafs/privacy": l.tafs, "/privacy": l.privacy} {
			s := renderText(t, page)
			for what, want := range map[string]string{"shared": l.shared, "Apple Watch": l.apple, "Wear OS": l.wear} {
				if n := strings.Count(s, want); n != 1 {
					t.Errorf("%s %s: the %s watch text appears %d times, want 1", lang, name, what, n)
				}
			}
			// What tafs keeps names the files it delivers: §3 and §4 on
			// /tafs/privacy, the processor list on /privacy.
			wantFiles := map[string]int{"/tafs/privacy": 2, "/privacy": 1}[name]
			if n := strings.Count(s, l.theFiles); n != wantFiles {
				t.Errorf("%s %s: %q appears %d times, want %d", lang, name, l.theFiles, n, wantFiles)
			}
			if strings.Contains(s, l.old) {
				t.Errorf("%s %s: the old watch text is still there", lang, name)
			}
			if name == "/tafs/privacy" && strings.Count(s, l.filesItem) != 1 {
				t.Errorf("%s %s: the delivered-files item is missing", lang, name)
			}
			if name == "/privacy" && strings.Count(s, l.filesRetains) != 1 {
				t.Errorf("%s %s: delivered files' retention is missing", lang, name)
			}
		}
	}
}

// Files (b), the owner's yes of 19 Nov 2026 16:27 Paris: /tafs/privacy §4 says
// everyone on the account can see the files tafs deliver, in every language.
var filesSeenByAll = map[string]string{
	"en": "Everyone on your account can see the files your tafs deliver.",
	"de": "Alle in deinem Konto können die Dateien sehen, die deine tafs liefern.",
	"es": "Todos los miembros de tu cuenta pueden ver los archivos que entregan tus tafs.",
	"fr": "Tous les membres de votre compte peuvent voir les fichiers livrés par vos tafs.",
	"it": "Tutti i membri del tuo account possono vedere i file consegnati dai tuoi taf.",
	"nl": "Iedereen in je account kan de bestanden zien die je tafs afleveren.",
	"pl": "Wszyscy członkowie Twojego konta widzą pliki dostarczane przez Twoje tafy.",
	"pt": "Todos os membros da sua conta podem ver os ficheiros entregues pelos seus tafs.",
}

func TestTafsPrivacySaysEveryoneOnTheAccountSeesDeliveredFiles(t *testing.T) {
	for lang, line := range filesSeenByAll {
		if n := strings.Count(renderText(t, watchFilesLangs[lang].tafs), line); n != 1 {
			t.Errorf("%s /tafs/privacy: %q appears %d times, want 1", lang, line, n)
		}
	}
}
