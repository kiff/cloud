package words

// Dutch membership emails (je).
func init() {
	nl.Membership = Membership{
		AnAccount:      "een KIFF-account",
		AccountNamed:   func(name string) string { return "het KIFF-account dat de beheerders „" + name + "” noemden" },
		UnnamedAccount: "Account zonder naam",
		InviteSubject: func(name string) string {
			if name == "" {
				return "Je bent uitgenodigd voor een account op KIFF"
			}
			return "Je bent uitgenodigd voor „" + name + "” op KIFF"
		},
		InviteIntro: func(by, account, role, what string) string {
			return by + " heeft je uitgenodigd voor " + account + ", als " + role + ", zodat je " + what + "."
		},
		InviteRoleWhat: map[string]string{
			"admin":    "het account kunt beheren: mensen, sleutels, facturering en beleid, en ook Cards en domeinen",
			"editor":   "kunt bepalen wat de agents van het account mogen: Cards, domeinen en tools",
			"approver": "kunt antwoorden op de vastgehouden acties van het account",
			"viewer":   "de agents, Cards en activiteit van het account kunt zien zonder iets te wijzigen",
		},
		RoleName:        map[string]string{"admin": "beheerder", "editor": "bewerker", "approver": "goedkeurder", "viewer": "kijker"},
		InviteRoleOther: "aan het account kunt werken",
		InviteAccept: func(url string) string {
			return "Log in met een willekeurig adres en accepteer de uitnodiging hier: " + url
		},
		InviteButton:  "Uitnodiging bekijken en accepteren",
		InviteExpires: func(when string) string { return "De uitnodiging verloopt op " + when + "." },
		InviteIgnore:  "Had je dit niet verwacht? Negeer het dan; er gebeurt niets tenzij je accepteert.",

		AnAdmin: "Een beheerder",
		Someone: "Iemand",
		AMember: "Een lid",

		PeopleLink:       "Bekijk de mensen in het account",
		NoticeWhy:        "Je krijgt dit omdat je beheerder of goedkeurder van het account bent.",
		AuthoritySubject: func(role, account string) string { return "Nieuwe " + role + " in " + account },
		AuthorityInvited: func(who, role, account, by string) string {
			return who + " is door " + by + " uitgenodigd als " + role + " in " + account + "."
		},
		AuthorityChanged: func(who, from, role, account, by string) string {
			return who + " is door " + by + " gewijzigd van " + from + " naar " + role + " in " + account + "."
		},
		JoinedSubject: func(account string) string { return "Iemand is lid geworden van " + account },
		JoinedUnknown: func(invited, role, account string) string {
			return "De uitnodiging aan " + invited + " is geaccepteerd, en die persoon is lid geworden van " + account + " als " + role + "."
		},
		JoinedOther: func(invited, who, role, account string) string {
			return "De uitnodiging aan " + invited + " is geaccepteerd door " + who + ", een ander adres, en die persoon is lid geworden van " + account + " als " + role + "."
		},
		JoinedSelf: func(who, role, account string) string {
			return who + " heeft de uitnodiging geaccepteerd en is lid geworden van " + account + " als " + role + "."
		},
		CardChangeSubject: "Een Card is gewijzigd om een vastgehouden actie door te laten",
		CardChangeBody: func(who, agent, action, entity string) string {
			card := "een Card"
			if agent != "" {
				card = "een Card van " + agent
			}
			out := who + " heeft " + card + " gewijzigd zodat de vastgehouden actie " + action
			if entity != "" {
				out += " op " + entity
			}
			return out + " door kon gaan. Functiescheiding staat aan, dus dit is een wijziging van de Card, geen goedkeuring door een goedkeurder; het staat vastgelegd op de Card."
		},
		CardChangeCards: func(cards string) string { return "Card: " + cards + "." },
		SoDOffSubject:   "Functiescheiding is uitgezet",
		SoDOffBody: func(who, account string) string {
			return who + " heeft functiescheiding uitgezet in " + account + ". Beheerders en bewerkers kunnen nu ook antwoorden op vastgehouden acties."
		},

		YourPhone:     "Je telefoon",
		RelinkSubject: func(phone string) string { return phone + " kan weer goedkeuren, met een nieuwe sleutel" },
		RelinkBody: func(phone string) string {
			return phone + " kan weer goedkeuren op KIFF, met een nieuwe sleutel voor vingerafdruk of gezicht. " +
				"Je hebt het net in je browser goedgekeurd. Was jij dat niet? Verwijder het apparaat in KIFF en log opnieuw in."
		},
		NewApproverSubject: func(phone string) string { return phone + " kan nu goedkeuren op KIFF" },
		NewApproverBody: func(phone string) string {
			return phone + " kan nu goedkeuren op KIFF, met vingerafdruk of gezicht. Je hebt het net in je browser goedgekeurd, terwijl een ander apparaat van je al goedkeurt. Was jij dat niet? Verwijder het apparaat in KIFF en wijzig je wachtwoord."
		},

		PackAskSubject:  "Een verzoek om een creditpakket voor tafs",
		PackAskBody:     func(who, pack string) string { return who + " vraagt om een creditpakket: " + pack + "." },
		RaiseAskSubject: "Een verzoek om de uitgavenlimiet van tafs te verhogen",
		RaiseAskBody: func(who string) string {
			return who + " vraagt je de uitgavenlimiet te verhogen, zodat de tafs van het account door kunnen."
		},
		AskReason: func(reason string) string { return "De reden: " + reason },
		PackAskHow: func(url string) string {
			return "Keur goed door het pakket toe te voegen, of weiger, in tafs of op kiff.dev: " + url
		},
		RaiseAskHow: "Verhoog de limiet, of weiger, in tafs.",
	}
}
