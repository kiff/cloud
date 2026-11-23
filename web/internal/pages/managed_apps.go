package pages

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// ManagedApps names the apps a customer may connect as a managed connection,
// whose sign-in Composio holds in the United States. It's the disclosure the
// rule for app connections asks for: no customer's
// connection goes through Composio for an app that isn't named here.
// /subprocessors shows it on Composio's row. Since 18 Nov 2026: Gmail,
// Google Calendar and Google Sheets (the owner's yes, pre-launch).
//
// The list lives in managed_apps.json, a JSON array of app names as people
// know them ("Google Sheets"), so planque can keep its own copy equal to it
// by a test, and refuse a managed sign-in for any app not on it. An app is
// added here, with the page's words, before planque lets it through.
var ManagedApps = mustManagedApps(managedAppsJSON)

//go:embed managed_apps.json
var managedAppsJSON []byte

func mustManagedApps(b []byte) []string {
	var apps []string
	if err := json.Unmarshal(b, &apps); err != nil {
		panic("pages: managed_apps.json: " + err.Error())
	}
	return apps
}

// managedUseWords are the line on Composio's row, per language: None when
// ManagedApps is empty, Only with %s for the names while the list is short,
// and Many, with %d for the count and %s for the link to the list below the
// table, once it's longer than managedAppsInline. Apps names the list's
// heading and the link's words.
var managedUseWords = map[string]struct{ None, Only, Many, Apps string }{
	"en": {"Not yet used for any customer's connection.", "Used only for: %s.", "Used only for the %d apps listed under %s.", "Apps connected through Composio"},
	"de": {"Noch für keine Verbindung eines Kunden genutzt.", "Nur genutzt für: %s.", "Nur genutzt für die %d Apps unter %s.", "Über Composio verbundene Apps"},
	"es": {"Aún no se usa para ninguna conexión de un cliente.", "Solo se usa para: %s.", "Solo se usa para las %d apps de %s.", "Apps conectadas a través de Composio"},
	"fr": {"Pas encore utilisé pour la connexion d'un client.", "Utilisé uniquement pour : %s.", "Utilisé uniquement pour les %d apps de la liste %s.", "Apps connectées via Composio"},
	"it": {"Non ancora usato per il collegamento di alcun cliente.", "Usato solo per: %s.", "Usato solo per le %d app elencate in %s.", "App collegate tramite Composio"},
	"nl": {"Nog voor geen enkele koppeling van een klant gebruikt.", "Alleen gebruikt voor: %s.", "Alleen gebruikt voor de %d apps onder %s.", "Apps gekoppeld via Composio"},
	"pl": {"Jeszcze nieużywany do żadnego połączenia klienta.", "Używany wyłącznie do: %s.", "Używany wyłącznie do %d aplikacji z listy %s.", "Aplikacje połączone przez Composio"},
	"pt": {"Ainda não é usado para a ligação de nenhum cliente.", "Usado apenas para: %s.", "Usado apenas para as %d apps listadas em %s.", "Apps ligadas através da Composio"},
}

// managedAppsInline is the longest list Composio's row names inline. A longer
// one is listed under the table, alphabetically, and the row links to it.
const managedAppsInline = 8

func managedWords(lang string) struct{ None, Only, Many, Apps string } {
	w, ok := managedUseWords[lang]
	if !ok {
		w = managedUseWords["en"]
	}
	return w
}

// managedAppsListed is true when the managed apps are listed under the table.
func managedAppsListed() bool { return len(ManagedApps) > managedAppsInline }

// managedManyParts are Many's words around the link, with the count in.
func managedManyParts(lang string) (before, after string) {
	w := managedWords(lang)
	line := strings.Replace(w.Many, "%d", strconv.Itoa(len(ManagedApps)), 1)
	before, after, _ = strings.Cut(line, "%s")
	return before, after
}

// managedAppsSorted is ManagedApps in alphabetical order, ignoring case.
func managedAppsSorted() []string {
	apps := slices.Clone(ManagedApps)
	slices.SortFunc(apps, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return apps
}

// managedUse is Composio's "used only for" line in lang, from ManagedApps.
func managedUse(lang string) string {
	w := managedWords(lang)
	if len(ManagedApps) == 0 {
		return w.None
	}
	return strings.Replace(w.Only, "%s", strings.Join(ManagedApps, ", "), 1)
}
