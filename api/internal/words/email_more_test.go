package words

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Every name the table lists is a Words field, so a line is never silently skipped.
func TestEveryEmailLineNameIsAWordsField(t *testing.T) {
	for _, n := range emailLineNames {
		if !reflect.ValueOf(en).FieldByName(n).IsValid() {
			t.Errorf("%s is not a Words field", n)
		}
	}
	if len(EmailLineNames()) < len(emailLineNames) {
		t.Errorf("a map line has no keys")
	}
}

// A field an email reads (w.X or words.Of(..).X in the notify package) is in the table: a
// new email line can't be added to the eight and forgotten for the other languages.
func TestEveryLineAnEmailReadsIsInTheTable(t *testing.T) {
	files, err := filepath.Glob("../notify/*.go")
	if err != nil || len(files) == 0 {
		t.Skip("the notify package is not here")
	}
	in := map[string]bool{}
	for _, n := range emailLineNames {
		in[n] = true
	}
	re := regexp.MustCompile(`\b(?:w|words\.Of\([^)]*\))\.([A-Z][A-Za-z]+)`)
	typ := reflect.TypeOf(en)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			name := m[1]
			if _, isField := typ.FieldByName(name); isField && !in[name] {
				t.Errorf("%s reads Words.%s, which is not in the table of lines other languages have (emailLineNames)", filepath.Base(f), name)
			}
		}
	}
}

// Built from the English templates, a language reads the same as English does.
func TestTheEnglishTemplatesBuildEnglish(t *testing.T) {
	var w Words = en
	setEmailLines(&w, EmailLinesEnglish())
	if got, want := w.HeldIntro("A", "B", " C"), en.HeldIntro("A", "B", " C"); got != want {
		t.Errorf("HeldIntro: %q, want %q", got, want)
	}
	if got, want := w.InviteIntro("by", "acct", "role", "what"), en.InviteIntro("by", "acct", "role", "what"); got != want {
		t.Errorf("InviteIntro: %q, want %q", got, want)
	}
	if got, want := w.DateTimeIn(time.Date(2026, 3, 2, 15, 4, 0, 0, time.UTC), nil), en.DateTimeIn(time.Date(2026, 3, 2, 15, 4, 0, 0, time.UTC), nil); got != want {
		t.Errorf("DateTimeIn: %q, want %q", got, want)
	}
	if got, want := w.Role("admin"), en.Role("admin"); got != want {
		t.Errorf("Role: %q, want %q", got, want)
	}
	if problems := EmailLinesProblems(EmailLinesEnglish()); len(problems) != 0 {
		t.Errorf("English is not complete: %v", problems)
	}
}

// A language with lines reads them in every email, with the parts put where its sentence has them;
// one with none reads English; English itself is never changed by building another.
func TestALanguageWithEmailLinesReadsThemAndOthersReadEnglish(t *testing.T) {
	lines := EmailLines{}
	for k, v := range EmailLinesEnglish() {
		lines[k] = "[xx] " + v
	}
	lines["HeldWants"] = "{0} [xx] wants"
	lines["Months"] = "a|b|c|d|e|f|g|h|i|j|k|l"
	lines["DayDot"] = "true"
	EmailMore["xx"] = lines
	defer func() { delete(EmailMore, "xx"); forgetEmailWords("xx") }()
	forgetEmailWords("xx")

	w := Of("xx")
	if w == catalogs[EN] || !strings.HasPrefix(w.NewDeviceSubject, "[xx]") || !strings.HasPrefix(w.HeldReject, "[xx]") {
		t.Fatalf("a language with lines reads them: %q", w.NewDeviceSubject)
	}
	if got := w.HeldWants("Bot"); got != "Bot [xx] wants" {
		t.Errorf("a part goes where the language's sentence has it: %q", got)
	}
	if got := w.HeldIntro("Bot", "pay", " 5"); !strings.HasPrefix(got, "[xx] ") || !strings.Contains(got, "Bot") || !strings.Contains(got, "pay") || !strings.Contains(got, " 5") {
		t.Errorf("HeldIntro: %q", got)
	}
	if got := w.DateTimeIn(time.Date(2026, 3, 2, 15, 4, 0, 0, time.UTC), nil); got != "2. c 2026, 15:04" {
		t.Errorf("the language's months and its dot after the day: %q", got)
	}
	if !strings.HasPrefix(w.Role("admin"), "[xx]") || !strings.HasPrefix(w.InviteRoleWhat["admin"], "[xx]") {
		t.Errorf("a map line: %q %q", w.Role("admin"), w.InviteRoleWhat["admin"])
	}
	if strings.Contains(en.NewDeviceSubject, "[xx]") || strings.Contains(en.InviteRoleWhat["admin"], "[xx]") || en.Months[0] != "Jan" || en.DayDot {
		t.Fatal("building a language changed English")
	}
	if Of("zz") != catalogs[EN] {
		t.Errorf("a language with no lines reads English")
	}
}

// What is wrong with a language's lines is said by name: a line missing or empty, one KIFF doesn't have,
// a line whose parts are not the English one's, months that aren't twelve.
func TestEmailLinesProblemsNameWhatIsWrong(t *testing.T) {
	good := EmailLines{}
	for k, v := range EmailLinesEnglish() {
		good[k] = v
	}
	if p := EmailLinesProblems(good); len(p) != 0 {
		t.Fatalf("complete lines have problems: %v", p)
	}
	for name, change := range map[string]func(EmailLines){
		"HeldIntro is missing":      func(l EmailLines) { delete(l, "HeldIntro") },
		"HeldReject is missing":     func(l EmailLines) { l["HeldReject"] = " " },
		"has parts":                 func(l EmailLines) { l["HeldIntro"] = "{0} wants {1}" },
		"Months is not twelve":      func(l EmailLines) { l["Months"] = "a|b" },
		"is not a line":             func(l EmailLines) { l["Nonsense"] = "x" },
		"InviteRoleWhat.admin is m": func(l EmailLines) { delete(l, "InviteRoleWhat.admin") },
	} {
		l := EmailLines{}
		for k, v := range good {
			l[k] = v
		}
		change(l)
		found := false
		for _, p := range EmailLinesProblems(l) {
			if strings.Contains(p, name) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q not reported: %v", name, EmailLinesProblems(l))
		}
	}
}

// Every language that has lines has every line (the eight have theirs in Words).
func TestEveryEmailMoreLanguageIsComplete(t *testing.T) {
	for tag, lines := range EmailMore {
		if _, more := CodeSignInEmailMore[tag]; !more {
			t.Errorf("%s has email lines but is not a language the emails speak", tag)
		}
		if p := EmailLinesProblems(lines); len(p) != 0 {
			t.Errorf("%s: %v", tag, p)
		}
	}
	t.Logf("languages the emails still read in English: %d (%s)", len(EmailLanguagesMissing()), strings.Join(EmailLanguagesMissing(), " "))
}

// TestWriteEmailLinesEnglish writes the English lines as templates, one JSON object by Words field name,
// to the file named by KIFF_EMAIL_LINES: the source a language is translated from. Nothing when unset.
func TestWriteEmailLinesEnglish(t *testing.T) {
	path := os.Getenv("KIFF_EMAIL_LINES")
	if path == "" {
		t.Skip("KIFF_EMAIL_LINES is not set")
	}
	b, err := json.MarshalIndent(EmailLinesEnglish(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
