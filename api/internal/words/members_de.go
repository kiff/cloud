package words

// German membership emails (du).
func init() {
	de.Membership = Membership{
		AnAccount:      "einem KIFF-Konto",
		AccountNamed:   func(name string) string { return "dem KIFF-Konto, das seine Admins „" + name + "“ genannt haben," },
		UnnamedAccount: "Konto ohne Namen",
		InviteSubject: func(name string) string {
			if name == "" {
				return "Du bist zu einem Konto auf KIFF eingeladen"
			}
			return "Du bist zu „" + name + "“ auf KIFF eingeladen"
		},
		InviteIntro: func(by, account, role, what string) string {
			return by + " hat dich zu " + account + " eingeladen, als " + role + ", damit du " + what + "."
		},
		InviteRoleWhat: map[string]string{
			"admin":    "das Konto führen kannst: Personen, Schlüssel, Abrechnung und Richtlinien, dazu Cards und Domains",
			"editor":   "festlegen kannst, was die Agenten des Kontos dürfen: Cards, Domains und Tools",
			"approver": "die zurückgehaltenen Aktionen des Kontos beantworten kannst",
			"viewer":   "die Agenten, Cards und Aktivitäten des Kontos sehen kannst, ohne etwas zu ändern",
		},
		RoleName:        map[string]string{"admin": "Admin", "editor": "Bearbeiter", "approver": "Genehmiger", "viewer": "Betrachter"},
		InviteRoleOther: "am Konto mitarbeiten kannst",
		InviteAccept: func(url string) string {
			return "Melde dich mit einer beliebigen Adresse an und nimm sie hier an: " + url
		},
		InviteButton:  "Einladung ansehen und annehmen",
		InviteExpires: func(when string) string { return "Die Einladung läuft am " + when + " ab." },
		InviteIgnore:  "Wenn du das nicht erwartet hast, ignoriere es; ohne deine Annahme passiert nichts.",

		AnAdmin: "Ein Admin",
		Someone: "Jemand",
		AMember: "Ein Mitglied",

		PeopleLink:       "Personen im Konto ansehen",
		NoticeWhy:        "Du erhältst dies, weil du Admin oder Genehmiger des Kontos bist.",
		AuthoritySubject: func(role, account string) string { return "Neu als " + role + " in " + account },
		AuthorityInvited: func(who, role, account, by string) string {
			return who + " wurde von " + by + " als " + role + " in " + account + " eingeladen."
		},
		AuthorityChanged: func(who, from, role, account, by string) string {
			return who + " wurde von " + by + " in " + account + " von " + from + " zu " + role + " geändert."
		},
		JoinedSubject: func(account string) string { return "Jemand ist " + account + " beigetreten" },
		JoinedUnknown: func(invited, role, account string) string {
			return "Die Einladung an " + invited + " wurde angenommen, und die Person ist " + account + " als " + role + " beigetreten."
		},
		JoinedOther: func(invited, who, role, account string) string {
			return "Die Einladung an " + invited + " wurde von " + who + " angenommen, einer anderen Adresse, und die Person ist " + account + " als " + role + " beigetreten."
		},
		JoinedSelf: func(who, role, account string) string {
			return who + " hat die Einladung angenommen und ist " + account + " als " + role + " beigetreten."
		},
		CardChangeSubject: "Eine Card wurde geändert, um eine zurückgehaltene Aktion durchzulassen",
		CardChangeBody: func(who, agent, action, entity string) string {
			card := "eine Card"
			if agent != "" {
				card = "eine Card von " + agent
			}
			out := who + " hat " + card + " geändert, damit die zurückgehaltene Aktion " + action
			if entity != "" {
				out += " für " + entity
			}
			return out + " durchgehen konnte. Die Funktionstrennung ist an, daher ist das eine Änderung der Card, keine Genehmigung durch einen Genehmiger; sie ist auf der Card protokolliert."
		},
		CardChangeCards: func(cards string) string { return "Card: " + cards + "." },
		SoDOffSubject:   "Die Funktionstrennung wurde ausgeschaltet",
		SoDOffBody: func(who, account string) string {
			return who + " hat die Funktionstrennung in " + account + " ausgeschaltet. Admins und Bearbeiter können jetzt auch zurückgehaltene Aktionen beantworten."
		},

		YourPhone:     "Dein Handy",
		RelinkSubject: func(phone string) string { return phone + " kann wieder genehmigen, mit einem neuen Schlüssel" },
		RelinkBody: func(phone string) string {
			return phone + " kann auf KIFF wieder genehmigen, mit einem neuen Schlüssel für Fingerabdruck oder Gesicht. " +
				"Du hast es gerade in deinem Browser genehmigt. Wenn du das nicht warst, entferne das Gerät in KIFF und melde dich erneut an."
		},
		NewApproverSubject: func(phone string) string { return phone + " kann jetzt auf KIFF genehmigen" },
		NewApproverBody: func(phone string) string {
			return phone + " kann jetzt auf KIFF genehmigen, mit Fingerabdruck oder Gesicht. Du hast es gerade in deinem Browser genehmigt, während schon ein anderes Gerät von dir genehmigt. Wenn du das nicht warst, entferne das Gerät in KIFF und ändere dein Passwort."
		},

		PackAskSubject:  "Eine Anfrage nach einem Credit-Paket für tafs",
		PackAskBody:     func(who, pack string) string { return who + " bittet um ein Credit-Paket: " + pack + "." },
		RaiseAskSubject: "Eine Anfrage, das Ausgabenlimit von tafs zu erhöhen",
		RaiseAskBody: func(who string) string {
			return who + " bittet dich, das Ausgabenlimit zu erhöhen, damit die tafs des Kontos weiterarbeiten können."
		},
		AskReason: func(reason string) string { return "Begründung: " + reason },
		PackAskHow: func(url string) string {
			return "Genehmige, indem du das Paket hinzufügst, oder lehne ab, in tafs oder auf kiff.dev: " + url
		},
		RaiseAskHow: "Erhöhe es oder lehne ab, in tafs.",
	}
}
