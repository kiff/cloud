package pages

import (
	"bytes"
	"context"
	"slices"
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
		saved, savedAll := ManagedApps, ManagedAll
		ManagedApps, ManagedAll = nil, false
		if got := managedUse(lang); got != w.None {
			t.Errorf("%s, none: got %q", lang, got)
		}
		ManagedApps = []string{"Google Sheets", "Slack"}
		if got := managedUse(lang); !strings.Contains(got, "Google Sheets, Slack") || strings.Contains(got, "%s") {
			t.Errorf("%s, two: got %q", lang, got)
		}
		ManagedApps, ManagedAll = saved, savedAll
	}
}

// The disclosed file names no app twice and no empty name, in either form.
func TestTheManagedAppsFileNamesEachAppOnce(t *testing.T) {
	m := mustManagedList(managedAppsJSON)
	for _, list := range [][]string{m.Named, m.Except} {
		seen := map[string]bool{}
		for _, a := range list {
			if strings.TrimSpace(a) == "" || seen[a] {
				t.Errorf("managed_apps.json: an empty or repeated name %q", a)
			}
			seen[a] = true
		}
	}
	for _, a := range m.Except {
		if slices.Contains(m.Named, a) {
			t.Errorf("managed_apps.json: %q is both named and excepted", a)
		}
	}
}

// Both forms read, the all mode with or without exceptions; an object that
// isn't the all mode, or has an unknown field, doesn't.
func TestTheManagedAppsFileReadsInEitherForm(t *testing.T) {
	if m := mustManagedList([]byte(`["Gmail", "Notion"]`)); m.All || strings.Join(m.Named, "|") != "Gmail|Notion" || m.Except != nil {
		t.Errorf("a list reads as the list mode: %+v", m)
	}
	if m := mustManagedList([]byte(`{"all": true, "named": ["Gmail"], "except": ["Gusto"]}`)); !m.All || m.Named[0] != "Gmail" || m.Except[0] != "Gusto" {
		t.Errorf("an object reads as the all mode: %+v", m)
	}
	for _, good := range []string{`{"all": true}`, `{"all": true, "except": []}`} {
		if m := mustManagedList([]byte(good)); !m.All || len(m.Except) != 0 {
			t.Errorf("%s reads as the all mode with nothing excepted: %+v", good, m)
		}
	}
	for _, bad := range []string{`{"all": false, "except": ["Gusto"]}`, `{"all": true, "except": ["Gusto"], "extra": 1}`, `{}`} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s should not read", bad)
				}
			}()
			mustManagedList([]byte(bad))
		}()
	}
}

// The shipped file is the all mode with nothing excepted: any app a customer
// connects through Composio. It still names the three
// Google apps and wave 1a's 15, which other pages speak of by name. planque's
// copy is held within it by .github/workflows/managed-apps.yml.
func TestTheManagedAppsAreTheShippedList(t *testing.T) {
	want := []string{"Gmail", "Google Calendar", "Google Sheets",
		"Airtable", "Asana", "Cal", "Calendly", "ClickUp", "Confluence", "Dropbox", "Excel",
		"Jira", "Linear", "Monday", "Notion", "OneDrive", "Trello", "Wrike"}
	m := mustManagedList(managedAppsJSON)
	if !m.All || len(m.Except) != 0 {
		t.Fatalf("managed_apps.json: want the all mode with nothing excepted, got %+v", m)
	}
	if strings.Join(m.Named, "|") != strings.Join(want, "|") {
		t.Fatalf("managed_apps.json: named %q, want %q", m.Named, want)
	}
	if managedAppsListed() {
		t.Error("in the all mode the named apps aren't listed under the table")
	}
}

// In the all mode with nothing excepted, Composio's row says any service, and
// nothing is listed under the table, in every language.
func TestTheAllModeWithNoExceptionsListsNothing(t *testing.T) {
	saved, savedAll, savedExcept := ManagedApps, ManagedAll, ManagedExcept
	defer func() { ManagedApps, ManagedAll, ManagedExcept = saved, savedAll, savedExcept }()
	ManagedApps, ManagedAll, ManagedExcept = []string{"Gmail", "Notion"}, true, nil
	for lang := range subprocessorsLists {
		w := managedUseWords[lang]
		if w.AllNone == "" || strings.Contains(w.AllNone, "%") {
			t.Fatalf("%s: the all-mode line is missing or malformed", lang)
		}
		s := renderSubprocessorsList(t, lang)
		if !strings.Contains(s, ". "+w.AllNone+"</td>") {
			t.Errorf("%s: the row doesn't say any service", lang)
		}
		if strings.Contains(s, `id="composio-except"`) || strings.Contains(s, `id="composio-apps"`) || strings.Contains(s, "<li>Notion</li>") || strings.Contains(s, "@composio") {
			t.Errorf("%s: nothing should be listed under the table", lang)
		}
	}
}

// In the all mode Composio's row says any service, links to the exceptions,
// and lists each exception once, alphabetically, in every language; no
// named app is listed as if it were the only ones.
func TestTheAllModeListsOnlyTheExceptions(t *testing.T) {
	saved, savedAll, savedExcept := ManagedApps, ManagedAll, ManagedExcept
	defer func() { ManagedApps, ManagedAll, ManagedExcept = saved, savedAll, savedExcept }()
	ManagedApps, ManagedAll, ManagedExcept = []string{"Gmail", "Notion"}, true, []string{"Rippling", "BambooHR", "Gusto"}
	for lang := range subprocessorsLists {
		w := managedUseWords[lang]
		if strings.Count(w.All, "%s") != 1 || w.Except == "" {
			t.Fatalf("%s: the all-mode words are missing or malformed", lang)
		}
		s := renderSubprocessorsList(t, lang)
		before, after := managedAllParts(lang)
		if !strings.Contains(s, ". "+before+`<a href="#composio-except">`+w.Except+"</a>"+after) || !strings.Contains(s, `<h3 id="composio-except">`+w.Except+"</h3>") {
			t.Errorf("%s: the row doesn't link to the exceptions under the table", lang)
		}
		if strings.Contains(s, `id="composio-apps"`) || strings.Contains(s, "<li>Notion</li>") || strings.Contains(s, "@composio") {
			t.Errorf("%s: the all mode shouldn't list the named apps", lang)
		}
		last := -1
		for _, a := range managedExceptSorted() {
			at := strings.Index(s, "<li>"+a+"</li>")
			if at < 0 || at < last || strings.Count(s, "<li>"+a+"</li>") != 1 {
				t.Errorf("%s: %s isn't listed once, in alphabetical order", lang, a)
			}
			last = at
		}
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
	saved, savedAll := ManagedApps, ManagedAll
	defer func() { ManagedApps, ManagedAll = saved, savedAll }()
	ManagedApps, ManagedAll = []string{"Gmail", "Google Calendar", "Google Sheets"}, false
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
	saved, savedAll := ManagedApps, ManagedAll
	defer func() { ManagedApps, ManagedAll = saved, savedAll }()
	ManagedAll = false
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
