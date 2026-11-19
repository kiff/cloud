package pages

// watchPL is /tafs/watch's words in Polish, at /pl/tafs/watch. The watch's
// own lines are quoted as tafs says them in Polish.
var watchPL = watchWords{
	Lang:        "pl",
	Title:       "tafs na Twoim zegarku — KIFF",
	Description: "Zobacz, co czeka na Ciebie, odrzuć oczekujące wywołanie, zablokuj tafa i zapytaj Coacha z zegarka. Zatwierdzanie zostaje na telefonie.",
	Badge:       "tafs · zegarek",
	Head:        "tafs na Twoim zegarku",
	Sub:         "Zobacz, co czeka na Ciebie, odrzuć oczekujące wywołanie, zablokuj tafa i zapytaj Coacha, prosto z nadgarstka. Zatwierdzanie zostaje na telefonie.",
	Does: watchSection{
		Head: "1. Co robi",
		Items: []string{
			"**Czeka na Ciebie:** ile oczekujących wywołań czeka na Ciebie, i każde tak, jak pokazuje je telefon: jego taf, czego chce, kwota i dlaczego.",
			"**Twoje tafs:** stan każdego tafa w jednym wierszu.",
			"**Odrzuć** oczekujące wywołanie, **Zablokuj teraz** jednego tafa albo **Zablokuj wszystko teraz**, co zegarek każe Ci potwierdzić jeszcze raz.",
			"**Zapytaj Coacha** głosem.",
			"**Karta** z liczbą tego, ile czeka na Ciebie.",
			"**Zatwierdzanie nigdy nie odbywa się na zegarku.** Przy oczekującym wywołaniu, Card, płatności lub połączeniu zegarek mówi „Zatwierdź na telefonie” i otwiera je tam.",
		},
	},
	Get: watchSection{
		Head: "2. Jak go zdobyć",
		Items: []string{
			"**Wear OS: Wkrótce w Google Play.** Wear OS 3 lub nowszy. Aplikacja na zegarek jest dostarczana razem z tafs na Androida: gdy będzie w Google Play, zainstaluj ją z Google Play na zegarku.",
			"**Apple Watch: Wkrótce.** Będzie dostarczana razem z tafs na iPhone'a i będzie robić to samo, z zatwierdzaniem na Twoim iPhonie.",
			"**Bez logowania na zegarku.** Korzysta z tafs na Twoim telefonie, który musi być zalogowany i w pobliżu.",
		},
	},
	Act: watchSection{
		Head: "3. Odrzucanie i blokowanie",
		Items: []string{
			"**Twój zegarek potrzebuje blokady ekranu i musi być odblokowany.** Inaczej mówi: „Aby odrzucać lub blokować z tego zegarka, ustaw blokadę ekranu i odblokuj go.”",
			"**Robi to Twój telefon,** własnym kluczem, a zegarek pokazuje wynik, gdy jest gotowe, na przykład „Odrzucono. Nic nie zostało wysłane.”",
			"Jeśli ktoś w międzyczasie odpowiedział na wywołanie, zegarek mówi „Już odpowiedziano albo zakończono.”",
		},
	},
	Coach: watchSection{
		Head: "4. Zapytaj Coacha",
		Items: []string{
			"Dotknij **Zapytaj Coacha** i mów. Wprowadzanie głosowe Twojego zegarka zamienia to, co mówisz, na tekst, a zegarek pokazuje go, zanim cokolwiek zostanie wysłane: **Wyślij** lub **Anuluj**.",
			"**Coach dostaje Twoje słowa jako tekst, nigdy Twój głos.**",
			"Najpierw zezwól na użycie AI w tafs na telefonie i uruchom tam Coacha.",
			"Krótka odpowiedź jest pokazywana na zegarku. Długa albo taka, na którą trzeba coś odpowiedzieć, pokazuje swój początek i **Otwórz na telefonie**.",
			"Tak jak przy odrzucaniu i blokowaniu, Twój zegarek potrzebuje blokady ekranu i musi być odblokowany.",
		},
	},
	Locked: watchSection{
		Head: "5. Gdy Twój zegarek jest zablokowany",
		Items: []string{
			"Karta pokazuje tylko „tafs”, a alert tylko „Taf czeka na Ciebie”. Wear OS blokuje zegarek, gdy zdejmiesz go z nadgarstka.",
			"Po odblokowaniu alert pokazuje to, co alert na telefonie: którego tafa dotyczy i czego chce, co może wymieniać kwotę, sprzedawcę lub osobę.",
			"Na Apple Watch alerty pochodzą z Twojego iPhone'a, więc to, co pokazują, zależy od ustawienia podglądów na iPhonie. Gdy podglądy są ukryte, a tak iPhone je ustawia, gdy jest zablokowany, chyba że to zmienisz, alert pokazuje tylko „Taf czeka na Ciebie”.",
		},
	},
	Keeps: watchSection{
		Head: "6. Co przechowuje",
		Items: []string{
			"Tylko to, ile czeka na Ciebie, dla karty. Nigdy nie jest pokazywane, gdy zegarek jest zablokowany, i wtedy jest usuwane, tak jak po wylogowaniu się na telefonie.",
			"Bez logowania, bez kluczy. To, co pokazuje, jest zapominane, gdy go odłożysz, i nic z tego, co mówisz, nie jest przechowywane.",
		},
		Link: watchLink{Before: "Szczegóły są w ", Text: "polityce prywatności tafs", After: "."},
	},
	Help: watchSection{
		Head: "7. Gdy coś jest nie tak",
		Items: []string{
			"„Telefon jest poza zasięgiem. Przybliż go i spróbuj ponownie.”",
			"„Najpierw zaloguj się do tafs na telefonie.”",
		},
		Link: watchLink{Before: "Wszystko inne: **Otwórz na telefonie** albo ", Text: "napisz do nas", After: "."},
	},
}
