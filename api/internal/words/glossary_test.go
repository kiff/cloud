package words

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The glossary is kiff/tafs/docs/languages.md (its Terms table), the one
// source for every word tafs and KIFF show. KIFF's tests can't read
// kiff/tafs, so glossary.md is a copy of the table and glossary.sha256 the
// digest kiff/tafs keeps of it (kiff/tafs/docs/glossary.sha256). A change to
// the glossary that KIFF's copy didn't follow fails here, not in a reader's
// email.

// glossaryRows are the Terms table's "|" rows, header and rule included.
func glossaryRows(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("glossary.md")
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)
	from := strings.Index(md, "\n## Terms")
	if from < 0 {
		t.Fatal("glossary.md has no ## Terms")
	}
	section := md[from+1:]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	var rows []string
	for _, l := range strings.Split(section, "\n") {
		if strings.HasPrefix(l, "|") {
			rows = append(rows, l)
		}
	}
	return rows
}

// When the glossary changes, kiff/tafs/docs/glossary.sha256 changes with
// it: copy both its Terms table into glossary.md and its digest into
// glossary.sha256, then fix any line the term check below finds.
func TestGlossaryCopyIsCurrent(t *testing.T) {
	var all strings.Builder
	for _, r := range glossaryRows(t) {
		all.WriteString(r + "\n")
	}
	sum := sha256.Sum256([]byte(all.String()))
	b, err := os.ReadFile("glossary.sha256")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(string(b))
	if len(want) == 0 || want[0] != hex.EncodeToString(sum[:]) {
		t.Fatalf("glossary.md's Terms table hashes to %s, glossary.sha256 says %v: copy the table and its digest from kiff/tafs together",
			hex.EncodeToString(sum[:]), want)
	}
}

type term struct {
	en    string
	label bool
	words map[Lang]string
}

// glossaryTerms reads the table as kiff/tafs' own check does: a term with
// a capital in English is a label and matches as written, a lowercase one
// is a noun and matches in any case; "(…)" after it only explains it.
func glossaryTerms(t *testing.T) []term {
	t.Helper()
	rows := glossaryRows(t)
	cells := func(row string) []string {
		parts := strings.Split(row, "|")
		parts = parts[1 : len(parts)-1]
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	order := []Lang{EN, DE, FR, ES, IT, PT, NL, PL}
	if got := cells(rows[0]); !reflect.DeepEqual(got, []string{"English", "German", "French", "Spanish", "Italian", "Portuguese", "Dutch", "Polish"}) {
		t.Fatalf("glossary header = %v", got)
	}
	explain := regexp.MustCompile(`\s*\(.*\)$`)
	var out []term
	for _, row := range rows[2:] {
		c := cells(row)
		en := explain.ReplaceAllString(c[0], "")
		tm := term{en: en, label: en[0] >= 'A' && en[0] <= 'Z', words: map[Lang]string{}}
		for i, l := range order {
			tm.words[l] = strings.ToLower(c[i])
		}
		out = append(out, tm)
	}
	return out
}

// glossaryForms are the other forms a term takes in a language's grammar,
// each written out, never a fuzzy match: by language, then by the
// glossary's word.
var glossaryForms = map[Lang]map[string][]string{
	DE: {"genehmigen": {"genehmige"}}, // "Approve by adding the pack": the imperative
	FR: {"approuver": {"approuvez"}},
	ES: {"aprobar": {"aprueba"}},
	IT: {"trattenuta": {"trattenute"}}, // "held actions": the plural
	PT: {"aprovar": {"aprove"}},
	NL: {"goedkeuren": {"keur goed"}},
	PL: {
		"połączenie":     {"połączenia"},               // "of a connection": the genitive
		"oczekujące":     {"oczekującą", "oczekująca"}, // "a held action", as object and subject
		"zatwierdzający": {"zatwierdzającą", "zatwierdzającego"},
		"limit wydatków": {"limitu wydatków"},
		"prośba":         {"prośby"}, // "no request": the genitive after a negation
	},
}

// notTheTerm are lines whose English word isn't the glossary's term (a
// verb where the term is a noun), by term, then line.
var notTheTerm = map[string][]string{
	"run": {"InviteRoleWhat", // "run the account": the verb, not a run
		"ConnectionWhy"}, // tool_not_served, "this action is never run for this app": the verb
}

// Every line of every language uses the glossary's word for each glossary
// term its English has, or a form of it listed above.
func TestEveryLineUsesTheGlossarysWords(t *testing.T) {
	terms := glossaryTerms(t)
	sort.SliceStable(terms, func(i, j int) bool { return len(terms[i].en) > len(terms[j].en) })
	lines := map[Lang]map[string]string{}
	for _, l := range All {
		lines[l] = catalogLines(Of(l))
	}
	var misses []string
	for _, key := range sortedKeys(lines[EN]) {
		// The longest terms first: "Approve once" is one term, and its
		// "Approve" isn't another.
		english := lines[EN][key]
		for _, tm := range terms {
			flags := "(?i)"
			if tm.label {
				flags = ""
			}
			re := regexp.MustCompile(flags + `\b` + regexp.QuoteMeta(tm.en) + `\b`)
			if !re.MatchString(english) {
				continue
			}
			english = re.ReplaceAllString(english, " ")
			if contains(notTheTerm[tm.en], key) {
				continue
			}
			for _, l := range All[1:] {
				line := strings.ToLower(lines[l][key])
				want := tm.words[l]
				ok := strings.Contains(line, want)
				for _, f := range glossaryForms[l][want] {
					ok = ok || strings.Contains(line, f)
				}
				if !ok {
					misses = append(misses, string(l)+"."+key+": \""+tm.en+"\" is \""+want+"\" in the glossary")
				}
			}
		}
	}
	for _, m := range misses {
		t.Error(m)
	}
}

// catalogLines is every line of w by field name, an embedded block's
// fields included: a string as it is, a map's or an array's lines joined,
// and a function called twice, once with every argument set and once with
// every argument empty, so each wording it has is read.
func catalogLines(w *Words) map[string]string {
	out := map[string]string{}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		for i := 0; i < v.NumField(); i++ {
			f, sf := v.Field(i), v.Type().Field(i)
			switch f.Kind() {
			case reflect.Struct:
				if sf.Anonymous {
					walk(f)
				}
			case reflect.String:
				out[sf.Name] = f.String()
			case reflect.Map:
				// Its lines, by key in order; a key is KIFF's own name
				// ("approver"), never a word the reader reads.
				keys := f.MapKeys()
				sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
				var parts []string
				for _, k := range keys {
					parts = append(parts, f.MapIndex(k).String())
				}
				out[sf.Name] = strings.Join(parts, " \x00 ")
			case reflect.Array:
				var parts []string
				for j := 0; j < f.Len(); j++ {
					parts = append(parts, f.Index(j).String())
				}
				out[sf.Name] = strings.Join(parts, " \x00 ")
			case reflect.Func:
				if f.IsNil() {
					continue
				}
				out[sf.Name] = call(f, true) + " \x00 " + call(f, false)
			}
		}
	}
	walk(reflect.ValueOf(w).Elem())
	return out
}

// call runs a line's function with every argument set ("‹…›", 3, true) or
// every argument empty, and joins what it returns.
func call(f reflect.Value, set bool) string {
	args := make([]reflect.Value, f.Type().NumIn())
	for i := range args {
		in := f.Type().In(i)
		v := reflect.New(in).Elem()
		if set {
			switch in.Kind() {
			case reflect.String:
				v.SetString("‹…›")
			case reflect.Int, reflect.Int64:
				v.SetInt(3)
			case reflect.Float64:
				v.SetFloat(3.5)
			case reflect.Bool:
				v.SetBool(true)
			}
		}
		args[i] = v
	}
	var parts []string
	for _, r := range f.Call(args) {
		parts = append(parts, r.String())
	}
	return strings.Join(parts, " ")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
