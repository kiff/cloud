package pages

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// ManagedApps names the apps a customer may connect as a managed connection,
// whose sign-in Composio holds in the United States. It's the disclosure the
// rule for app connections asks for: no customer's
// connection goes through Composio for an app that isn't named here, unless
// ManagedAll says every app may, except ManagedExcept. /subprocessors shows it
// on Composio's row.
//
// The list lives in managed_apps.json, so planque can keep its own copy equal
// to it by a test, and refuse a managed sign-in for any app it doesn't allow.
// The file is either a JSON array of app names as people know them ("Google
// Sheets"), the list mode, or an object {"all": true, "named": [...],
// "except": [...]}, the all mode: any app a customer connects through
// Composio, except the ones in "except" (none, or a single app withdrawn). "named" keeps the apps other pages
// speak of by name (the Google section of the tafs privacy policy). An app is
// allowed here, with the page's words, before planque lets it through.
var ManagedApps, ManagedAll, ManagedExcept = func() ([]string, bool, []string) {
	m := mustManagedList(managedAppsJSON)
	return m.Named, m.All, m.Except
}()

//go:embed managed_apps.json
var managedAppsJSON []byte

// managedList is managed_apps.json read in either form.
type managedList struct {
	All    bool     `json:"all"`
	Named  []string `json:"named"`
	Except []string `json:"except"`
}

func mustManagedList(b []byte) managedList {
	var m managedList
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '[' {
		if err := json.Unmarshal(t, &m.Named); err != nil {
			panic("pages: managed_apps.json: " + err.Error())
		}
		return m
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		panic("pages: managed_apps.json: " + err.Error())
	}
	if !m.All {
		panic(`pages: managed_apps.json: an object must say "all": true`)
	}
	return m
}

// mustManagedApps is the named apps in managed_apps.json, in either form.
func mustManagedApps(b []byte) []string { return mustManagedList(b).Named }

// managedUseWords are the line on Composio's row, per language: None when
// ManagedApps is empty, Only with %s for the names while the list is short,
// and Many, with %d for the count and %s for the link to the list below the
// table, once it's longer than managedAppsInline. Apps names the list's
// heading and the link's words. In the all mode, AllNone is the row's line
// while nothing is excepted; with exceptions, All is the line, with %s for the
// link to the exceptions listed under the table, and Except names that list's
// heading and the link's words.
type managedLines struct{ None, Only, Many, Apps, All, Except, AllNone string }

var managedUseWords = map[string]managedLines{
	"en": {"Not yet used for any customer's connection.", "Used only for: %s.", "Used only for the %d apps listed under %s.", "Apps connected through Composio", "Used for any service a customer connects through it, except those listed under %s.", "Services never connected through Composio", "Used for any service a customer connects through it."},
	"de": {"Noch für keine Verbindung eines Kunden genutzt.", "Nur genutzt für: %s.", "Nur genutzt für die %d Apps unter %s.", "Über Composio verbundene Apps", "Genutzt für jeden Dienst, den ein Kunde darüber verbindet, außer denen unter %s.", "Dienste, die nie über Composio verbunden werden", "Genutzt für jeden Dienst, den ein Kunde darüber verbindet."},
	"es": {"Aún no se usa para ninguna conexión de un cliente.", "Solo se usa para: %s.", "Solo se usa para las %d apps de %s.", "Apps conectadas a través de Composio", "Se usa para cualquier servicio que un cliente conecte a través de él, salvo los de %s.", "Servicios que nunca se conectan a través de Composio", "Se usa para cualquier servicio que un cliente conecte a través de él."},
	"fr": {"Pas encore utilisé pour la connexion d'un client.", "Utilisé uniquement pour : %s.", "Utilisé uniquement pour les %d apps de la liste %s.", "Apps connectées via Composio", "Utilisé pour tout service qu'un client connecte par son intermédiaire, sauf ceux de la liste %s.", "Services jamais connectés via Composio", "Utilisé pour tout service qu'un client connecte par son intermédiaire."},
	"it": {"Non ancora usato per il collegamento di alcun cliente.", "Usato solo per: %s.", "Usato solo per le %d app elencate in %s.", "App collegate tramite Composio", "Usato per qualsiasi servizio che un cliente collega tramite esso, tranne quelli elencati in %s.", "Servizi mai collegati tramite Composio", "Usato per qualsiasi servizio che un cliente collega tramite esso."},
	"nl": {"Nog voor geen enkele koppeling van een klant gebruikt.", "Alleen gebruikt voor: %s.", "Alleen gebruikt voor de %d apps onder %s.", "Apps gekoppeld via Composio", "Gebruikt voor elke dienst die een klant erlangs koppelt, behalve die onder %s.", "Diensten die nooit via Composio worden gekoppeld", "Gebruikt voor elke dienst die een klant erlangs koppelt."},
	"pl": {"Jeszcze nieużywany do żadnego połączenia klienta.", "Używany wyłącznie do: %s.", "Używany wyłącznie do %d aplikacji z listy %s.", "Aplikacje połączone przez Composio", "Używany do każdej usługi, którą klient łączy za jego pośrednictwem, z wyjątkiem tych z listy %s.", "Usługi nigdy nie łączone przez Composio", "Używany do każdej usługi, którą klient łączy za jego pośrednictwem."},
	"pt": {"Ainda não é usado para a ligação de nenhum cliente.", "Usado apenas para: %s.", "Usado apenas para as %d apps listadas em %s.", "Apps ligadas através da Composio", "Usado para qualquer serviço que um cliente ligue através dele, exceto os listados em %s.", "Serviços nunca ligados através da Composio", "Usado para qualquer serviço que um cliente ligue através dele."},
}

// managedAppsInline is the longest list Composio's row names inline. A longer
// one is listed under the table, alphabetically, and the row links to it.
const managedAppsInline = 8

func managedWords(lang string) managedLines {
	w, ok := managedUseWords[lang]
	if !ok {
		w = managedUseWords["en"]
	}
	return w
}

// managedAppsListed is true when the managed apps are listed under the table.
func managedAppsListed() bool { return !ManagedAll && len(ManagedApps) > managedAppsInline }

// managedAllParts are the all mode's words around the link to the exceptions.
func managedAllParts(lang string) (before, after string) {
	before, after, _ = strings.Cut(managedWords(lang).All, "%s")
	return before, after
}

// managedExceptSorted is ManagedExcept in alphabetical order, ignoring case.
func managedExceptSorted() []string {
	apps := slices.Clone(ManagedExcept)
	slices.SortFunc(apps, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return apps
}

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
