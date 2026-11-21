package pages

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// InHouseApps names the apps customers connect in-house: KIFF's own sign-in
// app at the provider, the token kept by planque in the EU (eu-west-1),
// with no connection service in between. It's the owner's decision on app
// connections (17 Nov 2026), point 3. Empty until the first one is live.
//
// /tafs/privacy §6 (Google user data) reads it: while it names no Google
// app, §6 describes the managed path it has always described; once it does,
// §6 describes the in-house one, naming each live Google app's scopes as
// Google's consent screen shows them. An app is added here when it goes
// live for customers, and before Google's verification form is sent.
var InHouseApps = mustAppNames(inHouseAppsJSON)

//go:embed inhouse_apps.json
var inHouseAppsJSON []byte

func mustAppNames(b []byte) []string {
	var apps []string
	if err := json.Unmarshal(b, &apps); err != nil {
		panic("pages: inhouse_apps.json: " + err.Error())
	}
	return apps
}

// googleScope is one scope a Google app asks for, with what it allows, per
// language, in the words §6 uses.
type googleScope struct {
	ID   string
	What map[string]string
}

// googleAppScopes are the scopes each in-house Google app's sign-in asks for,
// as registered in KIFF's Google project and planque's deploy/apps.json: Sheets
// asks for spreadsheets alone, in Google's own words.
var googleAppScopes = map[string][]googleScope{
	"Google Sheets": {
		{"https://www.googleapis.com/auth/spreadsheets", map[string]string{
			"en": "see, edit, create and delete all your spreadsheets in Google Sheets",
			"de": "alle deine Tabellen in Google Sheets sehen, bearbeiten, erstellen und löschen",
			"es": "ver, editar, crear y eliminar todas tus hojas de cálculo de Google Sheets",
			"fr": "voir, modifier, créer et supprimer toutes vos feuilles de calcul Google Sheets",
			"it": "vedere, modificare, creare ed eliminare tutti i tuoi fogli di lavoro di Google Sheets",
			"nl": "al je spreadsheets in Google Sheets zien, bewerken, maken en verwijderen",
			"pl": "widzieć, edytować, tworzyć i usuwać wszystkie Twoje arkusze w Google Sheets",
			"pt": "ver, editar, criar e eliminar todas as suas folhas de cálculo do Google Sheets",
			"ko": "Google Sheets의 모든 스프레드시트 보기, 수정, 만들기, 삭제",
			"ja": "Google Sheetsのすべてのスプレッドシートの参照、編集、作成、削除",
			"et": "kõigi teie Google Sheetsi arvutustabelite vaatamine, muutmine, loomine ja kustutamine",
			"lv": "skatīt, rediģēt, izveidot un dzēst visas jūsu Google Sheets izklājlapas",
			"el": "προβολή, επεξεργασία, δημιουργία και διαγραφή όλων των υπολογιστικών φύλλων σας στα Google Sheets",
		}},
	},
	"Google Calendar": {
		{"https://www.googleapis.com/auth/calendar.events.readonly", map[string]string{
			"en": "see the events on your calendars",
			"de": "die Termine in deinen Kalendern sehen",
			"es": "ver los eventos de tus calendarios",
			"fr": "voir les événements de vos agendas",
			"it": "vedere gli eventi dei tuoi calendari",
			"nl": "de afspraken in je agenda's zien",
			"pl": "widzieć wydarzenia w Twoich kalendarzach",
			"pt": "ver os eventos dos seus calendários",
			"ko": "캘린더의 일정 보기",
			"ja": "カレンダーの予定の参照",
			"et": "teie kalendrite sündmuste vaatamine",
			"lv": "skatīt notikumus jūsu kalendāros",
			"el": "προβολή των συμβάντων στα ημερολόγιά σας",
		}},
		{"https://www.googleapis.com/auth/calendar.events", map[string]string{
			"en": "see and edit the events on your calendars",
			"de": "die Termine in deinen Kalendern sehen und bearbeiten",
			"es": "ver y editar los eventos de tus calendarios",
			"fr": "voir et modifier les événements de vos agendas",
			"it": "vedere e modificare gli eventi dei tuoi calendari",
			"nl": "de afspraken in je agenda's zien en bewerken",
			"pl": "widzieć i edytować wydarzenia w Twoich kalendarzach",
			"pt": "ver e editar os eventos dos seus calendários",
			"ko": "캘린더의 일정 보기 및 수정",
			"ja": "カレンダーの予定の参照と編集",
			"et": "teie kalendrite sündmuste vaatamine ja muutmine",
			"lv": "skatīt un rediģēt notikumus jūsu kalendāros",
			"el": "προβολή και επεξεργασία των συμβάντων στα ημερολόγιά σας",
		}},
	},
}

type shownScope struct{ ID, What string }

type shownGoogleApp struct {
	Name   string
	Scopes []shownScope
}

// googleInHouse is the in-house Google apps §6 names, in InHouseApps' order,
// each with its scopes in lang. Empty while none is live: §6 then keeps its
// managed text.
func googleInHouse(lang string) []shownGoogleApp {
	var out []shownGoogleApp
	for _, name := range InHouseApps {
		scopes, ok := googleAppScopes[name]
		if !ok {
			continue
		}
		app := shownGoogleApp{Name: name}
		for _, s := range scopes {
			what := s.What[lang]
			if what == "" {
				what = s.What["en"]
			}
			app.Scopes = append(app.Scopes, shownScope{ID: s.ID, What: what})
		}
		out = append(out, app)
	}
	return out
}

// googleManaged is the Google apps on ManagedApps, in its order: the ones
// customers connect through Composio, the sign-in partner on /subprocessors.
func googleManaged() []string {
	var out []string
	for _, a := range ManagedApps {
		if isGoogleApp(a) {
			out = append(out, a)
		}
	}
	return out
}

func isGoogleApp(name string) bool {
	return name == "Gmail" || strings.HasPrefix(name, "Google ")
}

// googleInHouseNames is the names googleInHouse shows, in its order.
func googleInHouseNames() []string {
	var out []string
	for _, a := range googleInHouse("en") {
		out = append(out, a.Name)
	}
	return out
}

// googleBoth is true while §6 has a Google app on each route: then every
// item that differs by route names the route, so nothing true of one route
// reads as true of all Google apps.
func googleBoth() bool {
	return len(googleManaged()) > 0 && len(googleInHouse("en")) > 0
}

// appList joins app names as /subprocessors does.
func appList(names []string) string {
	return strings.Join(names, ", ")
}

// googleListed is true while name is on either route's list, so §6 speaks of
// a Google app only while customers can connect it.
func googleListed(name string) bool {
	for _, list := range [][]string{ManagedApps, InHouseApps} {
		for _, a := range list {
			if a == name {
				return true
			}
		}
	}
	return false
}
