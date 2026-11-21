package words

import "fmt"

// Polish (Ty), in the glossary's terms.
var pl = Words{
	DecimalComma: true,

	WindowNextApproval:     "Twój odcisk palca lub twarz przy następnym zatwierdzeniu",
	WindowAgainUnderMinute: "Odcisk palca lub twarz ponownie za mniej niż minutę",
	WindowNoneNextMinute:   "Bez odcisku palca ani twarzy przez następną minutę",
	WindowNoneMinutes: func(n int) string {
		if plFew(n) {
			return fmt.Sprintf("Bez odcisku palca ani twarzy przez następne %d minuty", n)
		}
		return fmt.Sprintf("Bez odcisku palca ani twarzy przez następne %d minut", n)
	},

	Today:     "dziś",
	Yesterday: "wczoraj",
	Months:    [12]string{"sty", "lut", "mar", "kwi", "maj", "cze", "lip", "sie", "wrz", "paź", "lis", "gru"},

	HappenedYes:           "Tak, przeszło",
	HappenedYesDetail:     "Nadal liczy się na Card.",
	HappenedNo:            "Nie, nie przeszło",
	HappenedNoDetail:      "Card odzyskuje swój zapas. U dostawcy nic nie jest cofane.",
	HappenedNotSure:       "Nie jestem pewien, pomóż mi sprawdzić",
	HappenedNotSureDetail: "Coach sprawdzi z Tobą historię dostawcy. Pytanie zostaje u Ciebie na jeden dzień.",
	HappenedNoAfter:       "Card odzyskała swój zapas. U dostawcy nic nie zostało cofnięte: sprawdź tam, czy płatność jednak przeszła, a jeśli tak, powiedz o tym Coachowi.",
	WhyLost: func(vendor string, read bool) string {
		of := "dostawcy"
		if vendor != "" {
			of = vendor
		}
		if read {
			return "Wywołanie zostało wysłane, ale jego odpowiedź zaginęła, a odczyty KIFF z historii " + of + " jeszcze tego nie rozstrzygnęły."
		}
		return "Wywołanie zostało wysłane, ale jego odpowiedź zaginęła, a w historii " + of + " nie można wyszukać tego wywołania."
	},
	WhyRefused: func(vendor string, read bool) string {
		if vendor == "" {
			vendor = "dostawca"
		}
		if read {
			return Capitalize(vendor) + " odrzucił je, nie mówiąc, czy coś pobrano, a odczyty KIFF z jego historii jeszcze tego nie rozstrzygnęły."
		}
		return Capitalize(vendor) + " odrzucił je, nie mówiąc, czy coś pobrano, a w jego historii nie można wyszukać tego wywołania."
	},
	WhyProbation: "Jedno z pierwszych wywołań połączenia, którego KIFF nie wypróbował jeszcze na koncie testowym: potwierdź, co się stało.",
	StatusRefused: func(vendor string) string {
		if vendor == "" {
			return "Dostawca je odrzucił. Sprawdź u dostawcy, czy pobrano jakieś pieniądze."
		}
		return Capitalize(vendor) + " odrzucił je. Sprawdź w " + vendor + ", czy pobrano jakieś pieniądze."
	},
	StatusLost: func(vendor string) string {
		if vendor == "" {
			return "Zostało wysłane do dostawcy, ale odpowiedź zginęła. Sprawdź u dostawcy, czy przeszło."
		}
		return "Zostało wysłane do " + vendor + ", ale odpowiedź zginęła. Sprawdź w " + vendor + ", czy przeszło."
	},
	StatusProbation: "Jedno z pierwszych wywołań przez to połączenie: sprawdź, czy zrobiło to, co miało zrobić.",
	CallAtVendor:    func(action, vendor string) string { return action + " w " + vendor },
	CallByTaf:       func(taf string) string { return "przez " + taf },

	Reasons: map[string]string{
		"mandate_owner_rejected":       "Zostało odrzucone przez Ciebie.",
		"mandate_owner_expired":        "Nikt nie odpowiedział na czas.",
		"mandate_withdrawn":            "Zostało wycofane, zanim ktoś odpowiedział.",
		"mandate_per_action_exceeded":  "Przekraczało limit Card dla jednego wywołania.",
		"mandate_limit_reached":        "Przekraczało jedną z sum Card.",
		"mandate_resource_not_on_card": "Dotyczyło czegoś, czego Card nie obejmuje.",
		"mandate_unreadable_resource":  "KIFF nie mógł odczytać, czego dotyczyło.",
		"mandate_not_covered":          "Żadna Card nie obejmuje tego narzędzia.",
		"mandate_expired":              "Jego Card wygasła.",
		"no_mandate_issued":            "Żadna Card nie obejmuje tego narzędzia.",
		"amount_unit_mismatch":         "Było w innej walucie niż waluta Card.",
		"amount_unit_unverified":       "KIFF nie mógł sprawdzić waluty.",
		"amount_unverified":            "KIFF nie mógł odczytać kwoty.",
		"halted":                       "Było zablokowane przez Ciebie.",
		"halt_unavailable":             "KIFF nie mógł sprawdzić, czy było zablokowane.",
	},
	OwnerAsks: map[string]string{
		OwnerAskSealedMoney:  "to narzędzie przenosi pieniądze przez połączenie, do którego klucz ma KIFF, więc każde wywołanie czeka na zatwierdzenie",
		OwnerAskSecondRefund: "ta płatność ma już zwrot, więc drugi czeka na zatwierdzenie",
		OwnerAskNoRequestID:  "agent nie podał żadnej prośby z połączonej aplikacji, więc ten zwrot czeka na zatwierdzenie",
		OwnerAskOther:        "każde wywołanie tego narzędzia czeka na zatwierdzenie",
		OwnerAskEachCall:     "każde wywołanie tego narzędzia czeka na zatwierdzenie",
	},
	ResourceMissing: func(param string) string {
		return fmt.Sprintf("wywołanie nie wskazuje żadnego %s, a ta Card dotyczy tylko wymienionych w niej %s", param, param)
	},
	ResourceNotOnCard: func(param, value string) string {
		return fmt.Sprintf("%s %q nie należy do tych, które obejmuje ta Card", param, value)
	},
	OverPerCall: func(draw, limit string) string {
		return fmt.Sprintf("%s przekracza limit %s na jedno wywołanie", draw, limit)
	},
	OverTotal: func(draw, limit, used string) string {
		return fmt.Sprintf("%s przekroczyłoby limit %s: wykorzystano już %s", draw, limit, used)
	},

	// The held-call and Card-warning emails, and a warning's reason.
	HeldSubject:     "Twój agent czeka na Twoją odpowiedź",
	HeldNothingDone: "Nic nie zostało zrobione.",
	HeldOpen:        "Otwórz to w KIFF, aby odpowiedzieć (zostaniesz poproszony o zalogowanie):",
	HeldOptions:     "Możesz to zatwierdzić raz, zmienić Card, aby na to pozwalała, lub to odrzucić.",
	HeldIntro: func(agent, action, amount string) string {
		return agent + " chce wykonać " + action + amount + ", co wykracza poza jego Card."
	},
	HeldSteps: func(steps string) string {
		return "Card pozwala też odrzucić to z następnym krokiem dla agenta: " + steps + "."
	},
	HeldExpires: func(when string) string {
		return "Jeśli nikt nie odpowie do " + when + ", zostanie to odrzucone i agent będzie musiał zapytać ponownie."
	},
	HeldNoApprove:   "Z tego e-maila nie można niczego zatwierdzić.",
	HeldButtonsNote: "Każdy przycisk otwiera tę akcję w KIFF, gdzie logujesz się i potwierdzasz odpowiedź.",
	HeldApproveOnce: "Zatwierdź raz",
	HeldChangeCard:  "Zmień Card, aby na to pozwalała",
	HeldReject:      "Odrzuć",
	WarnSpent:       "Ta suma Card została wyczerpana.",
	WarnBurst:       "Nowa Card zużyła więcej niż jej próg skoku na początku tego okresu.",
	WarnRunway:      "W ostatnim tempie ta suma Card wkrótce się wyczerpie.",
	WarnUnusual: func(ratio string) string {
		return "Ostatnie użycie to " + ratio + "× jego równoważnej średniej z siedmiu dni."
	},
	WarnUsed:         func(pct, period string) string { return "Wykorzystano " + pct + "% tej sumy " + period + "." },
	Periods:          map[string]string{"calendar_day": "dziś", "rolling_1h": "w ostatniej godzinie", "rolling_24h": "w ostatnich 24 godzinach", "rolling_7d": "w ostatnich 7 dniach", "lifetime": "do tej pory"},
	WarnNeverResets:  "Ta suma nigdy się nie resetuje. Dodaj zapas, zmień Card lub wstrzymaj jej agenta.",
	WarnReviewWindow: "Sprawdź zapas tego okresu, zmień Card lub wstrzymaj jej agenta.",
	WarnSubject:      "KIFF: ostrzeżenie o budżecie Card",
	WarnReview:       "Zobacz Card",
}
