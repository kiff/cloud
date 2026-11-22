package handlers

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/country"
)

// headingClass says what a policy section is about, from its heading, so a
// country section's "section 9 of each policy" can be checked against what
// section 9 really is in KIFF's policy and in tafs's.
var headingClasses = []struct{ class, re string }{
	{"who", `^Who we are`},
	{"roles", `roles`},
	{"data", `categories of personal data|What tafs collects`},
	{"lawful", `^Lawful bases`},
	{"transfers", `^International transfers|^Who else receives data`},
	{"retention", `^Retention|^How long we keep it`},
	{"rights", `^Your rights`},
}

var h2RE = regexp.MustCompile(`<h2[^>]*>\s*(\d+)\.\s*([^<]+)</h2>`)
var tagRE = regexp.MustCompile(`<[^>]+>`)

func policyClasses(t *testing.T, mux http.Handler, path string) map[int]string {
	t.Helper()
	body := getPage(t, mux, path).Body.String()
	out := map[int]string{}
	for _, m := range h2RE.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.Atoi(m[1])
		for _, c := range headingClasses {
			if regexp.MustCompile(`(?i)` + c.re).MatchString(strings.TrimSpace(m[2])) {
				out[n] = c.class
				break
			}
		}
	}
	return out
}

// citationRE finds "section 7 of each policy", "sections 4, 5 and 8 of
// KIFF's policy", "sections 1, 4, 8, 9 and 10 of the tafs policy".
var citationRE = regexp.MustCompile(`(?i)sections? ((?:\d+(?:, | and |, and )?)+) of (each|KIFF's|the tafs) policy`)

// What each kind of sentence is allowed to point at.
func allowedClasses(sentence string) map[string]bool {
	s := strings.ToLower(sentence)
	switch {
	case strings.Contains(s, "which is which"):
		return map[string]bool{"roles": true, "data": true}
	case strings.Contains(s, "do that"):
		return map[string]bool{"who": true, "data": true, "lawful": true, "retention": true, "rights": true}
	case strings.Contains(s, "what we process"), strings.Contains(s, "on the bases in"):
		return map[string]bool{"data": true, "lawful": true, "retention": true}
	case strings.Contains(s, "transfer"), strings.Contains(s, "recipients"), strings.Contains(s, "data may leave"):
		return map[string]bool{"transfers": true}
	case strings.Contains(s, "section 7"):
		return map[string]bool{"transfers": true}
	case strings.Contains(s, "retention"):
		return map[string]bool{"retention": true}
	case strings.Contains(s, "rights"):
		return map[string]bool{"rights": true}
	}
	return nil
}

// Every section number a country section cites names the section it means,
// in KIFF's policy and in tafs's (they are numbered differently: lawful bases
// are 5 and 9, rights 9 and 10). "Each policy" is only used for a number that
// means the same thing in both. The translated files keep the same numbers as
// their English text.
func TestCountrySectionCitationsMatchThePolicyHeadings(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /privacy", Privacy())
	mux.Handle("GET /tafs/privacy", TafsPrivacy())
	kiff := policyClasses(t, mux, "/privacy")
	tafs := policyClasses(t, mux, "/tafs/privacy")
	for _, n := range []int{3, 4, 5, 7, 8, 9} {
		if kiff[n] == "" {
			t.Fatalf("KIFF's policy section %d has no known heading class", n)
		}
	}
	for _, n := range []int{3, 4, 7, 8, 9, 10} {
		if tafs[n] == "" {
			t.Fatalf("tafs's policy section %d has no known heading class", n)
		}
	}
	numRE := regexp.MustCompile(`\d+`)
	checked := 0
	for c := range country.Served {
		en, ok, err := country.Get(c, "en")
		if err != nil || !ok {
			t.Fatalf("%s.en: %v %v", c, ok, err)
		}
		text := tagRE.ReplaceAllString(string(en.HTML), "")
		for _, sentence := range regexp.MustCompile(`[^.]*\.`).FindAllString(text, -1) {
			for _, m := range citationRE.FindAllStringSubmatch(sentence, -1) {
				allowed := allowedClasses(sentence)
				if allowed == nil {
					t.Errorf("%s: a citation in a sentence the test can't classify: %q", c, sentence)
					continue
				}
				for _, ns := range numRE.FindAllString(m[1], -1) {
					n, _ := strconv.Atoi(ns)
					which := strings.ToLower(m[2])
					var classes []string
					switch which {
					case "each":
						if kiff[n] != tafs[n] {
							t.Errorf("%s: %q cites section %d of each policy, but it is %q in KIFF's and %q in tafs's", c, sentence, n, kiff[n], tafs[n])
						}
						classes = []string{kiff[n]}
					case "kiff's":
						classes = []string{kiff[n]}
					default:
						classes = []string{tafs[n]}
					}
					if which == "each" && n == 7 && (strings.Contains(strings.ToLower(sentence), "safeguard") || strings.Contains(strings.ToLower(sentence), "contractual clauses")) {
						t.Errorf("%s: %q cites section 7 of each policy for safeguards, but only KIFF's section 7 names them (tafs's lists recipients and points to it)", c, sentence)
					}
					if !allowed[classes[0]] {
						t.Errorf("%s: %q cites section %d (%q) where it means one of %v", c, sentence, n, classes[0], allowed)
					}
					checked++
				}
			}
		}
		// A translated section cites the same numbers as the English one.
		for _, l := range country.Served[c] {
			if l == "en" {
				continue
			}
			tr, _, _ := country.Get(c, l)
			if a, b := policyNumbers(string(en.HTML)), policyNumbers(string(tr.HTML)); a != b {
				t.Errorf("%s.%s: its numbers are %s, the English text's are %s", c, l, b, a)
			}
		}
	}
	if checked < 40 {
		t.Errorf("the test checked only %d citations", checked)
	}
}

// policyNumbers is the list, in reading order, of small whole numbers in a
// section's text, which includes the section numbers it cites. A translation
// keeps the English sentence order, so a swapped pair of lists shows. A number inside a
// longer figure ("1.500", "2026") is not counted.
func policyNumbers(html string) string {
	text := tagRE.ReplaceAllString(html, " ")
	digits := regexp.MustCompile(`\d+`)
	var nums []int
	for _, loc := range digits.FindAllStringIndex(text, -1) {
		a, b := loc[0], loc[1]
		if b-a > 2 {
			continue
		}
		if a > 0 && strings.ContainsRune(".,", rune(text[a-1])) && a > 1 && text[a-2] >= '0' && text[a-2] <= '9' {
			continue
		}
		if b+1 < len(text) && strings.ContainsRune(".,", rune(text[b])) && text[b+1] >= '0' && text[b+1] <= '9' {
			continue
		}
		n, _ := strconv.Atoi(text[a:b])
		nums = append(nums, n)
	}
	out := make([]string, len(nums))
	for i, n := range nums {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, " ")
}
