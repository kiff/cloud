package words

// Italian membership emails (tu).
func init() {
	it.Membership = Membership{
		AnAccount: "un account KIFF",
		AccountNamed: func(name string) string {
			return "l'account KIFF che i suoi amministratori hanno chiamato «" + name + "»"
		},
		UnnamedAccount: "Account senza nome",
		InviteSubject: func(name string) string {
			if name == "" {
				return "Hai un invito per un account su KIFF"
			}
			return "Hai un invito per «" + name + "» su KIFF"
		},
		InviteIntro: func(by, account, role, what string) string {
			return by + " ti invita a entrare in " + account + ", come " + role + ", per " + what + "."
		},
		InviteRoleWhat: map[string]string{
			"admin":    "gestire l'account: persone, chiavi, fatturazione e regole, oltre a Card e domini",
			"editor":   "stabilire cosa possono fare gli agenti dell'account: Card, domini e strumenti",
			"approver": "rispondere alle azioni trattenute dell'account",
			"viewer":   "vedere gli agenti, le Card e l'attività dell'account senza modificare nulla",
		},
		RoleName:        map[string]string{"admin": "amministratore", "editor": "editor", "approver": "approvatore", "viewer": "visualizzatore"},
		InviteRoleOther: "lavorare sull'account",
		InviteAccept:    func(url string) string { return "Accedi con un indirizzo qualsiasi e accettalo qui: " + url },
		InviteButton:    "Vedi e accetta l'invito",
		InviteExpires:   func(when string) string { return "L'invito scade il " + when + "." },
		InviteIgnore:    "Se non te lo aspettavi, ignoralo; non succede nulla se non accetti.",

		AnAdmin: "Un amministratore",
		Someone: "Qualcuno",
		AMember: "Un membro",

		PeopleLink:       "Vedi le persone dell'account",
		NoticeWhy:        "Lo ricevi perché sei amministratore o approvatore dell'account.",
		AuthoritySubject: func(role, account string) string { return "Nuovo " + role + " in " + account },
		AuthorityInvited: func(who, role, account, by string) string {
			return "Un invito è stato inviato a " + who + " come " + role + " in " + account + ", da " + by + "."
		},
		AuthorityChanged: func(who, from, role, account, by string) string {
			return "Il ruolo di " + who + " è passato da " + from + " a " + role + " in " + account + ", da parte di " + by + "."
		},
		JoinedSubject: func(account string) string { return "Qualcuno è entrato in " + account },
		JoinedUnknown: func(invited, role, account string) string {
			return "L'invito inviato a " + invited + " è stato accettato, e la persona è entrata in " + account + " come " + role + "."
		},
		JoinedOther: func(invited, who, role, account string) string {
			return "L'invito inviato a " + invited + " è stato accettato da " + who + ", un altro indirizzo, e la persona è entrata in " + account + " come " + role + "."
		},
		JoinedSelf: func(who, role, account string) string {
			return who + " ha accettato l'invito e ora fa parte di " + account + " come " + role + "."
		},
		CardChangeSubject: "Una Card è stata modificata per lasciar passare un'azione trattenuta",
		CardChangeBody: func(who, agent, action, entity string) string {
			card := "una Card"
			if agent != "" {
				card = "una Card di " + agent
			}
			out := who + " ha modificato " + card + " perché l'azione trattenuta " + action
			if entity != "" {
				out += " su " + entity
			}
			return out + " potesse passare. La separazione dei compiti è attiva, quindi è una modifica della Card, non un'approvazione di un approvatore; è registrata sulla Card."
		},
		CardChangeCards: func(cards string) string { return "Card: " + cards + "." },
		SoDOffSubject:   "La separazione dei compiti è stata disattivata",
		SoDOffBody: func(who, account string) string {
			return who + " ha disattivato la separazione dei compiti in " + account + ". Ora anche amministratori ed editor possono rispondere alle azioni trattenute."
		},

		YourPhone:     "Il tuo telefono",
		RelinkSubject: func(phone string) string { return phone + " può di nuovo approvare, con una nuova chiave" },
		RelinkBody: func(phone string) string {
			return phone + " può di nuovo approvare su KIFF, con una nuova chiave per l'impronta o il volto. " +
				"L'hai approvato nel browser poco fa. Se non eri tu, rimuovi il dispositivo in KIFF e accedi di nuovo."
		},
		NewApproverSubject: func(phone string) string { return phone + " ora può approvare su KIFF" },
		NewApproverBody: func(phone string) string {
			return phone + " ora può approvare su KIFF, con l'impronta o il volto. L'hai approvato nel browser poco fa, mentre un altro tuo dispositivo approva già. Se non eri tu, rimuovi il dispositivo in KIFF e cambia la password."
		},

		PackAskSubject:  "Una richiesta di pacchetto di crediti tafs",
		PackAskBody:     func(who, pack string) string { return who + " chiede un pacchetto di crediti: " + pack + "." },
		RaiseAskSubject: "Una richiesta di aumentare il limite di spesa di tafs",
		RaiseAskBody: func(who string) string {
			return who + " ti chiede di aumentare il limite di spesa, così i tafs dell'account possono continuare."
		},
		AskReason: func(reason string) string { return "Il motivo: " + reason },
		PackAskHow: func(url string) string {
			return "Approva aggiungendo il pacchetto, o rifiuta, in tafs o su kiff.dev: " + url
		},
		RaiseAskHow: "Aumentalo, o rifiuta, in tafs.",
	}
}
