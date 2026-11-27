package pages

import (
	"strings"
	"testing"
)

// A pending app is a Google app §6 knows the scopes of, and customers can
// connect it today through the sign-in partner: its line says "until then,
// through our sign-in partner listed above", so it must be on that list.
func TestShippedPendingAppsAreManagedGoogleApps(t *testing.T) {
	names := mustAppNames(pendingAppsJSON)
	for _, n := range names {
		if _, ok := googleAppScopes[n]; !ok {
			t.Errorf("inhouse_pending.json names %q: use a key of googleAppScopes", n)
		}
		if !googleListed(n) || !containsName(ManagedApps, n) {
			t.Errorf("inhouse_pending.json names %q, which isn't on managed_apps.json: §6's \"until then\" would be false", n)
		}
	}
}

func containsName(list []string, name string) bool {
	for _, a := range list {
		if a == name {
			return true
		}
	}
	return false
}

// Every language of §6 has its own words for a pending app, not English.
func TestEveryLanguageHasPendingWords(t *testing.T) {
	for lang := range googleSections {
		if _, ok := googlePendingText[lang]; !ok {
			t.Errorf("%s: no pending words in googlePendingText", lang)
		}
	}
	if _, ok := googlePendingText["pt-br"]; !ok {
		t.Error("pt-br: no pending words in googlePendingText")
	}
}

// While Google Sheets' own sign-in is in Google's review, §6 keeps it
// disclosed with the access it will ask for, says customers connect through
// the sign-in partner until then, and says nothing that's true only of a live
// in-house route: no "no other company", no token kept in AWS KMS.
func TestGoogleSectionKeepsThePendingSignIn(t *testing.T) {
	saved, savedManaged, savedPending := InHouseApps, ManagedApps, PendingInHouseApps
	defer func() { InHouseApps, ManagedApps, PendingInHouseApps = saved, savedManaged, savedPending }()
	ManagedApps = []string{"Gmail", "Google Calendar", "Google Sheets"}
	InHouseApps = nil
	PendingInHouseApps = []string{"Google Sheets"}
	for lang := range googleSections {
		s := renderGoogleSection(t, lang)
		w, p := googleRouteWords[lang], googlePendingWords(lang)
		for _, want := range []string{": Gmail, Google Calendar, Google Sheets.</li>", "Google Sheets" + googlePendingSep(lang) + p.Line, p.AsksFor, "auth/spreadsheets</code>", limitedUse} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: §6 lacks %q", lang, want)
			}
		}
		for _, not := range []string{w.NoOtherCompany, "AWS KMS", "calendar.events"} {
			if strings.Contains(s, not) {
				t.Errorf("%s: §6 says %q while no Google app is live in-house", lang, not)
			}
		}
	}
}

// Once the app goes live in-house, it's no longer pending: §6 describes the
// live route and drops the "once Google has verified it" line.
func TestPendingLineGoesOnceTheAppIsLive(t *testing.T) {
	saved, savedManaged, savedPending := InHouseApps, ManagedApps, PendingInHouseApps
	defer func() { InHouseApps, ManagedApps, PendingInHouseApps = saved, savedManaged, savedPending }()
	ManagedApps = []string{"Gmail", "Google Calendar"}
	InHouseApps = []string{"Google Sheets"}
	PendingInHouseApps = []string{"Google Sheets"}
	for lang := range googleSections {
		s := renderGoogleSection(t, lang)
		if strings.Contains(s, googlePendingWords(lang).Line) {
			t.Errorf("%s: §6 still calls Google Sheets pending once it's live", lang)
		}
	}
}
