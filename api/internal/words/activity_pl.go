package words

// Polish Activity lines (ty), in impersonal forms where a person would take a gender.
func init() {
	why := func(w string) string {
		if w == "" {
			return ""
		}
		return ": " + w
	}
	pl.Activity = Activity{
		ByYou: "przez Ciebie", BySomeone: "przez osobę z konta",
		ByRole:      map[string]string{"admin": "przez administratora", "editor": "przez edytora", "approver": "przez osobę zatwierdzającą", "owner": "przez właściciela"},
		ByOtherRole: func(string) string { return "przez osobę z konta" },

		WaitingForYou:  "Czeka na Ciebie",
		NotSent:        "Nie wysłano",
		NotSentBecause: func(reason string) string { return "Nie wysłano · " + reason },
		ApprovedOnceBy: func(by string) string { return "Zatwierdzone raz " + by },
		ExtraRoomBy:    func(by string) string { return "Dodatkowy zapas przyznany " + by },
		RefusedBy:      func(by string) string { return "Odrzucone " + by },

		AllowedByCard:    "Dozwolone przez Card",
		ApprovedBy:       func(by string) string { return "Zatwierdzone " + by },
		ApprovedThenDone: func(by string) string { return "Zatwierdzone " + by + ", potem wykonane" },
		DoneWithinCard:   "Gotowe, w ramach Card",
		ConnectionRefused: func(w string) string {
			return "Nie wysłano: połączenie odrzuciło je przed wysłaniem" + why(w) + ". Nadal liczy się do dzisiejszych limitów, dopóki KIFF nie potwierdzi, że nic nie zostało zrobione."
		},
		AppRefusedCounts: func(w string) string {
			return "Aplikacja je odrzuciła, a nadal liczy się do dzisiejszych limitów" + why(w)
		},
		AppRefusedMoney: "Aplikacja je odrzuciła. Liczy się do dzisiejszej sumy, dopóki KIFF nie potwierdzi, że żadne pieniądze nie zostały przelane.",
		AppRefusedOther: "Aplikacja je odrzuciła. Liczy się do dzisiejszej sumy, dopóki KIFF nie potwierdzi, że nic nie zostało zrobione.",
		NotSentToTool: func(allowed, w string) string {
			return allowed + ", ale nie udało się go wysłać: nic nie dotarło do narzędzia" + why(w)
		},
		Sending: func(allowed string) string { return allowed + ", i wysłane: czeka na odpowiedź narzędzia" },
		AnswerLost: func(allowed string) string {
			return allowed + ", i wysłane, ale odpowiedź narzędzia zaginęła: sprawdź zapisy samego narzędzia"
		},
		FoundAtVendor: func(allowed string) string {
			return allowed + ", i wysłane. KIFF nie wiedział, jak się zakończyło, więc sprawdził zapisy samego narzędzia i tam je znalazł: przeszło"
		},
		NotRecorded: func(allowed string) string {
			return allowed + ", ale nie zapisano, czy narzędzie to zrobiło: sprawdź zapisy samego narzędzia"
		},
		ConnectionWhy: map[string]string{
			"halt_unknown":           "nie mogło sprawdzić, czy agent był zablokowany",
			"halted":                 "agent lub konto jest zablokowane",
			"halted_before":          "wywołanie zatwierdzono przed ostatnią blokadą",
			"not_granted":            "połączenie nie obejmuje tej akcji",
			"permit_invalid":         "zezwolenie KIFF nie pasowało do wywołania",
			"permit_expired":         "zezwolenie KIFF wygasło, zanim wywołanie wyszło",
			"bad_arguments":          "nie udało się odczytać argumentów wywołania",
			"record_unavailable":     "nie mogło zapisać wywołania",
			"profile_not_applied":    "KIFF nie zastosował jeszcze zatwierdzonych ustawień",
			"definition_invalid":     "akcja wskazuje pole, którego aplikacja nie przyjmuje",
			"definition_unread":      "nie mogło sprawdzić akcji w aplikacji",
			"operation_changed":      "aplikacja zmieniła tę akcję od czasu zatwierdzenia; połączenie nic nie wysyła, dopóki nie zostanie połączone ponownie",
			"signed_out":             "aplikacja zakończyła logowanie; połączenie nic nie wysyła, dopóki nie zalogujesz się ponownie",
			"app_closed":             "ta aplikacja nie jest już oferowana, więc jej połączenie zostało zamknięte",
			"connection_unavailable": "połączenia lub jego konta w aplikacji nie dało się wtedy użyć",
			"tool_not_served":        "ta akcja nigdy nie jest wykonywana dla tej aplikacji",
			"waits_for_key":          "ta akcja wymaga klucza aplikacji, a nie został jeszcze dodany",
			"waits_for_approval":     "ta akcja została dodana do połączenia i nadal czeka na Twoje zatwierdzenie",
		},
	}
}
