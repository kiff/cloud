package words

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// English's plural lines write what English's own functions write, for every number a Card can have.
func TestEnglishPluralLinesAreWhatEnglishWrites(t *testing.T) {
	calls := pluralFunc("en", pluralEnglish["CallCount"], false)
	ord := pluralFunc("en", pluralEnglish["Ordinal"], true)
	for n := int64(0); n <= 1000; n++ {
		if got, want := calls(n), en.CallCount(n); got != want {
			t.Fatalf("CallCount(%d) = %q, English writes %q", n, got, want)
		}
		if got, want := ord(n), en.Ordinal(n); got != want {
			t.Fatalf("Ordinal(%d) = %q, English writes %q", n, got, want)
		}
	}
}

// English's lines, laid back over English, change nothing: the warning's every sentence and the reasons read the same.
func TestEnglishCardLinesRoundTrip(t *testing.T) {
	w := en
	setEmailLines(&w, "en", EmailLinesEnglish())
	for _, kind := range []string{"spent", "unusual", "burst", "runway"} {
		for _, window := range []string{"calendar_day", "rolling_24h", "rolling_1h", "rolling_7d", "lifetime", "elsewhere"} {
			for _, pct := range []float64{-1, 0, 42.4, 100} {
				if got, want := w.WarningReason(kind, 3.25, pct, window), en.WarningReason(kind, 3.25, pct, window); got != want {
					t.Errorf("%s/%s/%v: %q, English writes %q", kind, window, pct, got, want)
				}
			}
		}
	}
	if w.Money != en.Money || w.DecimalComma != en.DecimalComma || w.NoCurrency != en.NoCurrency {
		t.Errorf("money: %+v, English %+v", w.Money, en.Money)
	}
	checks := map[string][2]string{
		"OverCalls":       {w.OverCalls(20, "21st"), en.OverCalls(20, "21st")},
		"WouldBe":         {w.WouldBe("20 calls a day", "21st"), en.WouldBe("20 calls a day", "21st")},
		"WouldBeFor":      {w.WouldBeFor("1 call in any 7 days", "2nd", "in_42"), en.WouldBeFor("1 call in any 7 days", "2nd", "in_42")},
		"OverForOneCall":  {w.OverForOneCall("$45", "$40"), en.OverForOneCall("$45", "$40")},
		"OverTheTotal":    {w.OverTheTotal("$45", "$40"), en.OverTheTotal("$45", "$40")},
		"ForEach":         {w.ForEach("invoice"), en.ForEach("invoice")},
		"EachCallAsks":    {w.EachCallAsks, en.EachCallAsks},
		"OutsideCard":     {w.OutsideCard, en.OutsideCard},
		"OverActionLimit": {w.OverActionLimit, en.OverActionLimit},
		"OverCardTotal":   {w.OverCardTotal, en.OverCardTotal},
		"NotCardResource": {w.NotCardResource, en.NotCardResource},
		"WindowCount":     {w.WindowCount["rolling_7d"], en.WindowCount["rolling_7d"]},
		"WindowAmount":    {w.WindowAmount["calendar_day"], en.WindowAmount["calendar_day"]},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: %q, English writes %q", name, c[0], c[1])
		}
	}
}

// Cards lines are a third group: with the core and not the cards, the emails are the language's and the Card warning
// and the held reason are English, whole; a part of the cards is a problem; with all of them both are the language's.
func TestTheCardLinesAreAThirdGroup(t *testing.T) {
	const tag = "nn"
	core, cards := EmailLines{}, EmailLines{}
	for k, v := range EmailLinesEnglish() {
		switch lineGroup(k) {
		case groupCore:
			core[k] = shout(tag, k, v)
		case groupCards:
			cards[k] = shout(tag, k, v)
		}
	}
	if len(cards) < 25 {
		t.Fatalf("the cards group has %d lines", len(cards))
	}
	defer func() { delete(EmailMore, tag); forgetEmailWords(tag) }()
	set := func(lines EmailLines) {
		EmailMore[tag] = lines
		forgetEmailWords(tag)
	}

	set(core)
	if got := Of(tag).WarningReason("spent", 0, 50, "calendar_day"); got != en.WarningReason("spent", 0, 50, "calendar_day") {
		t.Errorf("without the cards lines the warning is English, whole: %q", got)
	}
	if !strings.HasPrefix(Of(tag).HeldSubject, "[nn]") || EmailGroupComplete(tag, groupCards) {
		t.Errorf("the core is still the language's, the cards group not whole")
	}
	if len(EmailLinesProblems(core)) != 0 {
		t.Errorf("the core alone is a complete entry: %v", EmailLinesProblems(core))
	}

	// One line of the cards group, not the rest: a problem, and still English.
	half := EmailLines{}
	for k, v := range core {
		half[k] = v
	}
	half["OutsideCard"] = "[nn] OUTSIDE"
	set(half)
	if len(EmailLinesProblems(half)) == 0 {
		t.Errorf("a part of the cards group is a problem")
	}
	if Of(tag).OutsideCard != en.OutsideCard {
		t.Errorf("a part of the cards group is read as none: %q", Of(tag).OutsideCard)
	}

	full := EmailLines{}
	for k, v := range core {
		full[k] = v
	}
	for k, v := range cards {
		full[k] = v
	}
	set(full)
	if problems := EmailLinesProblems(full); len(problems) != 0 {
		t.Fatalf("every line: %v", problems)
	}
	w := Of(tag)
	if got := w.WarningReason("spent", 0, 50, "calendar_day"); !strings.Contains(got, "[nn]") {
		t.Errorf("with every line the warning is the language's: %q", got)
	}
	if got := w.OverForOneCall("$45", "$40"); !strings.HasPrefix(got, "[nn]") || !strings.Contains(got, "$45") || !strings.Contains(got, "$40") {
		t.Errorf("a reason has its parts: %q", got)
	}
	if got := w.OverCalls(20, "21ST"); !strings.Contains(got, "20") || !strings.Contains(got, "21ST") {
		t.Errorf("a count is written into the line: %q", got)
	}
	if got := w.CallCount(1); !strings.Contains(got, "1") || !strings.Contains(got, "[nn]") {
		t.Errorf("a count of calls: %q", got)
	}
}

// A count's words take the language's plural form, by CLDR's rules: Russian has four, Arabic six, English two.
func TestAPluralLineTakesTheLanguagesForm(t *testing.T) {
	ru := pluralFunc("ru", "one={0} звонок|few={0} звонка|many={0} звонков|other={0} звонка", false)
	for n, want := range map[int64]string{1: "1 звонок", 2: "2 звонка", 5: "5 звонков", 11: "11 звонков", 21: "21 звонок", 22: "22 звонка", 100: "100 звонков"} {
		if got := ru(n); got != want {
			t.Errorf("ru %d: %q, want %q", n, got, want)
		}
	}
	// A category a line lacks reads "other", never nothing.
	if got := pluralFunc("ru", "one={0} a|other={0} b", false)(5); got != "5 b" {
		t.Errorf("a missing category reads other: %q", got)
	}
	// English ordinals, with the teens.
	ord := pluralFunc("en", pluralEnglish["Ordinal"], true)
	for n, want := range map[int64]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 111: "111th"} {
		if got := ord(n); got != want {
			t.Errorf("en ordinal %d: %q, want %q", n, got, want)
		}
	}
	// The categories a language needs a form for.
	if got := strings.Join(pluralCategoriesOf("ru", false), ","); got != "few,many,one" && got != "few,many,one,other" {
		t.Errorf("ru categories: %s", got)
	}
	if got := pluralCategoriesOf("ar", false); len(got) != 6 {
		t.Errorf("ar categories: %v", got)
	}
	if got := strings.Join(pluralCategoriesOf("ja", false), ","); got != "other" {
		t.Errorf("ja categories: %s", got)
	}
}

// A language is held to the forms its own rules need: the lines of a Russian entry with only one and other fail.
func TestEmailPluralProblemsNameTheFormsALanguageLacks(t *testing.T) {
	lines := EmailLines{"CallCount": "one={0} a|other={0} b", "Ordinal": "other={0}."}
	if p := EmailPluralProblems("de", lines); len(p) != 0 {
		t.Errorf("German has one and other: %v", p)
	}
	p := EmailPluralProblems("ru", lines)
	if len(p) < 2 || !strings.Contains(strings.Join(p, ";"), "few") || !strings.Contains(strings.Join(p, ";"), "many") {
		t.Errorf("Russian needs few and many: %v", p)
	}
	if p := EmailPluralProblems("ru", EmailLines{"CallCount": "{0} calls"}); len(p) == 0 {
		t.Errorf("a line with no forms is a problem")
	}
	if p := EmailPluralProblems("de", EmailLines{"CallCount": "one=calls|other={0} b"}); len(p) == 0 {
		t.Errorf("a form without its number is a problem")
	}
	for tag, lines := range EmailMore {
		if p := EmailPluralProblems(tag, lines); len(p) != 0 {
			t.Errorf("%s: %v", tag, p)
		}
	}
}

// An amount's style is three named lines, and each is checked.
func TestMoneyLinesRoundTripAndAreChecked(t *testing.T) {
	for _, m := range []MoneyStyle{
		{Thousands: ",", CurrencyGap: ""},
		{Thousands: " ", CurrencyAfter: true, CurrencyGap: " "},
		{Thousands: "’", CurrencyGap: " "},
		{Thousands: "", CurrencyAfter: true, CurrencyGap: " "},
	} {
		w := en
		lines := moneyLines(m)
		for _, v := range lines {
			if v == "" {
				t.Fatalf("a separator's name is never empty: %v", lines)
			}
		}
		setEmailLines(&w, "en", lines)
		if w.Money != m {
			t.Errorf("%+v came back %+v", m, w.Money)
		}
	}
	if lineValueProblem("Money.Thousands", ",") == "" || lineValueProblem("Money.Gap", " ") == "" {
		t.Errorf("a separator is its name, not itself")
	}
	if lineValueProblem("Money.CurrencyAfter", "yes") == "" || lineValueProblem("DecimalComma", "") == "" {
		t.Errorf("a flag is true or false")
	}
}

// A line with a number as a part has it as {n}; English's lines list them, in order.
func TestACountPartIsAPlaceholder(t *testing.T) {
	lines := EmailLinesEnglish()
	if got := lines["OverCalls"]; !reflect.DeepEqual(placeholders(got), []string{"{0}", "{1}"}) {
		t.Errorf("OverCalls takes a limit and an ordinal: %q", got)
	}
	if strings.Contains(lines["OverCalls"], "70000") {
		t.Errorf("a marker was left in: %q", lines["OverCalls"])
	}
	w := en
	setEmailLines(&w, "xx", EmailLines{"OverCalls": "{1} / {0}"})
	if got := w.OverCalls(7, "8th"); got != "8th / 7" {
		t.Errorf("a number fills its part: %q", got)
	}
	_ = strconv.Itoa
}

// A line with a wrong value is a problem and the group is read as none, so a sentence is never built from a bad line.
func TestACardLineWithAWrongValueMakesTheGroupNone(t *testing.T) {
	const tag = "nn"
	full := EmailLines{}
	for k, v := range EmailLinesEnglish() {
		full[k] = shout(tag, k, v)
	}
	defer func() { delete(EmailMore, tag); forgetEmailWords(tag) }()
	for name, bad := range map[string]string{
		"Money.Thousands": ",", "Money.CurrencyAfter": "yes", "CallCount": "{0} calls", "Ordinal": "one={0}.",
		"DecimalComma": "[nn] TRUE",
	} {
		lines := EmailLines{}
		for k, v := range full {
			lines[k] = v
		}
		lines[name] = bad
		EmailMore[tag] = lines
		forgetEmailWords(tag)
		if len(EmailLinesProblems(lines)) == 0 {
			t.Errorf("%s = %q is a problem", name, bad)
		}
		if EmailGroupComplete(tag, groupCards) || Of(tag).OutsideCard != en.OutsideCard {
			t.Errorf("%s = %q: the cards group is read as none", name, bad)
		}
	}
	if _, ok := parsePlural("one={0} call"); ok {
		t.Errorf("a line needs the form for other")
	}
}

// A language's thousands separator is never its decimal mark: an entry with both a comma (or both a dot) is a problem
// and its cards group is read as none, so no amount in a warning reads two ways.
func TestAThousandsSeparatorIsNeverTheDecimalMark(t *testing.T) {
	const tag = "nn"
	full := EmailLines{}
	for k, v := range EmailLinesEnglish() {
		full[k] = shout(tag, k, v)
	}
	defer func() { delete(EmailMore, tag); forgetEmailWords(tag) }()
	for _, c := range []struct {
		thousands, comma string
		bad              bool
	}{
		{"comma", "false", false}, {"dot", "true", false}, {"nbsp", "true", false}, {"apostrophe", "false", false}, {"none", "true", false},
		{"comma", "true", true}, {"dot", "false", true},
	} {
		lines := EmailLines{}
		for k, v := range full {
			lines[k] = v
		}
		lines["Money.Thousands"], lines["DecimalComma"] = c.thousands, c.comma
		EmailMore[tag] = lines
		forgetEmailWords(tag)
		problems := len(EmailLinesProblems(lines)) > 0
		if problems != c.bad || EmailGroupComplete(tag, groupCards) == c.bad {
			t.Errorf("thousands %s, decimal comma %s: problem %v, group whole %v", c.thousands, c.comma, problems, EmailGroupComplete(tag, groupCards))
		}
	}
}
