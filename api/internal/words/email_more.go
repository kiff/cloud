package words

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
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

// EmailLineNames are the names an EmailLines needs, with a map line's keys, sorted.
func EmailLineNames() []string {
	var out []string
	for _, n := range emailLineNames {
		f := reflect.ValueOf(en).FieldByName(n)
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
		case reflect.Func:
			args := make([]reflect.Value, f.Type().NumIn())
			for i := range args {
				args[i] = reflect.ValueOf(fmt.Sprintf("{%d}", i))
			}
			out[n] = f.Call(args)[0].String()
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
	for name, en := range english {
		got, ok := lines[name]
		switch {
		case !ok || strings.TrimSpace(got) == "":
			out = append(out, name+" is missing")
		case strings.Join(placeholders(got), " ") != strings.Join(placeholders(en), " "):
			out = append(out, fmt.Sprintf("%s has parts %v, English %v", name, placeholders(got), placeholders(en)))
		case name == "Months" && len(strings.Split(got, "|")) != 12:
			out = append(out, "Months is not twelve names joined by |")
		}
	}
	for name := range lines {
		if _, ok := english[name]; !ok {
			out = append(out, name+" is not a line of the emails")
		}
	}
	sort.Strings(out)
	return out
}

var (
	moreMu    sync.Mutex
	moreWords = map[string]*Words{}
)

// emailWordsFor is English with lang's email lines laid over it, when lang has them.
func emailWordsFor(lang Lang) (*Words, bool) {
	lines, ok := EmailMore[string(lang)]
	if !ok {
		return nil, false
	}
	moreMu.Lock()
	defer moreMu.Unlock()
	if w, ok := moreWords[string(lang)]; ok {
		return w, true
	}
	w := en // a copy: English's own lines stay English
	setEmailLines(&w, lines)
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

func setEmailLines(w *Words, lines EmailLines) {
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
		case reflect.Func:
			v, ok := lines[n]
			if !ok {
				continue
			}
			f.Set(reflect.MakeFunc(f.Type(), func(in []reflect.Value) []reflect.Value {
				args := make([]string, len(in))
				for i, a := range in {
					args[i] = a.String()
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
		if lines, ok := EmailMore[tag]; !ok || len(EmailLinesProblems(lines)) > 0 {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}
