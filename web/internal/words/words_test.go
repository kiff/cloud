package words

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var verb = regexp.MustCompile(`%[sd]`)

// sameEverywhere are the words a language may share with English ("Card" is
// KIFF's name for it in every language).
var sameEverywhere = map[string]bool{"Code": true, "Admin": true, "Editor": true, "Card": true}

// checkCatalog checks every language has every line of English, with
// English's values in English's order, and none left in English.
func checkCatalog[T any](t *testing.T, name string, catalog map[string]T) {
	t.Helper()
	en := reflect.ValueOf(catalog["en"])
	for _, lang := range Languages {
		c, ok := catalog[lang]
		if !ok {
			t.Fatalf("%s %s: no lines", name, lang)
		}
		v := reflect.ValueOf(c)
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i).Name
			if v.Field(i).Kind() != reflect.String {
				continue
			}
			got, want := v.Field(i).String(), en.Field(i).String()
			if strings.TrimSpace(got) == "" {
				t.Errorf("%s %s %s: empty", name, lang, field)
			}
			if g, w := verb.FindAllString(got, -1), verb.FindAllString(want, -1); !reflect.DeepEqual(g, w) {
				t.Errorf("%s %s %s: values %v, English has %v", name, lang, field, g, w)
			}
			if lang != "en" && got == want && !sameEverywhere[want] {
				t.Errorf("%s %s %s: left in English", name, lang, field)
			}
		}
	}
	if len(catalog) != len(Languages) {
		t.Errorf("%s: %d catalogs, %d languages", name, len(catalog), len(Languages))
	}
}

func TestEveryLanguageHasEveryLine(t *testing.T) {
	checkCatalog(t, "device", device)
	checkCatalog(t, "held", held)
	checkCatalog(t, "app sign-in", appSignIn)
	checkCatalog(t, "region", region)
	for _, lang := range Languages {
		for status := range held["en"].Status {
			if held[lang].Status[status] == "" {
				t.Errorf("held %s: no words for %s", lang, status)
			}
		}
	}
}

// glossary is apps/api's copy of tafs' glossary: term → its word in each
// language, in Languages' order after English.
func glossary(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile("../../../api/internal/words/glossary.md")
	if err != nil {
		t.Fatalf("apps/api's glossary copy: %v", err)
	}
	out := map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		cells := strings.Split(strings.Trim(line, "| "), "|")
		if len(cells) != len(Languages) {
			continue
		}
		key := strings.TrimSpace(cells[0])
		for i := range cells {
			// A term's gloss ("held (a call held for approval)") isn't its word.
			word, _, _ := strings.Cut(cells[i], " (")
			cells[i] = strings.TrimSpace(word)
		}
		out[key] = cells
	}
	if len(out) < 20 {
		t.Fatalf("apps/api's glossary copy has %d rows: its table wasn't read", len(out))
	}
	return out
}

// The buttons that approve say the glossary's Approve, in every language.
func TestApproveIsTheGlossarysWord(t *testing.T) {
	approve := glossary(t)["Approve"]
	if approve == nil {
		t.Fatal("the glossary has no Approve row")
	}
	for i, lang := range Languages {
		w := strings.ToLower(approve[i])
		for field, line := range map[string]string{"ApproveDevice": device[lang].ApproveDevice, "ApproveAgain": device[lang].ApproveAgain} {
			if !strings.Contains(strings.ToLower(line), w) {
				t.Errorf("%s %s: %q doesn't say the glossary's %q", lang, field, line, approve[i])
			}
		}
	}
}

// A role reads as apps/api names it (internal/words/members_*.go), so the
// device page and KIFF's own lines agree.
func TestRolesAreTheAPIs(t *testing.T) {
	for _, lang := range Languages[1:] {
		raw, err := os.ReadFile("../../../api/internal/words/members_" + lang + ".go")
		if err != nil {
			t.Fatalf("apps/api's %s members: %v", lang, err)
		}
		for role, name := range device[lang].RoleName {
			if !strings.Contains(string(raw), `"`+role+`": "`+name+`"`) {
				t.Errorf("%s %s: %q isn't apps/api's name for it", lang, role, name)
			}
		}
		if len(device[lang].RoleName) != 4 {
			t.Errorf("%s: %d roles", lang, len(device[lang].RoleName))
		}
	}
}

// The device page says the glossary's words for a held call and for
// signing in again, in every language.
func TestDeviceSaysTheGlossarysTerms(t *testing.T) {
	g := glossary(t)
	held, again := g["held (a call held for approval)"], g["Sign in again"]
	if held == nil || again == nil {
		t.Fatal("the glossary has no held or Sign in again row")
	}
	for i, lang := range Languages {
		// Its stem: a held call agrees with its noun ("chiamate trattenute").
		stem := strings.ToLower(held[i])
		stem = stem[:len(stem)-1]
		if !strings.Contains(strings.ToLower(device[lang].KeyNote), stem) {
			t.Errorf("%s KeyNote: %q doesn't say the glossary's %q", lang, device[lang].KeyNote, held[i])
		}
		if device[lang].SignInAgain != again[i] {
			t.Errorf("%s SignInAgain: %q, the glossary says %q", lang, device[lang].SignInAgain, again[i])
		}
	}
}

// A held call's screen says the glossary's words: its heading, the two
// answers, and a held call.
func TestHeldSaysTheGlossarysTerms(t *testing.T) {
	g := glossary(t)
	for i, lang := range Languages {
		h := held[lang]
		for _, c := range []struct{ term, got string }{{"Needs you", h.Heading}, {"Approve once", h.ApproveOnce}, {"Refuse", h.Refuse}} {
			if want := g[c.term]; want == nil || c.got != want[i] {
				t.Errorf("%s %s: %q, the glossary says %v", lang, c.term, c.got, want)
			}
		}
		stem := strings.ToLower(g["held (a call held for approval)"][i])
		stem = stem[:len(stem)-1]
		if !strings.Contains(strings.ToLower(h.RoleCantAnswer), stem) {
			t.Errorf("%s RoleCantAnswer: %q doesn't say a held call as the glossary does", lang, h.RoleCantAnswer)
		}
	}
}
