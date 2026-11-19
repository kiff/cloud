package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// Every language has both lines, and the list shows exactly the names given.
func TestComposiosRowNamesOnlyTheManagedApps(t *testing.T) {
	for _, lang := range []string{"en", "de", "es", "fr", "it", "nl", "pl", "pt"} {
		w, ok := managedUseWords[lang]
		if !ok || w.None == "" || strings.Count(w.Only, "%s") != 1 {
			t.Fatalf("%s: the managed-use words are missing or malformed", lang)
		}
		saved := ManagedApps
		ManagedApps = nil
		if got := managedUse(lang); got != w.None {
			t.Errorf("%s, none: got %q", lang, got)
		}
		ManagedApps = []string{"Google Sheets", "Slack"}
		if got := managedUse(lang); !strings.Contains(got, "Google Sheets, Slack") || strings.Contains(got, "%s") {
			t.Errorf("%s, two: got %q", lang, got)
		}
		ManagedApps = saved
	}
}

// The disclosed list is a plain JSON array of names, none empty or repeated.
func TestTheManagedAppsFileIsAPlainList(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range mustManagedApps(managedAppsJSON) {
		if strings.TrimSpace(a) == "" || seen[a] {
			t.Errorf("managed_apps.json: an empty or repeated name %q", a)
		}
		seen[a] = true
	}
}

// The shipped list is the owner's 18 Nov decision, in this order, and planque's
// copy is held equal to it by planque's own test.
func TestTheManagedAppsAreTheThreeGoogleApps(t *testing.T) {
	want := []string{"Gmail", "Google Calendar", "Google Sheets"}
	got := mustManagedApps(managedAppsJSON)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("managed_apps.json: got %q, want %q", got, want)
	}
	if line := managedUse("en"); line != "Used only for: Gmail, Google Calendar, Google Sheets." {
		t.Errorf("Composio's English line: got %q", line)
	}
}

var subprocessorsLists = map[string]func() templ.Component{
	"en": subprocessorsList, "de": subprocessorsListDE, "es": subprocessorsListES, "fr": subprocessorsListFR,
	"it": subprocessorsListIT, "nl": subprocessorsListNL, "pl": subprocessorsListPL, "pt": subprocessorsListPT,
}

func renderSubprocessorsList(t *testing.T, lang string) string {
	t.Helper()
	var b bytes.Buffer
	if err := subprocessorsLists[lang]().Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(b.String(), "&#39;", "'")
}

// Every language's "many" line has the count and the link, and a list title.
func TestComposiosManyLineHasCountAndLinkInEveryLanguage(t *testing.T) {
	for lang := range subprocessorsLists {
		w := managedUseWords[lang]
		if strings.Count(w.Many, "%d") != 1 || strings.Count(w.Many, "%s") != 1 || w.Apps == "" {
			t.Errorf("%s: the many-apps words are missing or malformed", lang)
		}
	}
}

// While the list is short, Composio's row names the apps inline and nothing
// is listed under the table.
func TestShortManagedListStaysOnComposiosRow(t *testing.T) {
	saved := ManagedApps
	defer func() { ManagedApps = saved }()
	ManagedApps = []string{"Gmail", "Google Calendar", "Google Sheets"}
	for lang := range subprocessorsLists {
		s := renderSubprocessorsList(t, lang)
		if !strings.Contains(s, ". "+managedUse(lang)+"</td>") || strings.Contains(s, `id="composio-apps"`) || strings.Contains(s, "@composio") {
			t.Errorf("%s: a short list must stay inline on Composio's row", lang)
		}
	}
}

// A long list moves under the table: the row gives the count and links to
// it, and every app is listed once, alphabetically, and nowhere inline.
func TestLongManagedListIsListedUnderTheTable(t *testing.T) {
	saved := ManagedApps
	defer func() { ManagedApps = saved }()
	ManagedApps = []string{"Trello", "Gmail", "airtable", "Notion", "Google Calendar", "Google Sheets", "Asana", "Jira", "Linear", "Dropbox", "ClickUp", "monday"}
	for lang := range subprocessorsLists {
		s := renderSubprocessorsList(t, lang)
		before, _ := managedManyParts(lang)
		if !strings.Contains(s, ". "+before+`<a href="#composio-apps">`) || !strings.Contains(s, `<h3 id="composio-apps">`) || strings.Contains(s, "@composio") {
			t.Errorf("%s: the row doesn't link to the list under the table", lang)
		}
		if !strings.Contains(before, "12") {
			t.Errorf("%s: the row doesn't give the count: %q", lang, before)
		}
		if strings.Contains(s, strings.Join(ManagedApps, ", ")) {
			t.Errorf("%s: the long list is still inline", lang)
		}
		last := -1
		for _, a := range managedAppsSorted() {
			at := strings.Index(s, "<li>"+a+"</li>")
			if at < 0 || at < last || strings.Count(s, "<li>"+a+"</li>") != 1 {
				t.Errorf("%s: %s isn't listed once, in alphabetical order", lang, a)
			}
			last = at
		}
	}
}
