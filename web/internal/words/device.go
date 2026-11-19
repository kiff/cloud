package words

// device is device approval in each language. English is word for word
// what the page said before it was translated. Role names are apps/api's
// (internal/words/members_*.go), checked by a test.
var device = map[string]Device{
	"en": {
		Title: "KIFF — approve device", Eyebrow: "Device sign-in", ThisPhone: "This phone",

		RelinkDoneTitle: "It can approve again",
		RelinkDoneF:     "%s approves with its new key. Go back to it; you can close this tab.",
		RelinkTitleF:    "Let %s approve again",
		RelinkOnlyF:     "Only approve this if %s is in your hands now.",
		RelinkWhy:       "It is already signed in. Its Face ID or fingerprints changed, so it asks for a new key to approve with; its old key stops working.",

		Code: "Code", Device: "Device", NewKey: "Its new key", Key: "Its key",
		CheckCodeMatches: "Check that the code matches the one the phone shows.",
		CouldntApprove:   "Couldn't approve:",
		SignInAgain:      "Sign in again",
		ApproveAgain:     "Approve again", OtherCode: "Use a different code",

		ApprovedTitle:  "Device approved",
		ApprovedNamedF: "%s is signed in. Go back to it; you can close this tab.",
		Approved:       "Go back to your device; it is signed in. You can close this tab.",

		ReviewTitle: "Approve this device",
		ReviewNote:  "Check that the code and key below match the ones your device shows, then approve. Only approve a sign-in you started yourself.",
		KeyNote:     "If the device shows its key, check that it matches. Approving lets the device answer held calls, each answer signed with that key.",
		SignInTo:    "Sign it in to", ApproveDevice: "Approve device",

		EnterNote:       "Enter the code your device shows. You'll see the device and its key before you approve.",
		CouldntContinue: "Couldn't continue:", DeviceCode: "Device code", Continue: "Continue",

		ErrUnknown:   "that code is unknown or has expired. Start signing in again on your device and use the new code.",
		ErrLookup:    "could not look up the code; try again.",
		ErrEnterCode: "enter the code your device shows.",
		ErrReauth:    "approving a device needs a recent sign-in. Sign in again, then approve it within ten minutes.",
		ErrApprove:   "could not approve the device; try again.",
		NoticeCheck:  "Check the device and its key below, then approve.",
		NoticeChoose: "Choose one of your accounts, then approve.",

		RoleName: map[string]string{"admin": "Admin", "editor": "Editor", "approver": "Approver", "viewer": "Viewer"},
	},
	"de": {
		Title: "KIFF — Gerät genehmigen", Eyebrow: "Anmeldung eines Geräts", ThisPhone: "Dieses Telefon",

		RelinkDoneTitle: "Es kann wieder genehmigen",
		RelinkDoneF:     "%s genehmigt jetzt mit seinem neuen Schlüssel. Geh zurück zu ihm; du kannst diesen Tab schließen.",
		RelinkTitleF:    "%s wieder genehmigen lassen",
		RelinkOnlyF:     "Genehmige das nur, wenn du %s gerade in der Hand hast.",
		RelinkWhy:       "Es ist schon angemeldet. Seine Face ID oder Fingerabdrücke haben sich geändert, deshalb bittet es um einen neuen Schlüssel zum Genehmigen; sein alter Schlüssel funktioniert dann nicht mehr.",

		Code: "Code", Device: "Gerät", NewKey: "Sein neuer Schlüssel", Key: "Sein Schlüssel",
		CheckCodeMatches: "Prüfe, ob der Code mit dem auf dem Telefon übereinstimmt.",
		CouldntApprove:   "Genehmigen nicht möglich:",
		SignInAgain:      "Erneut anmelden",
		ApproveAgain:     "Erneut genehmigen", OtherCode: "Anderen Code verwenden",

		ApprovedTitle:  "Gerät genehmigt",
		ApprovedNamedF: "%s ist angemeldet. Geh zurück zu ihm; du kannst diesen Tab schließen.",
		Approved:       "Geh zurück zu deinem Gerät; es ist angemeldet. Du kannst diesen Tab schließen.",

		ReviewTitle: "Dieses Gerät genehmigen",
		ReviewNote:  "Prüfe, ob Code und Schlüssel unten mit denen auf deinem Gerät übereinstimmen, und genehmige dann. Genehmige nur eine Anmeldung, die du selbst begonnen hast.",
		KeyNote:     "Wenn das Gerät seinen Schlüssel zeigt, prüfe, ob er übereinstimmt. Mit der Genehmigung kann das Gerät zurückgehaltene Aufrufe beantworten, jede Antwort mit diesem Schlüssel signiert.",
		SignInTo:    "Anmelden bei", ApproveDevice: "Gerät genehmigen",

		EnterNote:       "Gib den Code ein, den dein Gerät zeigt. Du siehst das Gerät und seinen Schlüssel, bevor du genehmigst.",
		CouldntContinue: "Weiter nicht möglich:", DeviceCode: "Gerätecode", Continue: "Weiter",

		ErrUnknown:   "Dieser Code ist unbekannt oder abgelaufen. Starte die Anmeldung auf deinem Gerät neu und nutze den neuen Code.",
		ErrLookup:    "Der Code ließ sich nicht nachschlagen; versuche es erneut.",
		ErrEnterCode: "Gib den Code ein, den dein Gerät zeigt.",
		ErrReauth:    "Ein Gerät zu genehmigen erfordert eine kürzlich erfolgte Anmeldung. Melde dich erneut an und genehmige es dann innerhalb von zehn Minuten.",
		ErrApprove:   "Das Gerät ließ sich nicht genehmigen; versuche es erneut.",
		NoticeCheck:  "Prüfe das Gerät und seinen Schlüssel unten, dann genehmige.",
		NoticeChoose: "Wähle eines deiner Konten, dann genehmige.",

		RoleName: map[string]string{"admin": "Admin", "editor": "Bearbeiter", "approver": "Genehmiger", "viewer": "Betrachter"},
	},
	"fr": {
		Title: "KIFF — approuver l'appareil", Eyebrow: "Connexion d'un appareil", ThisPhone: "Ce téléphone",

		RelinkDoneTitle: "Il peut de nouveau approuver",
		RelinkDoneF:     "%s approuve désormais avec sa nouvelle clé. Revenez-y ; vous pouvez fermer cet onglet.",
		RelinkTitleF:    "Laisser %s approuver de nouveau",
		RelinkOnlyF:     "N'approuvez que si vous avez %s en main en ce moment.",
		RelinkWhy:       "Il est déjà connecté. Son Face ID ou ses empreintes ont changé, il demande donc une nouvelle clé pour approuver ; son ancienne clé cesse de fonctionner.",

		Code: "Code", Device: "Appareil", NewKey: "Sa nouvelle clé", Key: "Sa clé",
		CheckCodeMatches: "Vérifiez que le code correspond à celui affiché sur le téléphone.",
		CouldntApprove:   "Approbation impossible :",
		SignInAgain:      "Se reconnecter",
		ApproveAgain:     "Approuver de nouveau", OtherCode: "Utiliser un autre code",

		ApprovedTitle:  "Appareil approuvé",
		ApprovedNamedF: "%s est connecté. Revenez-y ; vous pouvez fermer cet onglet.",
		Approved:       "Revenez à votre appareil ; il est connecté. Vous pouvez fermer cet onglet.",

		ReviewTitle: "Approuver cet appareil",
		ReviewNote:  "Vérifiez que le code et la clé ci-dessous correspondent à ceux affichés sur votre appareil, puis approuvez. N'approuvez qu'une connexion que vous avez lancée vous-même.",
		KeyNote:     "Si l'appareil affiche sa clé, vérifiez qu'elle correspond. L'approbation permet à l'appareil de répondre aux appels retenus, chaque réponse étant signée avec cette clé.",
		SignInTo:    "Le connecter à", ApproveDevice: "Approuver l'appareil",

		EnterNote:       "Saisissez le code affiché sur votre appareil. Vous verrez l'appareil et sa clé avant d'approuver.",
		CouldntContinue: "Impossible de continuer :", DeviceCode: "Code de l'appareil", Continue: "Continuer",

		ErrUnknown:   "ce code est inconnu ou a expiré. Relancez la connexion sur votre appareil et utilisez le nouveau code.",
		ErrLookup:    "impossible de vérifier le code ; réessayez.",
		ErrEnterCode: "saisissez le code affiché sur votre appareil.",
		ErrReauth:    "approuver un appareil demande une connexion récente. Reconnectez-vous, puis approuvez-le dans les dix minutes.",
		ErrApprove:   "impossible d'approuver l'appareil ; réessayez.",
		NoticeCheck:  "Vérifiez l'appareil et sa clé ci-dessous, puis approuvez.",
		NoticeChoose: "Choisissez l'un de vos comptes, puis approuvez.",

		RoleName: map[string]string{"admin": "administrateur", "editor": "éditeur", "approver": "approbateur", "viewer": "lecteur"},
	},
	"es": {
		Title: "KIFF — aprobar dispositivo", Eyebrow: "Inicio de sesión de un dispositivo", ThisPhone: "Este teléfono",

		RelinkDoneTitle: "Ya puede volver a aprobar",
		RelinkDoneF:     "%s aprueba ahora con su nueva clave. Vuelve a él; puedes cerrar esta pestaña.",
		RelinkTitleF:    "Dejar que %s vuelva a aprobar",
		RelinkOnlyF:     "Apruébalo solo si tienes %s en la mano ahora.",
		RelinkWhy:       "Ya tiene la sesión iniciada. Su Face ID o sus huellas cambiaron, así que pide una clave nueva para aprobar; su clave anterior deja de funcionar.",

		Code: "Código", Device: "Dispositivo", NewKey: "Su nueva clave", Key: "Su clave",
		CheckCodeMatches: "Comprueba que el código coincide con el que muestra el teléfono.",
		CouldntApprove:   "No se pudo aprobar:",
		SignInAgain:      "Volver a iniciar sesión",
		ApproveAgain:     "Aprobar de nuevo", OtherCode: "Usar otro código",

		ApprovedTitle:  "Dispositivo aprobado",
		ApprovedNamedF: "%s tiene la sesión iniciada. Vuelve a él; puedes cerrar esta pestaña.",
		Approved:       "Vuelve a tu dispositivo; ya tiene la sesión iniciada. Puedes cerrar esta pestaña.",

		ReviewTitle: "Aprobar este dispositivo",
		ReviewNote:  "Comprueba que el código y la clave de abajo coinciden con los que muestra tu dispositivo y luego aprueba. Aprueba solo un inicio de sesión que hayas empezado tú.",
		KeyNote:     "Si el dispositivo muestra su clave, comprueba que coincide. Al aprobar, el dispositivo podrá responder a las llamadas retenidas, cada respuesta firmada con esa clave.",
		SignInTo:    "Iniciar sesión en", ApproveDevice: "Aprobar dispositivo",

		EnterNote:       "Escribe el código que muestra tu dispositivo. Verás el dispositivo y su clave antes de aprobar.",
		CouldntContinue: "No se pudo continuar:", DeviceCode: "Código del dispositivo", Continue: "Continuar",

		ErrUnknown:   "ese código es desconocido o ha caducado. Vuelve a iniciar sesión en tu dispositivo y usa el código nuevo.",
		ErrLookup:    "no se pudo comprobar el código; inténtalo de nuevo.",
		ErrEnterCode: "escribe el código que muestra tu dispositivo.",
		ErrReauth:    "aprobar un dispositivo requiere un inicio de sesión reciente. Vuelve a iniciar sesión y apruébalo en menos de diez minutos.",
		ErrApprove:   "no se pudo aprobar el dispositivo; inténtalo de nuevo.",
		NoticeCheck:  "Comprueba el dispositivo y su clave abajo y luego aprueba.",
		NoticeChoose: "Elige una de tus cuentas y luego aprueba.",

		RoleName: map[string]string{"admin": "administrador", "editor": "editor", "approver": "aprobador", "viewer": "lector"},
	},
	"it": {
		Title: "KIFF — approva dispositivo", Eyebrow: "Accesso di un dispositivo", ThisPhone: "Questo telefono",

		RelinkDoneTitle: "Può approvare di nuovo",
		RelinkDoneF:     "%s ora approva con la sua nuova chiave. Torna lì; puoi chiudere questa scheda.",
		RelinkTitleF:    "Consenti a %s di approvare di nuovo",
		RelinkOnlyF:     "Approva solo se hai %s in mano adesso.",
		RelinkWhy:       "Ha già effettuato l'accesso. Il suo Face ID o le impronte sono cambiati, quindi chiede una nuova chiave per approvare; la vecchia chiave smette di funzionare.",

		Code: "Codice", Device: "Dispositivo", NewKey: "La sua nuova chiave", Key: "La sua chiave",
		CheckCodeMatches: "Controlla che il codice corrisponda a quello mostrato sul telefono.",
		CouldntApprove:   "Impossibile approvare:",
		SignInAgain:      "Accedi di nuovo",
		ApproveAgain:     "Approva di nuovo", OtherCode: "Usa un altro codice",

		ApprovedTitle:  "Dispositivo approvato",
		ApprovedNamedF: "%s ha effettuato l'accesso. Torna lì; puoi chiudere questa scheda.",
		Approved:       "Torna al tuo dispositivo; ha effettuato l'accesso. Puoi chiudere questa scheda.",

		ReviewTitle: "Approva questo dispositivo",
		ReviewNote:  "Controlla che il codice e la chiave qui sotto corrispondano a quelli mostrati sul tuo dispositivo, poi approva. Approva solo un accesso che hai avviato tu.",
		KeyNote:     "Se il dispositivo mostra la sua chiave, controlla che corrisponda. Approvando, il dispositivo potrà rispondere alle chiamate trattenute, ogni risposta firmata con quella chiave.",
		SignInTo:    "Accedi a", ApproveDevice: "Approva dispositivo",

		EnterNote:       "Inserisci il codice mostrato dal tuo dispositivo. Vedrai il dispositivo e la sua chiave prima di approvare.",
		CouldntContinue: "Impossibile continuare:", DeviceCode: "Codice del dispositivo", Continue: "Continua",

		ErrUnknown:   "quel codice è sconosciuto o scaduto. Ricomincia l'accesso sul tuo dispositivo e usa il nuovo codice.",
		ErrLookup:    "impossibile verificare il codice; riprova.",
		ErrEnterCode: "inserisci il codice mostrato dal tuo dispositivo.",
		ErrReauth:    "approvare un dispositivo richiede un accesso recente. Accedi di nuovo, poi approvalo entro dieci minuti.",
		ErrApprove:   "impossibile approvare il dispositivo; riprova.",
		NoticeCheck:  "Controlla il dispositivo e la sua chiave qui sotto, poi approva.",
		NoticeChoose: "Scegli uno dei tuoi account, poi approva.",

		RoleName: map[string]string{"admin": "amministratore", "editor": "editor", "approver": "approvatore", "viewer": "visualizzatore"},
	},
	"pt": {
		Title: "KIFF — aprovar dispositivo", Eyebrow: "Início de sessão de um dispositivo", ThisPhone: "Este telefone",

		RelinkDoneTitle: "Já pode voltar a aprovar",
		RelinkDoneF:     "%s aprova agora com a sua nova chave. Volte a ele; pode fechar este separador.",
		RelinkTitleF:    "Deixar %s voltar a aprovar",
		RelinkOnlyF:     "Aprove apenas se tiver %s na mão neste momento.",
		RelinkWhy:       "Já tem sessão iniciada. O seu Face ID ou as impressões digitais mudaram, por isso pede uma nova chave para aprovar; a chave antiga deixa de funcionar.",

		Code: "Código", Device: "Dispositivo", NewKey: "Nova chave do dispositivo", Key: "Chave do dispositivo",
		CheckCodeMatches: "Confirme que o código corresponde ao que o telefone mostra.",
		CouldntApprove:   "Não foi possível aprovar:",
		SignInAgain:      "Iniciar sessão novamente",
		ApproveAgain:     "Aprovar novamente", OtherCode: "Usar outro código",

		ApprovedTitle:  "Dispositivo aprovado",
		ApprovedNamedF: "%s tem sessão iniciada. Volte a ele; pode fechar este separador.",
		Approved:       "Volte ao seu dispositivo; tem sessão iniciada. Pode fechar este separador.",

		ReviewTitle: "Aprovar este dispositivo",
		ReviewNote:  "Confirme que o código e a chave abaixo correspondem aos que o seu dispositivo mostra e depois aprove. Aprove apenas um início de sessão que tenha iniciado pessoalmente.",
		KeyNote:     "Se o dispositivo mostrar a sua chave, confirme que corresponde. Ao aprovar, o dispositivo pode responder a chamadas retidas, cada resposta assinada com essa chave.",
		SignInTo:    "Iniciar sessão em", ApproveDevice: "Aprovar dispositivo",

		EnterNote:       "Introduza o código que o seu dispositivo mostra. Verá o dispositivo e a respetiva chave antes de aprovar.",
		CouldntContinue: "Não foi possível continuar:", DeviceCode: "Código do dispositivo", Continue: "Continuar",

		ErrUnknown:   "esse código é desconhecido ou expirou. Inicie sessão novamente no seu dispositivo e use o novo código.",
		ErrLookup:    "não foi possível verificar o código; tente novamente.",
		ErrEnterCode: "introduza o código que o seu dispositivo mostra.",
		ErrReauth:    "aprovar um dispositivo exige um início de sessão recente. Inicie sessão novamente e aprove-o dentro de dez minutos.",
		ErrApprove:   "não foi possível aprovar o dispositivo; tente novamente.",
		NoticeCheck:  "Confirme o dispositivo e a respetiva chave abaixo e depois aprove.",
		NoticeChoose: "Escolha uma das suas contas e depois aprove.",

		RoleName: map[string]string{"admin": "administrador", "editor": "editor", "approver": "aprovador", "viewer": "leitor"},
	},
	"nl": {
		Title: "KIFF — apparaat goedkeuren", Eyebrow: "Inloggen van een apparaat", ThisPhone: "Deze telefoon",

		RelinkDoneTitle: "Het kan weer goedkeuren",
		RelinkDoneF:     "%s keurt nu goed met zijn nieuwe sleutel. Ga ernaar terug; je kunt dit tabblad sluiten.",
		RelinkTitleF:    "%s opnieuw laten goedkeuren",
		RelinkOnlyF:     "Keur dit alleen goed als je %s nu in handen hebt.",
		RelinkWhy:       "Het is al ingelogd. Zijn Face ID of vingerafdrukken zijn veranderd, dus het vraagt een nieuwe sleutel om mee goed te keuren; de oude sleutel werkt dan niet meer.",

		Code: "Code", Device: "Apparaat", NewKey: "Zijn nieuwe sleutel", Key: "Zijn sleutel",
		CheckCodeMatches: "Controleer of de code overeenkomt met die op de telefoon.",
		CouldntApprove:   "Goedkeuren lukte niet:",
		SignInAgain:      "Opnieuw inloggen",
		ApproveAgain:     "Opnieuw goedkeuren", OtherCode: "Andere code gebruiken",

		ApprovedTitle:  "Apparaat goedgekeurd",
		ApprovedNamedF: "%s is ingelogd. Ga ernaar terug; je kunt dit tabblad sluiten.",
		Approved:       "Ga terug naar je apparaat; het is ingelogd. Je kunt dit tabblad sluiten.",

		ReviewTitle: "Dit apparaat goedkeuren",
		ReviewNote:  "Controleer of de code en de sleutel hieronder overeenkomen met die op je apparaat, en keur dan goed. Keur alleen een inlog goed die je zelf bent begonnen.",
		KeyNote:     "Als het apparaat zijn sleutel toont, controleer dan of die overeenkomt. Na goedkeuring kan het apparaat vastgehouden aanroepen beantwoorden, elk antwoord ondertekend met die sleutel.",
		SignInTo:    "Inloggen bij", ApproveDevice: "Apparaat goedkeuren",

		EnterNote:       "Voer de code in die je apparaat toont. Je ziet het apparaat en zijn sleutel voordat je goedkeurt.",
		CouldntContinue: "Verder gaan lukte niet:", DeviceCode: "Apparaatcode", Continue: "Doorgaan",

		ErrUnknown:   "die code is onbekend of verlopen. Begin opnieuw met inloggen op je apparaat en gebruik de nieuwe code.",
		ErrLookup:    "de code kon niet worden opgezocht; probeer het opnieuw.",
		ErrEnterCode: "voer de code in die je apparaat toont.",
		ErrReauth:    "een apparaat goedkeuren vraagt een recente inlog. Log opnieuw in en keur het binnen tien minuten goed.",
		ErrApprove:   "het apparaat kon niet worden goedgekeurd; probeer het opnieuw.",
		NoticeCheck:  "Controleer het apparaat en zijn sleutel hieronder en keur dan goed.",
		NoticeChoose: "Kies een van je accounts en keur dan goed.",

		RoleName: map[string]string{"admin": "beheerder", "editor": "bewerker", "approver": "goedkeurder", "viewer": "kijker"},
	},
	"pl": {
		Title: "KIFF — zatwierdź urządzenie", Eyebrow: "Logowanie urządzenia", ThisPhone: "Ten telefon",

		RelinkDoneTitle: "Może znowu zatwierdzać",
		RelinkDoneF:     "%s zatwierdza teraz nowym kluczem. Wróć do niego; możesz zamknąć tę kartę.",
		RelinkTitleF:    "Przywróć zatwierdzanie: %s",
		RelinkOnlyF:     "Zatwierdź to tylko wtedy, gdy masz teraz w ręku: %s.",
		RelinkWhy:       "Logowanie jest już aktywne. Zmienił się Face ID lub odciski palców, więc potrzebny jest nowy klucz do zatwierdzania; stary klucz przestaje działać.",

		Code: "Kod", Device: "Urządzenie", NewKey: "Jego nowy klucz", Key: "Jego klucz",
		CheckCodeMatches: "Sprawdź, czy kod zgadza się z tym, który pokazuje telefon.",
		CouldntApprove:   "Nie udało się zatwierdzić:",
		SignInAgain:      "Zaloguj się ponownie",
		ApproveAgain:     "Zatwierdź ponownie", OtherCode: "Użyj innego kodu",

		ApprovedTitle:  "Urządzenie zatwierdzone",
		ApprovedNamedF: "Zalogowano: %s. Wróć do niego; możesz zamknąć tę kartę.",
		Approved:       "Wróć do swojego urządzenia; jest zalogowane. Możesz zamknąć tę kartę.",

		ReviewTitle: "Zatwierdź to urządzenie",
		ReviewNote:  "Sprawdź, czy kod i klucz poniżej zgadzają się z tymi, które pokazuje Twoje urządzenie, a potem zatwierdź. Zatwierdzaj tylko logowanie rozpoczęte przez Ciebie.",
		KeyNote:     "Jeśli urządzenie pokazuje swój klucz, sprawdź, czy się zgadza. Po zatwierdzeniu urządzenie może odpowiadać na oczekujące wywołania, a każda odpowiedź jest podpisana tym kluczem.",
		SignInTo:    "Zaloguj do konta", ApproveDevice: "Zatwierdź urządzenie",

		EnterNote:       "Wpisz kod, który pokazuje Twoje urządzenie. Zanim zatwierdzisz, zobaczysz urządzenie i jego klucz.",
		CouldntContinue: "Nie udało się kontynuować:", DeviceCode: "Kod urządzenia", Continue: "Dalej",

		ErrUnknown:   "ten kod jest nieznany lub wygasł. Zacznij logowanie od nowa na swoim urządzeniu i użyj nowego kodu.",
		ErrLookup:    "nie udało się sprawdzić kodu; spróbuj ponownie.",
		ErrEnterCode: "wpisz kod, który pokazuje Twoje urządzenie.",
		ErrReauth:    "zatwierdzenie urządzenia wymaga niedawnego logowania. Zaloguj się ponownie, a potem zatwierdź je w ciągu dziesięciu minut.",
		ErrApprove:   "nie udało się zatwierdzić urządzenia; spróbuj ponownie.",
		NoticeCheck:  "Sprawdź urządzenie i jego klucz poniżej, a potem zatwierdź.",
		NoticeChoose: "Wybierz jedno ze swoich kont, a potem zatwierdź.",

		RoleName: map[string]string{"admin": "administrator", "editor": "edytor", "approver": "zatwierdzający", "viewer": "przeglądający"},
	},
}
