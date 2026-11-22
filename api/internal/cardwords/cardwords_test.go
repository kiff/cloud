package cardwords

import (
	"slices"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/words"
)

var en = words.Of(words.EN)

func TestMoney(t *testing.T) {
	for _, c := range []struct {
		minor int64
		unit  string
		scale int
		want  string
	}{
		{4000, "USD", 2, "$40"},
		{4550, "usd", 2, "$45.50"},
		{125050, "EUR", 2, "€1,250.50"},
		{5, "EUR", 2, "€0.05"},
		{100000, "CHF", 2, "CHF 1,000"},
		{1200, "JPY", 0, "¥1,200"},
		{12, "points", 0, "12 points"},
		{30000, "", 0, "30,000 (no currency)"},
		{-4000, "USD", 2, "-$40"},
	} {
		if got := Money(c.minor, c.unit, c.scale, en); got != c.want {
			t.Errorf("Money(%d, %q, %d) = %q, want %q", c.minor, c.unit, c.scale, got, c.want)
		}
	}
}

func TestToolWords(t *testing.T) {
	for in, want := range map[string]string{
		"stripe.create_refund":  "create refund",
		"kiff__refund_order":    "refund order",
		"bxkwf7vi_list_charges": "list charges",
		"list_charges":          "list charges",
		"kiff-pending":          "kiff pending",
		"memory":                "memory",
	} {
		if got := ToolWords(in); got != want {
			t.Errorf("ToolWords(%q) = %q, want %q", in, got, want)
		}
	}
}

// The lines the owner confirms: each action by its title, its caps in its
// unit, then the totals; two grants that read alike each add their action.
func TestLines(t *testing.T) {
	c := Card{
		Grants: []Grant{
			{Action: "create_refund", Title: "Refund a payment", PerAction: []PerAction{{Quantity: "sum(amount)", Max: 4000, Unit: "USD", Scale: 2}}},
			{Action: "stripe_api_read", Title: "Read Stripe API data"},
			{Action: "x9k2ab_get_balance", Title: "Read Stripe API data"},
			{Action: "send_note", PerAction: []PerAction{{Quantity: "count", Max: 3}}},
		},
		Aggregates: []Aggregate{{Quantity: "sum(amount)", Limit: 30000, Window: "calendar_day", Unit: "USD", Scale: 2}},
	}
	got, err := Lines(c, Alike(c.Grants), en)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Refund a payment: up to $40 each",
		"Read Stripe API data · stripe api read",
		"Read Stripe API data · get balance",
		"send note: up to 3 each",
		"$300 in total a day",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("lines\n%q\nwant\n%q", got, want)
	}
}

// Each of KIFF's windows has its own words, an amount's and a count's; a
// rolling window never reads as a day that resets at midnight.
func TestLinesForEachWindow(t *testing.T) {
	for _, c := range []struct{ window, amount, count string }{
		{"calendar_day", "$300 in total a day", "20 calls a day"},
		{"rolling_24h", "$300 in any 24 hours", "20 calls in any 24 hours"},
		{"rolling_1h", "$300 in any hour", "20 calls in any hour"},
		{"lifetime", "$300 in total", "20 calls in total"},
		{"rolling_7d", "$300 in any 7 days", "20 calls in any 7 days"},
	} {
		got, err := Lines(Card{Aggregates: []Aggregate{
			{Quantity: "sum(amount)", Limit: 30000, Window: c.window, Unit: "USD", Scale: 2},
			{Quantity: "count", Limit: 20, Window: c.window},
		}}, nil, en)
		if err != nil || !slices.Equal(got, []string{c.amount, c.count}) {
			t.Errorf("%s: %q %v, want %q, %q", c.window, got, err, c.amount, c.count)
		}
		if !KnownWindow(c.window) {
			t.Errorf("%s isn't known", c.window)
		}
	}
	if got, _ := Lines(Card{Aggregates: []Aggregate{{Quantity: "count", Limit: 1, Window: "rolling_1h"}}}, nil, en); !slices.Equal(got, []string{"1 call in any hour"}) {
		t.Errorf("one call: %q", got)
	}
	for _, w := range []string{"calendar_week", "", "month"} {
		if _, err := Lines(Card{Aggregates: []Aggregate{{Quantity: "count", Limit: 1, Window: w}}}, nil, en); err == nil || KnownWindow(w) {
			t.Errorf("window %q: no error", w)
		}
	}
}

// A Card that holds for the owner says, for each limit, that past it the
// call asks them; a Card that refuses reads as bare limits.
func TestLinesOnACardThatAsks(t *testing.T) {
	grants := []Grant{{Action: "create_refund", Title: "Refund a payment", PerAction: []PerAction{{Quantity: "sum(amount)", Max: 4000, Unit: "USD", Scale: 2}}}}
	for _, c := range []struct {
		onExceed string
		limit    int64
		window   string
		want     []string
	}{
		{"owner", 0, "calendar_day", []string{"Refund a payment: up to $40 each; more asks you first", "$300 in total a day, then it asks you", "Each call asks you first"}},
		{"owner", 1, "rolling_1h", []string{"Refund a payment: up to $40 each; more asks you first", "$300 in any hour, then it asks you", "1 call in any hour, then it asks you"}},
		{"owner", 20, "calendar_day", []string{"Refund a payment: up to $40 each; more asks you first", "$300 in total a day, then it asks you", "20 calls a day, then it asks you"}},
		{"owner", 20, "rolling_24h", []string{"Refund a payment: up to $40 each; more asks you first", "$300 in any 24 hours, then it asks you", "20 calls in any 24 hours, then it asks you"}},
		{"owner", 20, "lifetime", []string{"Refund a payment: up to $40 each; more asks you first", "$300 in total, then it asks you", "20 calls in total, then it asks you"}},
		{"refuse", 0, "calendar_day", []string{"Refund a payment: up to $40 each", "$300 in total a day", "0 calls a day"}},
		{"refuse", 20, "calendar_day", []string{"Refund a payment: up to $40 each", "$300 in total a day", "20 calls a day"}},
		{"", 20, "calendar_day", []string{"Refund a payment: up to $40 each", "$300 in total a day", "20 calls a day"}},
	} {
		got, err := Lines(Card{OnExceed: c.onExceed, Grants: grants, Aggregates: []Aggregate{
			{Quantity: "sum(amount)", Limit: 30000, Window: c.window, Unit: "USD", Scale: 2},
			{Quantity: "count", Limit: c.limit, Window: c.window},
		}}, nil, en)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%q %d %s:\n%q %v\nwant\n%q", c.onExceed, c.limit, c.window, got, err, c.want)
		}
	}
	// A grant with no cap says nothing about asking: there's no limit to pass.
	if got, _ := Lines(Card{OnExceed: "owner", Grants: []Grant{{Action: "get_order", Title: "Read an order"}}}, nil, en); !slices.Equal(got, []string{"Read an order"}) {
		t.Errorf("no cap: %q", got)
	}
}

// ToolWords strips a grant's prefix, a run of letters and digits with a
// digit in it, at least six long: that pins today's cases, a real word with
// a digit among them ("oauth2_token" reads "token"). It only names a tool
// that has no title, and goes once every tool KIFF lists carries one.
func TestToolWordsPinsTheGrantPrefix(t *testing.T) {
	for in, want := range map[string]string{"oauth2_token": "token", "s3_upload": "s3 upload", "abc123": "abc123"} {
		if got := ToolWords(in); got != want {
			t.Errorf("ToolWords(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCallsAndOrdinal(t *testing.T) {
	if s, ok := Calls(20, "calendar_day", en); !ok || s != "20 calls a day" {
		t.Errorf("Calls: %q %v", s, ok)
	}
	if _, ok := Calls(1, "month", en); ok {
		t.Error("a window KIFF doesn't have has words")
	}
	for n, want := range map[int64]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 101: "101st", 111: "111th"} {
		if got := en.Ordinal(n); got != want {
			t.Errorf("Ordinal(%d) = %q", n, got)
		}
	}
}

// A per-item total says what it counts apart: "1 call in any 7 days for each invoice".
func TestLinesForAPerItemTotal(t *testing.T) {
	got, err := Lines(Card{Aggregates: []Aggregate{
		{Quantity: "count", Limit: 1, Window: "rolling_7d", Per: "invoice_id"},
		{Quantity: "sum(amount)", Limit: 10000, Window: "calendar_day", Unit: "EUR", Scale: 2, Per: "customer"},
	}, OnExceed: "owner"}, nil, en)
	want := []string{"1 call in any 7 days for each invoice id, then it asks you", "€100 in total a day for each customer, then it asks you"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("got %q %v, want %q", got, err, want)
	}
	// A count of 0 per item still says its item: it isn't "each call asks you first".
	got, _ = Lines(Card{Aggregates: []Aggregate{{Quantity: "count", Limit: 0, Window: "rolling_7d", Per: "to"}}, OnExceed: "owner"}, nil, en)
	if !slices.Equal(got, []string{"0 calls in any 7 days for each to, then it asks you"}) {
		t.Errorf("zero per item: %q", got)
	}
}

// An amount as each language writes it: its separators, and its currency
// after the amount or before it.
func TestMoneyInALanguage(t *testing.T) {
	for _, c := range []struct {
		l     words.Lang
		minor int64
		unit  string
		want  string
	}{
		{words.DE, 125050, "EUR", "1.250,50\u00a0€"},
		{words.DE, 100000, "CHF", "1.000\u00a0CHF"},
		{words.FR, 125050, "EUR", "1\u00a0250,50\u00a0€"},
		{words.ES, 4000, "EUR", "40\u00a0€"},
		{words.IT, 4550, "EUR", "45,50\u00a0€"},
		{words.PT, 125050, "EUR", "1\u00a0250,50\u00a0€"},
		{words.NL, 125050, "EUR", "€\u00a01.250,50"},
		{words.NL, 100000, "CHF", "CHF\u00a01.000"},
		{words.PL, 30000, "", "30\u00a0000 (bez waluty)"},
		{words.DE, 12, "points", "12 points"},
		{words.DE, -4000, "EUR", "-40\u00a0€"},
	} {
		scale := 2
		if c.unit == "" || c.unit == "points" {
			scale = 0
		}
		if got := Money(c.minor, c.unit, scale, words.Of(c.l)); got != c.want {
			t.Errorf("%s: Money(%d, %q) = %q, want %q", c.l, c.minor, c.unit, got, c.want)
		}
	}
	if got := MoneyDecimal("45.00", "EUR", words.Of(words.FR)); got != "45\u00a0€" {
		t.Errorf("fr decimal = %q", got)
	}
	// A currency's one-digit fraction shows two places; a unit that isn't
	// money keeps what it was given.
	if got := MoneyDecimal("1250.5", "EUR", words.Of(words.DE)); got != "1.250,50\u00a0€" {
		t.Errorf("de one-digit fraction = %q", got)
	}
	if got := MoneyDecimal("2.5", "points", words.Of(words.DE)); got != "2,5 points" {
		t.Errorf("points = %q", got)
	}
}

// A Card's lines in the reader's language: the cap, the total and its
// window, what a Card that asks does past them, and a count's plural.
func TestLinesInALanguage(t *testing.T) {
	c := Card{OnExceed: "owner",
		Grants: []Grant{{Action: "create_refund", Title: "Refund a payment", PerAction: []PerAction{{Quantity: "sum(amount)", Max: 4000, Unit: "EUR", Scale: 2}}}},
		Aggregates: []Aggregate{
			{Quantity: "sum(amount)", Limit: 30000, Window: "calendar_day", Unit: "EUR", Scale: 2},
			{Quantity: "count", Limit: 5, Window: "rolling_7d", Per: "invoice"},
		}}
	for l, want := range map[words.Lang][]string{
		words.EN: {"Refund a payment: up to €40 each; more asks you first", "€300 in total a day, then it asks you", "5 calls in any 7 days for each invoice, then it asks you"},
		words.DE: {"Refund a payment: bis zu 40\u00a0€ pro Aufruf; bei mehr wirst du zuerst gefragt", "300\u00a0€ insgesamt pro Tag, danach wirst du gefragt", "5 Aufrufe in jedem Zeitraum von 7 Tagen je invoice, danach wirst du gefragt"},
		words.NL: {"Refund a payment: tot €\u00a040 per aanroep; voor meer vraagt het je eerst", "€\u00a0300 in totaal per dag, daarna vraagt het je", "5 aanroepen in elke periode van 7 dagen per invoice, daarna vraagt het je"},
		words.PL: {"Refund a payment: do 40\u00a0€ na wywołanie; powyżej tego najpierw Cię zapyta", "300\u00a0€ łącznie dziennie, potem Cię zapyta", "5 wywołań w dowolnych 7 dniach dla każdej wartości invoice, potem Cię zapyta"},
	} {
		got, err := Lines(c, Alike(c.Grants), words.Of(l))
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", l, got, want)
		}
	}
	// Polish's three forms of a count.
	pl := words.Of(words.PL)
	for n, want := range map[int64]string{1: "1 wywołanie dziennie", 3: "3 wywołania dziennie", 5: "5 wywołań dziennie", 12: "12 wywołań dziennie", 22: "22 wywołania dziennie"} {
		if got, _ := Calls(n, "calendar_day", pl); got != want {
			t.Errorf("pl Calls(%d) = %q, want %q", n, got, want)
		}
	}
}

// An amount, written in every language that has its words, reads back as the amount: with the language's own marks
// taken out (its currency, its gap, its thousands separator, its decimal mark) 1,234,567.89 is digits 123456789.
func TestAnAmountReadsBackInEveryLanguage(t *testing.T) {
	langs := []words.Lang{words.EN, words.DE, words.FR, words.ES, words.IT, words.PT, words.NL, words.PL}
	for tag := range words.EmailMore {
		langs = append(langs, words.Lang(tag))
	}
	for _, l := range langs {
		w := words.Of(l)
		mark := "."
		if w.DecimalComma {
			mark = ","
		}
		if w.Money.Thousands == mark {
			t.Errorf("%s: the thousands separator %q is the decimal mark", l, mark)
		}
		got := Money(123456789, "points", 2, w)
		rest := strings.TrimSpace(strings.ReplaceAll(got, "points", ""))
		i := strings.LastIndex(rest, mark)
		if i < 0 {
			t.Errorf("%s: %q has no decimal mark %q", l, got, mark)
			continue
		}
		whole, frac := rest[:i], rest[i+len(mark):]
		if w.Money.Thousands != "" {
			whole = strings.ReplaceAll(whole, w.Money.Thousands, "")
		}
		whole = strings.TrimSpace(strings.ReplaceAll(whole, " ", ""))
		if whole+frac != "123456789" || len(frac) != 2 {
			t.Errorf("%s: %q reads back as %q.%q, want 1234567.89", l, got, whole, frac)
		}
		// A currency's symbol and gap come out the same way.
		eur := Money(123456789, "EUR", 2, w)
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, eur)
		if digits != "123456789" || !strings.Contains(eur, "€") {
			t.Errorf("%s: %q is not the amount in euros", l, eur)
		}
	}
}
