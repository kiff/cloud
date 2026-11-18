package words

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// Every language has every line: no empty string, no missing function,
// the same reason codes as English, and no line left in English.
func TestEveryLanguageHasEveryLine(t *testing.T) {
	src := flatten(reflect.ValueOf(en))
	for _, l := range All {
		fields := flatten(reflect.ValueOf(*Of(l)))
		for i, fl := range fields {
			f, name := fl.v, fl.name
			srcField := src[i].v
			switch f.Kind() {
			case reflect.String:
				if strings.TrimSpace(f.String()) == "" {
					t.Errorf("%s: %s is empty", l, name)
				}
				if l != EN && f.String() == srcField.String() {
					t.Errorf("%s: %s is still in English: %q", l, name, f.String())
				}
			case reflect.Func:
				if f.IsNil() {
					t.Errorf("%s: %s is missing", l, name)
				}
			case reflect.Map:
				for _, k := range srcField.MapKeys() {
					v := f.MapIndex(k)
					if !v.IsValid() || strings.TrimSpace(v.String()) == "" {
						t.Errorf("%s: %s has no %v", l, name, k)
					}
				}
				if f.Len() != srcField.Len() {
					t.Errorf("%s: %s has %d lines, English %d", l, name, f.Len(), srcField.Len())
				}
			case reflect.Array:
				for j := 0; j < f.Len(); j++ {
					if strings.TrimSpace(f.Index(j).String()) == "" {
						t.Errorf("%s: %s[%d] is empty", l, name, j)
					}
				}
				if l != EN && f.Interface() == srcField.Interface() {
					t.Errorf("%s: %s is still in English", l, name)
				}
			}
		}
		for code, line := range en.Reasons {
			got, ok := Of(l).Reasons[code]
			if !ok || strings.TrimSpace(got) == "" {
				t.Errorf("%s: no reason for %s", l, code)
			} else if l != EN && got == line {
				t.Errorf("%s: reason %s is still in English", l, code)
			}
		}
		// The functions write a line, and in a language other than English.
		w2 := Of(l)
		lines := []string{w2.WindowNoneMinutes(5), w2.DayMonth(3, time.November, "09:14"),
			w2.WhyLost("", true), w2.WhyLost("Stripe", false), w2.WhyRefused("", false), w2.WhyRefused("stripe", true),
			w2.CallAtVendor("Refund", "Stripe"), w2.CallByTaf("Refunds"), w2.ResourceMissing("charge"),
			w2.ResourceNotOnCard("charge", "ch_9"), w2.OverPerCall("150 EUR", "100 EUR"), w2.OverTotal("150 EUR", "1000 EUR", "900 EUR"),
			w2.HeldIntro("Refunds", "refund", " 4700 amount_eur"), w2.HeldSteps("Return first"), w2.HeldExpires("2 Jan 2027 15:04 UTC"),
			w2.WarnUnusual("3,2"), w2.WarnUsed("80", "today"), w2.DateTime(time.Date(2027, 1, 2, 15, 4, 0, 0, time.UTC)),
			w2.WarningReason("unusual", 3.2, 80, "rolling_7d"), w2.WarningReason("spent", 0, 100, "lifetime"),
			w2.AccountNamed("Acme"), w2.InviteSubject(""), w2.InviteSubject("Acme"),
			w2.InviteIntro(w2.AnAdmin, w2.AnAccount, "viewer", w2.InviteRoleWhat["viewer"]), w2.InviteAccept("https://app.kiff.dev/x"),
			w2.InviteExpires("2 Jan 2027 15:04 UTC"), w2.AuthoritySubject("approver", "Acme"),
			w2.AuthorityInvited("a@example.com", "approver", "Acme", w2.AnAdmin),
			w2.AuthorityChanged("a@example.com", "viewer", "approver", "Acme", "b@example.com"), w2.JoinedSubject("Acme"),
			w2.JoinedUnknown("a@example.com", "viewer", "Acme"), w2.JoinedOther("a@example.com", "b@example.com", "viewer", "Acme"),
			w2.JoinedSelf("a@example.com", "viewer", "Acme"), w2.CardChangeBody("a@example.com", "Refunds", "refund", "ch_9"),
			w2.CardChangeBody(w2.Someone, "", "refund", ""), w2.CardChangeCards("m_1, m_2"), w2.SoDOffBody(w2.AnAdmin, "Acme"),
			w2.RelinkSubject(w2.YourPhone), w2.RelinkBody("Pixel"), w2.PackAskBody(w2.AMember, "Rayo"), w2.RaiseAskBody(w2.AMember),
			w2.AskReason("month end"), w2.PackAskHow("https://app.kiff.dev/dashboard/settings"),
			w2.NewDeviceOn("Chrome", "Android"), w2.NewDeviceZone("Paris"), w2.NewDeviceNear("Paris, France")}
		for j, line := range lines {
			if strings.TrimSpace(line) == "" {
				t.Errorf("%s: function line %d is empty", l, j)
			}
		}
		if l != EN && (w2.InviteIntro("x", "y", "viewer", "z") == en.InviteIntro("x", "y", "viewer", "z") ||
			w2.CardChangeBody("x", "y", "z", "w") == en.CardChangeBody("x", "y", "z", "w")) {
			t.Errorf("%s: a membership line is still in English", l)
		}
		if l != EN && (w2.NewDeviceOn("x", "y") == en.NewDeviceOn("x", "y") || w2.NewDeviceZone("x") == en.NewDeviceZone("x") ||
			w2.NewDeviceNear("x") == en.NewDeviceNear("x")) {
			t.Errorf("%s: a new-device line is still in English", l)
		}
		if l != EN && w2.WhyLost("", true) == en.WhyLost("", true) {
			t.Errorf("%s: WhyLost is still in English", l)
		}
	}
}

// The language is the best of the eight by q, the earlier on a tie, and
// English otherwise.
func TestFromHeader(t *testing.T) {
	for header, want := range map[string]Lang{
		"":                              EN,
		"es-CO, fr-FR;q=0.9, en;q=0.8":  ES,
		"fr-FR, en;q=0.9, es;q=0.8":     FR,
		"de-AT":                         DE,
		"ja, de;q=0.5":                  DE,
		"ja, zh":                        EN,
		"en-US, es;q=0.5":               EN,
		"es;q=0, en;q=0.4":              EN,
		"PL-pl":                         PL,
		"es;q=oops, nl":                 NL,
		"fr;q=0.9, es;q=0.7, en;q=0.7":  FR,
		"en;q=0.7, it;q=0.7":            EN,
		"pt-BR, es-419;q=0.6, en;q=0.5": PT,
		"de;q=2, it":                    IT,
	} {
		if got := FromHeader(header); got != want {
			t.Errorf("%q: %s, want %s", header, got, want)
		}
	}
	if Parse("de-CH") != DE || Parse("xx") != EN || Parse("") != EN {
		t.Error("Parse")
	}
}

// Polish counts minutes as Polish does; amounts use a decimal comma but in English.
func TestPolishMinutesAndDecimalComma(t *testing.T) {
	for n, want := range map[int]string{2: "minuty", 4: "minuty", 5: "minut", 12: "minut", 22: "minuty", 25: "minut"} {
		if got := Of(PL).WindowNoneMinutes(n); !strings.HasSuffix(got, want) {
			t.Errorf("%d: %q", n, got)
		}
	}
	if Of(EN).Number("150.00") != "150.00" || Of(DE).Number("150.00") != "150,00" {
		t.Error("decimal")
	}
}

// English composes a warning's reason word for word as KIFF stored it, so
// composing on read changes nothing for an English reader.
func TestEnglishWarningReason(t *testing.T) {
	w := Of(EN)
	if got := w.WarningReason("spent", 0, 100, "calendar_day"); got != "This Card total is spent. 100% of this total is used today. Review this window’s room, change the Card, or pause its agent." {
		t.Errorf("spent: %q", got)
	}
	if got := w.WarningReason("unusual", 3.25, -1, "lifetime"); got != "Recent usage is 3.2x its equivalent seven-day average. This total never resets. Add room, change the Card, or pause its agent." && got != "Recent usage is 3.3x its equivalent seven-day average. This total never resets. Add room, change the Card, or pause its agent." {
		t.Errorf("unusual: %q", got)
	}
	if got := Of(DE).DateTime(time.Date(2027, 3, 2, 15, 4, 0, 0, time.UTC)); got != "2. März 2027 15:04 UTC" {
		t.Errorf("de date: %q", got)
	}
	if got := w.DateTime(time.Date(2006, 1, 2, 15, 4, 0, 0, time.UTC)); got != "2 Jan 2006 15:04 UTC" {
		t.Errorf("en date: %q", got)
	}
}

// The same fields as mandate's TestWarningsHourlyRunwayBurstEscalationAndDedup
// compose the reasons it pins as stored, word for word.
func TestWarningReasonMatchesKIFFsStoredEnglish(t *testing.T) {
	burst := mandate.CardWarning{Kind: "burst", Used: 30, Ceiling: 40, Window: mandate.WindowRollingHour}
	if got := WarningReason(burst, EN); got != "A new Card used more than its burst threshold at the start of this window. 75% of this total is used over the last hour. Review this window’s room, change the Card, or pause its agent." {
		t.Errorf("burst: %q", got)
	}
	spent := mandate.CardWarning{Kind: "spent", Used: 30, Ceiling: 30, Window: mandate.WindowRollingHour}
	if got := WarningReason(spent, EN); got != "This Card total is spent. 100% of this total is used over the last hour. Review this window’s room, change the Card, or pause its agent." {
		t.Errorf("spent: %q", got)
	}
	if WarningReason(spent, DE) == WarningReason(spent, EN) {
		t.Error("German is English")
	}
}

type field struct {
	name string
	v    reflect.Value
}

// flatten lists a Words' fields, with an embedded block's (Membership)
// in its place, so every line of every block is checked.
func flatten(v reflect.Value) []field {
	var out []field
	for i := 0; i < v.NumField(); i++ {
		sf := v.Type().Field(i)
		if sf.Anonymous && v.Field(i).Kind() == reflect.Struct {
			out = append(out, flatten(v.Field(i))...)
			continue
		}
		out = append(out, field{sf.Name, v.Field(i)})
	}
	return out
}

// A device's name in the list is drawn from its kind in the reader's
// language; another kind's is the name it gave.
func TestDeviceNameByKind(t *testing.T) {
	want := map[Lang][4]string{
		EN: {"tafs on iPhone", "tafs on iPad", "tafs on Android phone", "tafs on Android tablet"},
		FR: {"tafs sur iPhone", "tafs sur iPad", "tafs sur téléphone Android", "tafs sur tablette Android"},
		ES: {"tafs en iPhone", "tafs en iPad", "tafs en teléfono Android", "tafs en tableta Android"},
		DE: {"tafs auf dem iPhone", "tafs auf dem iPad", "tafs auf dem Android-Telefon", "tafs auf dem Android-Tablet"},
	}
	for l, names := range want {
		for i, kind := range []string{"iphone", "ipad", "android_phone", "android_tablet"} {
			if got := Of(l).DeviceName(kind, "tafs en iPad"); got != names[i] {
				t.Errorf("%s %s = %q; want %q", l, kind, got, names[i])
			}
		}
		if got := Of(l).DeviceName("other", "Ana's laptop"); got != "Ana's laptop" {
			t.Errorf("%s other = %q", l, got)
		}
	}
}
