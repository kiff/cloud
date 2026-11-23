package words

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

// The emails in every shipped language. Eight languages have every line in Words; the
// others (the 37 more the code sign-in emails already speak) have the lines the emails
// read, here, as EmailLines, written by legal-i18n. Of(lang) is the English Words with
// these lines laid over it, so every email (new device, invitations, account notices,
// held action, Card warning) reads in the person's language with no change in the code
// that writes it. A language with no entry reads English, never half of each: an entry
// must have every line (a test says which are missing).
//
// A line is plain text. A line that has parts to fill in has them as {0}, {1}, … in the
// order the English one takes them (EmailLinesEnglish lists them all with their parts).

// EmailLines are one language's lines, by Words field name. A line with a part to fill in
// is a template, {0} the first part. A map line is a key per entry ("InviteRoleWhat.admin"),
// the months are twelve names joined by "|" ("Months"), DayDot is "true" or "false".
type EmailLines map[string]string

// EmailMore are the languages beyond the eight, by primary language tag.
var EmailMore = map[string]EmailLines{}

// emailLineNames are the Words fields the emails read, by name. A test reads the notify
// package's source and fails on a field an email reads that isn't here.
var emailLineNames = []string{
	"NewDeviceSubject", "NewDeviceSubjectTafs", "NewDeviceIntro", "NewDeviceIntroTafs", "NewDeviceWhen",
	"NewDeviceDevice", "NewDeviceBrowser", "NewDeviceMethod", "NewDevicePlace", "NewDevicePassword",
	"NewDevicePlaceNote", "NewDeviceWasYou", "NewDeviceNotYou", "NewDeviceNotYouSignOut", "NewDeviceSessions",
	"NewDeviceWhy", "NewDeviceRow", "NewDeviceOn", "NewDeviceNear", "NewDeviceZone",
	"InviteIgnore", "InviteButton", "InviteRoleOther", "InviteSubject", "InviteIntro", "InviteExpires",
	"InviteAccept", "InviteRoleWhat", "RoleName", "AnAdmin", "AnAccount", "AccountNamed", "PeopleLink",
	"HeldSubject", "HeldNothingDone", "HeldOpen", "HeldOptions", "HeldNoApprove", "HeldButtonsNote",
	"HeldApproveOnce", "HeldChangeCard", "HeldReject", "HeldRowAction", "HeldRowAmount", "HeldRowWhy",
	"HeldIntro", "HeldWants", "HeldSteps", "HeldExpires",
	"WarnSubject", "WarnReview", "Months", "DayDot",
}

// noticeLineNames are the Words fields the account notices read, which handlers word before the notice goes to the
// layout: a new approver, a relinked phone, a pack or a raise asked for, who joined, a Card changed, separation of duties
// off. They are a second group of the table: a language may have the first (the emails drawn here) and not yet this one.
var noticeLineNames = []string{
	"NewApproverSubject", "NewApproverBody", "YourPhone", "RelinkSubject", "RelinkBody", "AMember", "AskReason",
	"PackAskSubject", "PackAskBody", "PackAskHow", "RaiseAskSubject", "RaiseAskBody", "RaiseAskHow",
	"AuthoritySubject", "AuthorityChanged", "AuthorityInvited", "CardChangeSubject", "CardChangeBody", "CardChangeCards",
	"JoinedSubject", "JoinedSelf", "JoinedOther", "JoinedUnknown", "NoticeWhy", "SoDOffSubject", "SoDOffBody",
	"Someone", "UnnamedAccount",
}

// cardLineNames are the Words fields the Card warning's sentence and a held action's reason line read, and that the
// amounts in them are written with: how an amount is written (Money, DecimalComma, NoCurrency), a window's words, the
// count of calls and the ordinal ("21st"), and each reason. They are the third group of the table ("cards").
//
// Two lines are not plain templates. CallCount and Ordinal take a number, so they hold one form per plural category of
// the language (CLDR: zero, one, two, few, many, other), written "one={0} call|other={0} calls"; the form for a number
// is the language's own rule's, and "other" is the one a missing category falls back to. Money is written as three
// lines: Money.Thousands, Money.Gap (what goes between the amount and its currency) and Money.CurrencyAfter, whose
// separators are named (moneySeparators) because a space that is a line's whole text cannot be told from no text.
var cardLineNames = []string{
	"WarnSpent", "WarnBurst", "WarnRunway", "WarnUnusual", "WarnUsed", "Periods", "WarnNeverResets", "WarnReviewWindow",
	"Money", "DecimalComma", "NoCurrency",
	"EachCallAsks", "CallCount", "WindowAmount", "WindowCount", "ForEach", "Ordinal",
	"WouldBe", "WouldBeFor", "OverCalls", "OverForOneCall", "OverTheTotal",
	"OutsideCard", "OverActionLimit", "OverCardTotal", "NotCardResource",
}

// pluralLines are the lines that hold a form per plural category, by name, and whether they count places (ordinal).
var pluralLines = map[string]bool{"CallCount": false, "Ordinal": true}

// pluralEnglish are English's forms of those lines (a test says they are what English's functions write).
var pluralEnglish = map[string]string{
	"CallCount": "one={0} call|other={0} calls",
	"Ordinal":   "one={0}st|two={0}nd|few={0}rd|other={0}th",
}

// moneySeparators are the names a money line gives its separators.
var moneySeparators = map[string]string{
	"none": "", "comma": ",", "dot": ".", "space": " ", "nbsp": "\u00a0", "thin": "\u202f", "apostrophe": "’",
}

var moneyKeys = []string{"Money.Thousands", "Money.Gap", "Money.CurrencyAfter"}

// Groups of lines. "core" is every email this package draws; "notices" is the account notices' sentences, written by
// handlers, and needs "core"; "cards" is the Card warning's and the held reason's, and needs "core" too. An entry is
// complete when core is, and has each other group when that group is whole.
const (
	groupCore    = "core"
	groupNotices = "notices"
	groupCards   = "cards"
)

func init() {
	emailLineNames = append(emailLineNames, noticeLineNames...)
	emailLineNames = append(emailLineNames, cardLineNames...)
}

// lineGroup is the group a line belongs to: its Words field name decides it.
func lineGroup(key string) string {
	name, _, _ := strings.Cut(key, ".")
	for _, n := range noticeLineNames {
		if n == name {
			return groupNotices
		}
	}
	for _, n := range cardLineNames {
		if n == name {
			return groupCards
		}
	}
	return groupCore
}

// parsePlural reads "one={0} call|other={0} calls" into its forms; false when a form has no category, an unknown one,
// or "other" is missing.
func parsePlural(line string) (map[string]string, bool) {
	out := map[string]string{}
	for _, part := range strings.Split(line, "|") {
		cat, text, ok := strings.Cut(part, "=")
		switch cat {
		case "zero", "one", "two", "few", "many", "other":
		default:
			return nil, false
		}
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false
		}
		out[cat] = text
	}
	_, hasOther := out["other"]
	return out, hasOther
}

// pluralCategory is the plural category of the whole number n in lang, by CLDR's rules.
func pluralCategory(lang string, n int64, ordinal bool) string {
	tag := language.Make(lang)
	rules := plural.Cardinal
	if ordinal {
		rules = plural.Ordinal
	}
	switch rules.MatchPlural(tag, int(n), 0, 0, 0, 0) {
	case plural.Zero:
		return "zero"
	case plural.One:
		return "one"
	case plural.Two:
		return "two"
	case plural.Few:
		return "few"
	case plural.Many:
		return "many"
	}
	return "other"
}

// pluralCategoriesOf are the categories the whole numbers reach in lang: the ones a line of it needs a form for.
func pluralCategoriesOf(lang string, ordinal bool) []string {
	seen := map[string]bool{}
	probe := []int64{1000000, 2000000, 1000000000}
	for n := int64(0); n <= 1000; n++ {
		probe = append(probe, n)
	}
	for _, n := range probe {
		seen[pluralCategory(lang, n, ordinal)] = true
	}
	var out []string
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// EmailPluralProblems says which plural forms a language's CallCount and Ordinal lines lack: a form for each category
// a whole number can have in it. A missing one is not an error at run time ("other" reads), but it is a wrong
// sentence ("2 call"), so legal-i18n's lines are checked.
func EmailPluralProblems(lang string, lines EmailLines) []string {
	var out []string
	for name, ordinal := range pluralLines {
		line, ok := lines[name]
		if !ok {
			continue
		}
		forms, ok := parsePlural(line)
		if !ok {
			out = append(out, name+" is not forms like one={0} call|other={0} calls")
			continue
		}
		for _, c := range pluralCategoriesOf(lang, ordinal) {
			if _, has := forms[c]; !has {
				out = append(out, name+" has no form for "+c+" in "+lang)
			}
		}
		for c, text := range forms {
			if strings.Join(placeholders(text), " ") != "{0}" {
				out = append(out, name+" form "+c+" must have {0} once as its number")
			}
		}
	}
	sort.Strings(out)
	return out
}

// moneyClash says when a language's thousands separator is its decimal mark, so "1,250" could be one thousand two
// hundred fifty or one and a quarter: DecimalComma false writes the mark as a dot, true as a comma.
func moneyClash(lines EmailLines) string {
	th, ok := lines["Money.Thousands"]
	dc, ok2 := lines["DecimalComma"]
	if !ok || !ok2 {
		return ""
	}
	mark := "dot"
	if dc == "true" {
		mark = "comma"
	}
	if th == mark {
		return "Money.Thousands is the decimal mark (" + mark + "): an amount would read two ways"
	}
	return ""
}

// lineValueProblem is what is wrong with one line's value besides its parts, "" when nothing.
func lineValueProblem(name, got string) string {
	_, isPlural := pluralLines[name]
	switch {
	case name == "Months" && len(strings.Split(got, "|")) != 12:
		return "Months is not twelve names joined by |"
	case isPlural:
		if _, ok := parsePlural(got); !ok {
			return name + " is not forms like one={0} call|other={0} calls"
		}
	case name == "Money.Thousands" || name == "Money.Gap":
		if _, ok := moneySeparators[got]; !ok {
			return name + " is not one of none, comma, dot, space, nbsp, thin, apostrophe"
		}
	case name == "Money.CurrencyAfter" || name == "DecimalComma" || name == "DayDot":
		if got != "true" && got != "false" {
			return name + " is not true or false"
		}
	}
	return ""
}

// groupLines are the English lines of one group.
func groupLines(group string) EmailLines {
	out := EmailLines{}
	for k, v := range EmailLinesEnglish() {
		if lineGroup(k) == group {
			out[k] = v
		}
	}
	return out
}

// groupComplete says whether lines has every line of group, with the English one's parts.
func groupComplete(lines EmailLines, group string) bool {
	for k, en := range groupLines(group) {
		got, ok := lines[k]
		if !ok || strings.TrimSpace(got) == "" || strings.Join(placeholders(got), " ") != strings.Join(placeholders(en), " ") {
			return false
		}
		if lineValueProblem(k, got) != "" {
			return false
		}
	}
	return group != groupCards || moneyClash(lines) == ""
}

// EmailGroupComplete says whether lang has every line of a group ("core" or "notices"); the eight have them all.
func EmailGroupComplete(lang Lang, group string) bool {
	if _, ok := catalogs[lang]; ok {
		return true
	}
	// A variant has every group: a line it doesn't write reads as its base's.
	if _, ok := variantBase[lang]; ok {
		return true
	}
	lines, ok := EmailMore[string(lang)]
	if !ok || !groupComplete(lines, groupCore) {
		return false
	}
	return group == groupCore || groupComplete(lines, group)
}

// EmailLineNames are the names an EmailLines needs, with a map line's keys, sorted.
func EmailLineNames() []string {
	var out []string
	for _, n := range emailLineNames {
		f := reflect.ValueOf(en).FieldByName(n)
		if f.Kind() == reflect.Struct { // Money
			out = append(out, moneyKeys...)
			continue
		}
		if f.Kind() == reflect.Map {
			for _, k := range f.MapKeys() {
				out = append(out, n+"."+k.String())
			}
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// EmailLinesEnglish are the English lines as templates, the source to translate: each
// func line called with its parts written as {0}, {1}, ….
func EmailLinesEnglish() EmailLines {
	out := EmailLines{}
	ev := reflect.ValueOf(en)
	for _, n := range emailLineNames {
		f := ev.FieldByName(n)
		switch f.Kind() {
		case reflect.String:
			out[n] = f.String()
		case reflect.Bool:
			out[n] = fmt.Sprint(f.Bool())
		case reflect.Array:
			parts := make([]string, f.Len())
			for i := range parts {
				parts[i] = f.Index(i).String()
			}
			out[n] = strings.Join(parts, "|")
		case reflect.Map:
			for _, k := range f.MapKeys() {
				out[n+"."+k.String()] = f.MapIndex(k).String()
			}
		case reflect.Struct:
			for k, v := range moneyLines(ev.FieldByName(n).Interface().(MoneyStyle)) {
				out[k] = v
			}
		case reflect.Func:
			if line, ok := pluralEnglish[n]; ok {
				out[n] = line
				continue
			}
			// A part that is a number is called with a marker, then written as {i}.
			args := make([]reflect.Value, f.Type().NumIn())
			marks := map[int]string{}
			for i := range args {
				if k := f.Type().In(i).Kind(); k >= reflect.Int && k <= reflect.Int64 {
					marks[i] = strconv.Itoa(7000001 + i)
					args[i] = reflect.ValueOf(int64(7000001 + i)).Convert(f.Type().In(i))
					continue
				}
				args[i] = reflect.ValueOf(fmt.Sprintf("{%d}", i))
			}
			line := f.Call(args)[0].String()
			for i, m := range marks {
				line = strings.ReplaceAll(line, m, fmt.Sprintf("{%d}", i))
			}
			out[n] = line
		}
	}
	return out
}

// emailFuncArity is how many parts a func line takes, 0 for a line that isn't one.
func emailFuncArity(name string) int {
	if f := reflect.ValueOf(en).FieldByName(name); f.Kind() == reflect.Func {
		return f.Type().NumIn()
	}
	return 0
}

// Placeholders are the {n} parts a line has, sorted and without repeats.
func placeholders(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 2 {
			continue
		}
		p := s[i : i+j+1]
		if strings.Trim(p[1:len(p)-1], "0123456789") == "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	var uniq []string
	for i, p := range out {
		if i == 0 || p != out[i-1] {
			uniq = append(uniq, p)
		}
	}
	return uniq
}

// EmailLinesProblems says what is wrong with a language's lines: a line missing or empty,
// a line KIFF doesn't have, or a line whose parts aren't the English one's. Empty when it
// is complete.
func EmailLinesProblems(lines EmailLines) []string {
	var out []string
	english := EmailLinesEnglish()
	given := map[string]bool{}
	for name := range lines {
		given[lineGroup(name)] = true
	}
	for name, en := range english {
		// The core is required; the notices only once a line of them is given (a half group is a problem).
		if g := lineGroup(name); g != groupCore && !given[g] {
			continue
		}
		got, ok := lines[name]
		switch {
		case !ok || strings.TrimSpace(got) == "":
			out = append(out, name+" is missing")
		case strings.Join(placeholders(got), " ") != strings.Join(placeholders(en), " "):
			out = append(out, fmt.Sprintf("%s has parts %v, English %v", name, placeholders(got), placeholders(en)))
		default:
			if p := lineValueProblem(name, got); p != "" {
				out = append(out, p)
			}
		}
	}
	for name := range lines {
		if _, ok := english[name]; !ok {
			out = append(out, name+" is not a line of the emails")
		}
	}
	if p := moneyClash(lines); p != "" {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

var (
	moreMu    sync.Mutex
	moreWords = map[string]*Words{}
)

// emailWordsFor is English with lang's email lines laid over it, when lang has all of the core: an entry with a
// core line missing is read as none, so an email is never half in a language and half in English.
func emailWordsFor(lang Lang) (*Words, bool) {
	lines, ok := EmailMore[string(lang)]
	if !ok || !groupComplete(lines, groupCore) {
		return nil, false
	}
	moreMu.Lock()
	defer moreMu.Unlock()
	if w, ok := moreWords[string(lang)]; ok {
		return w, true
	}
	w := en // a copy: English's own lines stay English
	// Each group is laid over only when it is whole: the notices' sentences need the core as well.
	use := EmailLines{}
	whole := map[string]bool{groupCore: true, groupNotices: groupComplete(lines, groupNotices), groupCards: groupComplete(lines, groupCards)}
	for k, v := range lines {
		if whole[lineGroup(k)] {
			use[k] = v
		}
	}
	setEmailLines(&w, string(lang), use)
	moreWords[string(lang)] = &w
	return &w, true
}

// forgetEmailWords drops what was built for lang (tests change EmailMore).
func forgetEmailWords(lang string) {
	moreMu.Lock()
	delete(moreWords, lang)
	moreMu.Unlock()
}

func fill(template string, args []string) string {
	pairs := make([]string, 0, 2*len(args))
	for i, a := range args {
		pairs = append(pairs, fmt.Sprintf("{%d}", i), a)
	}
	return strings.NewReplacer(pairs...).Replace(template)
}

// moneyLines are a MoneyStyle as the table's lines.
func moneyLines(m MoneyStyle) EmailLines {
	name := func(sep string) string {
		for k, v := range moneySeparators {
			if v == sep {
				return k
			}
		}
		return "none"
	}
	return EmailLines{"Money.Thousands": name(m.Thousands), "Money.Gap": name(m.CurrencyGap),
		"Money.CurrencyAfter": strconv.FormatBool(m.CurrencyAfter)}
}

// pluralFunc is a line of forms as a function of a number, by lang's plural rules.
func pluralFunc(lang, line string, ordinal bool) func(int64) string {
	forms, _ := parsePlural(line)
	return func(n int64) string {
		form, ok := forms[pluralCategory(lang, n, ordinal)]
		if !ok {
			form = forms["other"]
		}
		return strings.ReplaceAll(form, "{0}", strconv.FormatInt(n, 10))
	}
}

func setEmailLines(w *Words, lang string, lines EmailLines) {
	wv := reflect.ValueOf(w).Elem()
	for _, n := range emailLineNames {
		f := wv.FieldByName(n)
		switch f.Kind() {
		case reflect.String:
			if v, ok := lines[n]; ok {
				f.SetString(v)
			}
		case reflect.Bool:
			if v, ok := lines[n]; ok {
				f.SetBool(v == "true")
			}
		case reflect.Array:
			if v, ok := lines[n]; ok {
				for i, m := range strings.Split(v, "|") {
					if i < f.Len() {
						f.Index(i).SetString(m)
					}
				}
			}
		case reflect.Map:
			// A map is copied, never changed in place: English's is shared.
			m := reflect.MakeMap(f.Type())
			for _, k := range f.MapKeys() {
				m.SetMapIndex(k, f.MapIndex(k))
			}
			for key, v := range lines {
				if k, ok := strings.CutPrefix(key, n+"."); ok {
					m.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(v))
				}
			}
			f.Set(m)
		case reflect.Struct:
			if v, ok := lines["Money.Thousands"]; ok {
				w.Money.Thousands = moneySeparators[v]
				w.Money.CurrencyGap = moneySeparators[lines["Money.Gap"]]
				w.Money.CurrencyAfter = lines["Money.CurrencyAfter"] == "true"
			}
		case reflect.Func:
			v, ok := lines[n]
			if !ok {
				continue
			}
			if ordinal, isPlural := pluralLines[n]; isPlural {
				if n == "CallCount" {
					w.CallCount = pluralFunc(lang, v, ordinal)
				} else {
					w.Ordinal = pluralFunc(lang, v, ordinal)
				}
				continue
			}
			f.Set(reflect.MakeFunc(f.Type(), func(in []reflect.Value) []reflect.Value {
				args := make([]string, len(in))
				for i, a := range in {
					if k := a.Kind(); k >= reflect.Int && k <= reflect.Int64 {
						args[i] = strconv.FormatInt(a.Int(), 10)
					} else {
						args[i] = a.String()
					}
				}
				return []reflect.Value{reflect.ValueOf(fill(v, args))}
			}))
		}
	}
}

// EmailLanguagesMissing are the languages the emails speak that have not every email's lines
// yet, by primary tag: the code sign-in emails' languages without EmailMore lines, and the
// languages in EmailMore that are not complete. For legal-i18n's list.
func EmailLanguagesMissing() []string {
	var out []string
	for tag := range CodeSignInEmailMore {
		if _, variant := variantBase[Lang(tag)]; variant {
			continue // its base's lines fill what it doesn't write
		}
		if lines, ok := EmailMore[tag]; !ok || len(EmailLinesProblems(lines)) > 0 {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// The language an email is really in. A person's language may be one of the 48 the emails speak, but an email
// reads in it only as far as its words are written: the page's language, its direction and its footer follow the
// words, so English is never laid out right to left.

// TextLang is the language of words that are not in the emails' table (a notice a handler words from `Words`): the
// eight, else English.
func TextLang(l Lang) Lang {
	if b, ok := variantBase[l]; ok {
		return b
	}
	if _, ok := catalogs[l]; ok {
		return l
	}
	return EN
}

// EmailLangFor is the language of an email whose words are all in the table: the eight, or a language with every
// line, else English.
func EmailLangFor(l Lang) Lang {
	if _, ok := catalogs[l]; ok {
		return l
	}
	if _, ok := variantBase[l]; ok {
		return l
	}
	if _, ok := emailWordsFor(l); ok {
		return l
	}
	return EN
}

// AccountEmailLang is the language of the suspended, reactivated and support emails: English, or a language with its
// account lines (AccountEmails).
func AccountEmailLang(l Lang) Lang {
	if l == EN {
		return EN
	}
	if _, ok := AccountEmails[string(l)]; ok {
		return l
	}
	return EN
}

// NoticeLangFor is the language of an account notice, whose sentences handlers word from Words: the eight, or a language
// with the core lines and the notices' too, else English.
func NoticeLangFor(l Lang) Lang {
	if EmailGroupComplete(l, groupNotices) {
		return l
	}
	return EN
}

// CardLangFor is the language of a Card warning email: the eight, or a language with the core lines and the cards'
// lines too (its sentence and the email around it are then both the language's), else English entirely.
func CardLangFor(l Lang) Lang {
	if EmailGroupComplete(l, groupCards) {
		return l
	}
	return EN
}

// EmailCardLanguagesMissing are the languages that have the core lines and not yet the cards': the Card warning and
// a held action's reason read English in them.
func EmailCardLanguagesMissing() []string {
	var out []string
	for tag := range CodeSignInEmailMore {
		if !EmailGroupComplete(Lang(tag), groupCards) {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// EmailNoticeLanguagesMissing are the languages that have the core lines and not yet the notices'.
func EmailNoticeLanguagesMissing() []string {
	var out []string
	for tag := range CodeSignInEmailMore {
		if !EmailGroupComplete(Lang(tag), groupNotices) {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// EmailGroupName is the group a line of the table is in: "core", "notices" or "cards".
func EmailGroupName(key string) string { return lineGroup(key) }

// OfNotice is the words a handler words an account notice from: the language's when it has the notices' sentences
// and the core they are filled with (a role's name, "an admin"), else English entirely, so a notice is never an English
// sentence with another language's words inside it. AccountNotice lays the notice out in the same language
// (NoticeLangFor).
func OfNotice(l Lang) *Words { return Of(NoticeLangFor(l)) }

// ForgetEmailWords drops what was built for a language, for tests that change EmailMore.
func ForgetEmailWords(tag string) { forgetEmailWords(tag) }
