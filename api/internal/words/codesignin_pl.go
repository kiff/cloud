package words

// The code sign-in emails in pl. A draft for the languages' owner to correct.
func init() {
	pl.CodeSignInEmail = CodeSignInEmail{
		CodeSubject:          "Twój kod logowania do KIFF",
		CodeIntro:            "Ktoś poprosił o zalogowanie się do KIFF kodem wysłanym na ten adres. Twój kod:",
		CodeValid:            "Działa raz, przez 10 minut, w przeglądarce, która o niego poprosiła.",
		CodeNeverShare:       "Nikomu nie podawaj tego kodu. KIFF nigdy Cię o niego nie poprosi.",
		CodeNotYou:           "Jeśli to nie Ty, zignoruj tę wiadomość. Bez kodu nikt się nie zaloguje.",
		CodeAskedWhen:        "Kiedy",
		CodeAskedBrowser:     "Przeglądarka",
		CodeAskedPlace:       "Przybliżona lokalizacja",
		CodeOffSubject:       "Logowanie kodem nie jest włączone na Twoim koncie KIFF",
		CodeOffIntro:         "Ktoś poprosił o zalogowanie się do KIFF kodem wysłanym na ten adres. Logowanie kodem nie jest włączone na Twoim koncie, więc nie wysłano żadnego kodu.",
		CodeOffHow:           "Aby z niego korzystać, zaloguj się w inny sposób, a potem włącz „Logowanie kodem wysłanym e-mailem” w ustawieniach logowania swojego konta.",
		CodeOffNotYou:        "Jeśli to nie Ty, zignoruj tę wiadomość. Nic się nie zmieniło.",
		CodeTurnedOnSubject:  "Logowanie kodem jest teraz włączone na Twoim koncie KIFF",
		CodeTurnedOnIntro:    "Na Twoim koncie KIFF włączono logowanie kodem wysłanym e-mailem.",
		CodeTurnedOffSubject: "Logowanie kodem jest teraz wyłączone na Twoim koncie KIFF",
		CodeTurnedOffIntro:   "Na Twoim koncie KIFF wyłączono logowanie kodem wysłanym e-mailem.",
		CodeChangedNotYou:    "Jeśli to nie Ty, zaloguj się w inny sposób, sprawdź ustawienia logowania swojego konta i wyloguj urządzenia, których nie znasz.",
		CodeWhy:              "KIFF wysyła tę wiadomość, gdy ktoś prosi o zalogowanie się kodem wysłanym na ten adres.",
	}
}
