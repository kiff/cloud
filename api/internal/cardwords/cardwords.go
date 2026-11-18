// Package cardwords says a Card's terms in the owner's words, the lines the
// tafs apps show before the owner confirms: "Refund
// a payment: up to €40 each", "€300 in total a day", "20 calls a day"; on a
// Card that holds for the owner, each limit says it asks ("20 calls a day,
// then it asks you"). KIFF writes them, so every app shows the same words and
// none keeps its own copy. Each is in the reader's language (internal/words,
// its Cards block); a tool's title and an action in words stay as they are.
//
// The wording is the one the iOS app used (DraftCard.lines): each action by
// its tool's title, never its name; where two read alike, each adds its
// action in words; an amount in the unit KIFF declares for it, or a bare
// number that says it has no currency, never a guessed one.
package cardwords

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
)

// PerAction is one per-action limit of a grant, as a Card's view shows it.
type PerAction struct {
	Quantity string
	Max      int64
	Unit     string
	Scale    int
}

// Grant is one action a Card allows, with its tool's title when it has one.
type Grant struct {
	Action    string
	Title     string
	PerAction []PerAction
}

// Aggregate is one of a Card's totals.
type Aggregate struct {
	Quantity string
	Limit    int64
	Window   string
	Unit     string
	Scale    int
	// Per is the parameter a per-item total counts each value of apart
	// ("invoice"): its line ends "for each invoice". Empty for one total.
	Per string
}

// Card is a Card's terms, as the owner reads them.
type Card struct {
	Grants     []Grant
	Aggregates []Aggregate
	// OnExceed is what happens past a limit: mandate.OnExceedOwner holds
	// the call for the owner, so its lines say it asks; anything else
	// refuses it, and its lines read as bare limits.
	OnExceed string
}

// IsSum reports whether quantity adds an amount up ("sum(amount)"), not a count.
func IsSum(quantity string) bool { return strings.HasPrefix(strings.ToLower(quantity), "sum(") }

// Alike is the names, lowercased, that more than one of grants reads as:
// those add their action in words, so two tools never read the same.
func Alike(grants []Grant) map[string]bool {
	seen, twice := map[string]bool{}, map[string]bool{}
	for _, g := range grants {
		n := strings.ToLower(Name(g.Action, g.Title))
		if seen[n] {
			twice[n] = true
		}
		seen[n] = true
	}
	return twice
}

// Lines is a Card's lines, where a grant whose name is in alike (from every
// Card the owner reads together) adds its action in words. A total in a
// window KIFF doesn't have is an error, never a guessed line: the owner
// signs these words. They are in w's language.
func Lines(c Card, alike map[string]bool, w *words.Words) ([]string, error) {
	out := make([]string, 0, len(c.Grants)+len(c.Aggregates))
	// Past a limit, a Card that holds for the owner asks them: a bare limit
	// would read as a hard stop.
	asks := c.OnExceed == mandate.OnExceedOwner
	for _, g := range c.Grants {
		name := Name(g.Action, g.Title)
		caps := make([]string, 0, len(g.PerAction))
		for _, p := range g.PerAction {
			if IsSum(p.Quantity) {
				caps = append(caps, w.UpToEach(Money(p.Max, p.Unit, p.Scale, w)))
			} else {
				caps = append(caps, w.UpToEach(strconv.FormatInt(p.Max, 10)))
			}
		}
		action := name
		if alike[strings.ToLower(name)] {
			action = name + " · " + ToolWords(g.Action)
		}
		if len(caps) > 0 {
			action += ": " + strings.Join(caps, ", ")
			if asks {
				action += w.MoreAsksFirst
			}
		}
		out = append(out, action)
	}
	for _, a := range c.Aggregates {
		if !KnownWindow(a.Window) {
			return nil, fmt.Errorf("a total in window %q, which KIFF doesn't have", a.Window)
		}
		then := ""
		if asks {
			then = w.ThenAsks
		}
		each := ""
		if a.Per != "" {
			each = " " + EachItem(a.Per, w)
		}
		if IsSum(a.Quantity) {
			out = append(out, Money(a.Limit, a.Unit, a.Scale, w)+" "+w.WindowAmount[a.Window]+each+then)
			continue
		}
		if asks && a.Limit == 0 && a.Per == "" {
			out = append(out, w.EachCallAsks)
			continue
		}
		out = append(out, w.CallCount(a.Limit)+" "+w.WindowCount[a.Window]+each+then)
	}
	return out, nil
}

// EachItem is how a per-item total names what it counts apart: "for each
// invoice", from the parameter's name in words.
func EachItem(per string, w *words.Words) string {
	return w.ForEach(strings.TrimSpace(strings.ReplaceAll(strings.ToLower(per), "_", " ")))
}

// Calls is a count total in words, "20 calls a day" or "1 call in any
// hour"; false for a window KIFF doesn't have.
func Calls(limit int64, window string, w *words.Words) (string, bool) {
	if !KnownWindow(window) {
		return "", false
	}
	return w.CallCount(limit) + " " + w.WindowCount[window], true
}

// KnownWindow reports whether KIFF has window, so its totals have words.
// Every language has words for the same windows (words' completeness test).
func KnownWindow(window string) bool {
	_, ok := words.Of(words.EN).WindowAmount[window]
	return ok
}

// Name is what the owner reads for an action: its tool's title, else the
// action in words.
func Name(action, title string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	return ToolWords(action)
}

var grantPrefix = regexp.MustCompile(`^[a-z0-9]*[0-9][a-z0-9]*_`)

// ToolWords is a tool's name in words: past a server's prefix
// ("stripe.create_refund", "kiff__refund_order") and a grant's own
// ("bxkwf7vi_list_charges"), separators as spaces.
func ToolWords(tool string) string {
	name := tool
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if m := grantPrefix.FindString(name); len(m) >= 7 && len(m) < len(name) {
		name = name[len(m):]
	}
	words := strings.Join(strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' }), " ")
	if words == "" {
		return tool
	}
	return words
}

// Words is a vendor's own tool name in words, as it is: its separators as
// spaces, nothing taken off. For a name KIFF knows to be the vendor's, so no
// prefix or suffix of a relay's is guessed at.
func Words(name string) string {
	w := strings.Join(strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' }), " ")
	if w == "" {
		return name
	}
	return w
}

var symbols = map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥"}

// Money is an amount in its unit, as w's language writes it: in English
// "€40", "€1,250.50", "CHF 1,000", "12 points"; in German "1.250,50 €", in
// Dutch "€ 1.250,50". The unit's decimal places (scale) show only when it
// isn't whole. Without a unit it says so, "30,000 (no currency)", never a
// guessed currency.
func Money(minor int64, unit string, scale int, w *words.Words) string {
	style := w.Money
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return group(strconv.FormatInt(minor, 10), style.Thousands) + " " + w.NoCurrency
	}
	if scale < 0 {
		scale = 0
	}
	neg := minor < 0
	if neg {
		minor = -minor
	}
	s := strconv.FormatInt(minor, 10)
	for len(s) <= scale {
		s = "0" + s
	}
	whole, frac := s[:len(s)-scale], s[len(s)-scale:]
	n := group(whole, style.Thousands)
	// A currency with a fraction shows two places at least: "€1,250.50",
	// never "€1,250.5".
	_, isSym := symbols[strings.ToUpper(unit)]
	if (isSym || isoCode.MatchString(unit)) && len(frac) == 1 {
		frac += "0"
	}
	if strings.Trim(frac, "0") != "" {
		if w.DecimalComma {
			n += "," + frac
		} else {
			n += "." + frac
		}
	}
	sign := ""
	if neg {
		sign = "-"
	}
	label, gap := "", style.CurrencyGap
	if sym, ok := symbols[strings.ToUpper(unit)]; ok {
		label = sym
	} else if isoCode.MatchString(unit) {
		// A code never touches the number: "CHF 1,000".
		label = strings.ToUpper(unit)
		if gap == "" {
			gap = " "
		}
	} else {
		return sign + n + " " + unit
	}
	if style.CurrencyAfter {
		return sign + n + gap + label
	}
	return sign + label + gap + n
}

var isoCode = regexp.MustCompile(`^[A-Za-z]{3}$`)

// MoneyDecimal is Money for an amount written as a decimal ("45.00"): its
// decimal places are its scale. A value that isn't a decimal number is
// shown as it was written, with its unit.
func MoneyDecimal(value, unit string, w *words.Words) string {
	v := strings.TrimSpace(value)
	whole, frac, _ := strings.Cut(v, ".")
	neg := strings.HasPrefix(whole, "-")
	digits := strings.TrimPrefix(whole, "-") + frac
	minor, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || digits == "" {
		return strings.TrimSpace(v + " " + unit)
	}
	if neg {
		minor = -minor
	}
	return Money(minor, unit, len(frac), w)
}

// group puts sep between thousands in a run of digits.
func group(digits, sep string) string {
	neg := strings.HasPrefix(digits, "-")
	digits = strings.TrimPrefix(digits, "-")
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
