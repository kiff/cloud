package words

// French membership emails (vous).
func init() {
	fr.Membership = Membership{
		AnAccount: "un compte KIFF",
		AccountNamed: func(name string) string {
			return "le compte KIFF que ses administrateurs ont nommé « " + name + " »"
		},
		UnnamedAccount: "Compte sans nom",
		InviteSubject: func(name string) string {
			if name == "" {
				return "Une invitation pour un compte sur KIFF"
			}
			return "Une invitation pour « " + name + " » sur KIFF"
		},
		InviteIntro: func(by, account, role, what string) string {
			return by + " vous invite à rejoindre " + account + ", en tant que " + role + ", pour " + what + "."
		},
		InviteRoleWhat: map[string]string{
			"admin":    "gérer le compte : personnes, clés, facturation et règles, ainsi que les Cards et les domaines",
			"editor":   "définir ce que les agents du compte peuvent faire : Cards, domaines et outils",
			"approver": "répondre aux actions retenues du compte",
			"viewer":   "voir les agents, les Cards et l'activité du compte sans rien modifier",
		},
		RoleName:        map[string]string{"admin": "administrateur", "editor": "éditeur", "approver": "approbateur", "viewer": "lecteur"},
		InviteRoleOther: "travailler sur le compte",
		InviteAccept: func(url string) string {
			return "Connectez-vous avec n'importe quelle adresse et acceptez-la ici : " + url
		},
		InviteButton:  "Voir et accepter l'invitation",
		InviteExpires: func(when string) string { return "L'invitation expire le " + when + "." },
		InviteIgnore:  "Si vous ne vous y attendiez pas, ignorez ce message ; rien ne se passe sans votre acceptation.",

		AnAdmin: "Un administrateur",
		Someone: "Quelqu'un",
		AMember: "Un membre",

		PeopleLink:       "Voir les personnes du compte",
		NoticeWhy:        "Vous recevez ce message parce que vous êtes administrateur ou approbateur du compte.",
		AuthoritySubject: func(role, account string) string { return "Nouveau rôle sur " + account + " : " + role },
		AuthorityInvited: func(who, role, account, by string) string {
			return "Une invitation a été envoyée à " + who + " en tant que " + role + " sur " + account + ", par " + by + "."
		},
		AuthorityChanged: func(who, from, role, account, by string) string {
			return "Le rôle de " + who + " est passé de " + from + " à " + role + " sur " + account + ", par " + by + "."
		},
		JoinedSubject: func(account string) string { return "Quelqu'un a rejoint " + account },
		JoinedUnknown: func(invited, role, account string) string {
			return "L'invitation envoyée à " + invited + " a été acceptée, et la personne a rejoint " + account + " en tant que " + role + "."
		},
		JoinedOther: func(invited, who, role, account string) string {
			return "L'invitation envoyée à " + invited + " a été acceptée par " + who + ", une autre adresse, et la personne a rejoint " + account + " en tant que " + role + "."
		},
		JoinedSelf: func(who, role, account string) string {
			return who + " a accepté son invitation et a rejoint " + account + " en tant que " + role + "."
		},
		CardChangeSubject: "Une Card a été modifiée pour laisser passer une action retenue",
		CardChangeBody: func(who, agent, action, entity string) string {
			card := "une Card"
			if agent != "" {
				card = "une Card de " + agent
			}
			out := who + " a modifié " + card + " pour que l'action retenue " + action
			if entity != "" {
				out += " sur " + entity
			}
			return out + " puisse passer. La séparation des tâches est activée : il s'agit donc d'une modification de Card, pas d'une approbation par un approbateur ; elle est enregistrée sur la Card."
		},
		CardChangeCards: func(cards string) string { return "Card : " + cards + "." },
		SoDOffSubject:   "La séparation des tâches a été désactivée",
		SoDOffBody: func(who, account string) string {
			return who + " a désactivé la séparation des tâches sur " + account + ". Les administrateurs et les éditeurs peuvent désormais aussi répondre aux actions retenues."
		},

		YourPhone:     "Votre téléphone",
		RelinkSubject: func(phone string) string { return phone + " peut de nouveau approuver, avec une nouvelle clé" },
		RelinkBody: func(phone string) string {
			return phone + " peut de nouveau approuver sur KIFF, avec une nouvelle clé pour l'empreinte ou le visage. " +
				"Vous l'avez approuvé dans votre navigateur à l'instant. Si ce n'était pas vous, supprimez l'appareil dans KIFF et reconnectez-vous."
		},
		NewApproverSubject: func(phone string) string { return phone + " peut maintenant approuver sur KIFF" },
		NewApproverBody: func(phone string) string {
			return phone + " peut maintenant approuver sur KIFF, avec l'empreinte ou le visage. Vous l'avez approuvé dans votre navigateur à l'instant, alors qu'un autre de vos appareils approuve déjà. Si ce n'était pas vous, supprimez l'appareil dans KIFF et changez votre mot de passe."
		},

		PackAskSubject:  "Une demande de pack de crédits tafs",
		PackAskBody:     func(who, pack string) string { return who + " demande un pack de crédits : " + pack + "." },
		RaiseAskSubject: "Une demande pour relever le plafond de dépenses de tafs",
		RaiseAskBody: func(who string) string {
			return who + " vous demande de relever le plafond de dépenses, pour que les tafs du compte puissent continuer."
		},
		AskReason: func(reason string) string { return "Sa raison : " + reason },
		PackAskHow: func(url string) string {
			return "Approuvez en ajoutant le pack, ou refusez, dans tafs ou sur kiff.dev : " + url
		},
		RaiseAskHow: "Relevez-le, ou refusez, dans tafs.",
	}
}
