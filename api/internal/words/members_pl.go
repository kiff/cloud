package words

// Polish membership emails (Ty). Sentences avoid gendered past forms, so
// they read right whoever did what: an impersonal form, or "Osoba" with
// the person's address.
func init() {
	pl.Membership = Membership{
		AnAccount:      "konta KIFF",
		AccountNamed:   func(name string) string { return "konta KIFF, które jego administratorzy nazwali „" + name + "”" },
		UnnamedAccount: "Konto bez nazwy",
		InviteSubject: func(name string) string {
			if name == "" {
				return "Zaproszenie do konta w KIFF"
			}
			return "Zaproszenie do „" + name + "” w KIFF"
		},
		InviteIntro: func(by, account, role, what string) string {
			return "Zaproszenie od: " + by + " – do " + account + " jako " + role + ", aby " + what + "."
		},
		InviteRoleWhat: map[string]string{
			"admin":    "zarządzać kontem: osobami, kluczami, rozliczeniami i zasadami, a także Card i domenami",
			"editor":   "ustalać, co mogą robić agenci konta: Card, domeny i narzędzia",
			"approver": "odpowiadać na oczekujące akcje konta",
			"viewer":   "widzieć agentów, Card i aktywność konta bez wprowadzania zmian",
		},
		RoleName:        map[string]string{"admin": "administrator", "editor": "edytor", "approver": "zatwierdzający", "viewer": "przeglądający"},
		InviteRoleOther: "pracować na koncie",
		InviteAccept:    func(url string) string { return "Zaloguj się dowolnym adresem i przyjmij zaproszenie tutaj: " + url },
		InviteButton:    "Zobacz i przyjmij zaproszenie",
		InviteExpires:   func(when string) string { return "Zaproszenie wygasa: " + when + "." },
		InviteIgnore:    "Jeśli to dla Ciebie zaskoczenie, zignoruj tę wiadomość; nic się nie stanie bez Twojej zgody.",

		AnAdmin: "administrator",
		Someone: "nieznana osoba",
		AMember: "członek konta",

		PeopleLink:       "Zobacz osoby na koncie",
		NoticeWhy:        "Otrzymujesz to, ponieważ masz na tym koncie rolę administratora lub zatwierdzającego.",
		AuthoritySubject: func(role, account string) string { return "Nowa rola na koncie " + account + ": " + role },
		AuthorityInvited: func(who, role, account, by string) string {
			return "Na koncie " + account + " wysłano zaproszenie do roli " + role + " dla: " + who + " (od: " + by + ")."
		},
		AuthorityChanged: func(who, from, role, account, by string) string {
			return "Na koncie " + account + " zmieniono rolę z " + from + " na " + role + " dla: " + who + " (zmiana: " + by + ")."
		},
		JoinedSubject: func(account string) string { return "Nowa osoba na koncie " + account },
		JoinedUnknown: func(invited, role, account string) string {
			return "Zaproszenie wysłane na " + invited + " zostało przyjęte; ta osoba dołącza do konta " + account + " jako " + role + "."
		},
		JoinedOther: func(invited, who, role, account string) string {
			return "Zaproszenie wysłane na " + invited + " przyjęto z innego adresu (" + who + "); ta osoba dołącza do konta " + account + " jako " + role + "."
		},
		JoinedSelf: func(who, role, account string) string {
			return "Zaproszenie przyjęte: " + who + " dołącza do konta " + account + " jako " + role + "."
		},
		CardChangeSubject: "Zmieniono Card, aby przepuścić oczekującą akcję",
		CardChangeBody: func(who, agent, action, entity string) string {
			card := "Card"
			if agent != "" {
				card = "Card agenta " + agent
			}
			out := "Zmieniono " + card + " (zmiana: " + who + "), aby oczekująca akcja " + action
			if entity != "" {
				out += " na " + entity
			}
			return out + " mogła przejść. Rozdział obowiązków jest włączony, więc to zmiana Card, a nie zatwierdzenie przez osobę zatwierdzającą; jest zapisana na Card."
		},
		CardChangeCards: func(cards string) string { return "Card: " + cards + "." },
		SoDOffSubject:   "Rozdział obowiązków został wyłączony",
		SoDOffBody: func(who, account string) string {
			return "Na koncie " + account + " wyłączono rozdział obowiązków (zmiana: " + who + "). Teraz administratorzy i edytorzy też mogą odpowiadać na oczekujące akcje."
		},

		YourPhone:     "Twój telefon",
		RelinkSubject: func(phone string) string { return phone + " znów może zatwierdzać, z nowym kluczem" },
		RelinkBody: func(phone string) string {
			return phone + " znów może zatwierdzać w KIFF, z nowym kluczem do odcisku palca lub twarzy. " +
				"Zatwierdzono to przed chwilą w Twojej przeglądarce. Jeśli to nie Ty, usuń urządzenie w KIFF i zaloguj się ponownie."
		},
		NewApproverSubject: func(phone string) string { return phone + " może teraz zatwierdzać w KIFF" },
		NewApproverBody: func(phone string) string {
			return phone + " może teraz zatwierdzać w KIFF, odciskiem palca lub twarzą. Zatwierdzono to przed chwilą w Twojej przeglądarce, choć inne Twoje urządzenie już zatwierdza. Jeśli to nie Ty, usuń urządzenie w KIFF i zmień hasło."
		},

		PackAskSubject:  "Prośba o pakiet kredytów tafs",
		PackAskBody:     func(who, pack string) string { return "Prośba od: " + who + " – pakiet kredytów: " + pack + "." },
		RaiseAskSubject: "Prośba o podniesienie limitu wydatków tafs",
		RaiseAskBody: func(who string) string {
			return "Prośba od: " + who + " – podnieś limit wydatków, aby tafs konta mogły dalej działać."
		},
		AskReason: func(reason string) string { return "Powód: " + reason },
		PackAskHow: func(url string) string {
			return "Zatwierdź, dodając pakiet, albo odrzuć, w tafs lub na kiff.dev: " + url
		},
		RaiseAskHow: "Podnieś go albo odrzuć, w tafs.",
	}
}
