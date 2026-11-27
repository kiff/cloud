package handlers

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/kiff/cloud/api/internal/cardwords"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
)

// A held call in the owner's words: what it is,
// its amount, why it's held and what it's on, written here so every app
// shows KIFF's own and none builds them. It reads a view after annotate
// has put the units on it and the title on it, and adds:
//
//   - summary: "Refund a payment", or with what it's on when KIFF keeps no
//     arguments ("Refund a payment order-42");
//   - amount {minor, unit, scale} and amount_text ("$45"), when the call
//     has one KIFF can name;
//   - reason_text: "$45 is over the $40 limit for one call", or the
//     breach's own words when its amount isn't in one unit;
//   - details: every argument written out in full, as tafs'
//     contracts/held-arguments.json says (a plain value on its line, a list's
//     items and an object's fields below it, indented), but the amount and
//     the gateway's own (kiff_operation_id, kiff_reason, kiff_about);
//   - answer_url: the hold's own page, where a person answers it (item 23);
//   - install {taf, n}: for Coach's held version_install, the version it
//     installs (item 22);
//   - taf_about and taf_reason: the taf's own words about the call, when it
//     sent them (taf_words.go). Apps show them as the taf's, as plain text,
//     and never in place of KIFF's amount, reason or summary.
//
// Its amount and reason are in w's language.
func withHeldWords(v map[string]any, e mandate.Exception, appBaseURL string, w *words.Words) map[string]any {
	v["answer_url"] = appBaseURL + "/needs-you/" + url.PathEscape(e.ID)
	if in := heldInstall(e.Action, v["arguments"]); in != nil {
		v["install"] = in
	}
	title, _ := v["title"].(string)
	vendor, _ := v["vendor_tool"].(string)
	name := heldName(e.Action, title, vendor)
	args, _ := v["arguments"].(json.RawMessage)
	if len(args) == 0 {
		v["summary"] = cut(strings.TrimSpace(name+" "+e.EntityID), heldLineMax)
	} else {
		v["summary"] = cut(name, heldLineMax)
	}
	breaches, _ := v["breaches"].([]map[string]any)
	units, _ := v["parameter_units"].(map[string]any)
	amount, ok := heldAmount(e.Request.Parameters, units, breaches)
	if ok {
		var unit any // a bare number has no unit: null, never ""
		if amount.unit != "" {
			unit = amount.unit
		}
		v["amount"] = map[string]any{"minor": amount.minor, "unit": unit, "scale": amount.scale}
		v["amount_text"] = cardwords.Money(amount.minor, amount.unit, amount.scale, w)
	}
	v["reason_text"] = heldReason(e, breaches, amount, ok, w)
	v["details"] = heldDetails(args, e.Request.Parameters, units)
	if e.Request.TafAbout != "" {
		v["taf_about"] = e.Request.TafAbout
	}
	if e.Request.TafReason != "" {
		v["taf_reason"] = e.Request.TafReason
	}
	return v
}

type heldMoney struct {
	minor int64
	unit  string
	scale int
}

// heldInstall is the version a held version_install installs, from its
// arguments: {taf, n}, or nil when it isn't one. Only the account's control
// server has a tool of that name (a name is one connection per account),
// so it is Coach's.
func heldInstall(action string, arguments any) map[string]any {
	if action != "version_install" {
		return nil
	}
	raw, _ := arguments.(json.RawMessage)
	var a struct {
		Taf string          `json:"taf"`
		N   json.RawMessage `json:"n"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil || strings.TrimSpace(a.Taf) == "" {
		return nil
	}
	n, err := strconv.Atoi(strings.Trim(string(a.N), `"`))
	if err != nil || n <= 0 {
		return nil
	}
	return map[string]any{"taf": a.Taf, "n": n}
}

// heldName is what the owner reads for a held call's action: its tool's
// title, with the action in words unless the title already names it (a
// vendor's shared label, "Write Stripe API data · refund payment"), so
// the one call the owner approves says which tool.
func heldName(action, title, vendorTool string) string {
	name := cardwords.Name(action, title)
	if strings.TrimSpace(title) == "" {
		if vendorTool != "" {
			return cardwords.Words(vendorTool)
		}
		return name
	}
	// The vendor's own name for the tool, when its relay said it, is the
	// action in words: nothing of the relay's name for it is guessed away.
	if vendorTool != "" {
		words := strings.Fields(strings.ToLower(cardwords.Words(vendorTool)))
		titleWords := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
			titleWords[w] = true
		}
		if len(words) == 0 || titleWords[words[len(words)-1]] {
			return name
		}
		return name + " · " + strings.Join(words, " ")
	}
	words := strings.Fields(strings.ToLower(cardwords.ToolWords(action)))
	titleWords := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		titleWords[w] = true
	}
	// planque ends a tool's name with four letters or digits of its grant
	// ("create_refund_repg"); that's never shown.
	if n := len(words); n > 1 && len(words[n-1]) == 4 && !titleWords[words[n-1]] {
		words = words[:n-1]
	}
	if len(words) == 0 || titleWords[words[len(words)-1]] {
		return name
	}
	return name + " · " + strings.Join(words, " ")
}

// heldAmount is the amount to show above the answer: a parameter whose tool
// declares its unit, in that unit; else the parameter a breach sums, as a
// bare number; else none.
func heldAmount(params map[string]int64, units map[string]any, breaches []map[string]any) (heldMoney, bool) {
	names := make([]string, 0, len(params))
	for k := range params {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		for uk, u := range units {
			if strings.EqualFold(uk, k) {
				um, _ := u.(map[string]any)
				return heldMoney{params[k], jsonStr(um["unit"]), int(jsonInt(um["scale"]))}, true
			}
		}
	}
	for _, k := range names {
		for _, b := range breaches {
			q, _ := b["quantity"].(string)
			if strings.EqualFold(q, "sum("+k+")") {
				return heldMoney{minor: params[k]}, true
			}
		}
	}
	return heldMoney{}, false
}

// heldReason is why the call is held, in one line. A call over both its
// limit for one call and a total can't be approved once, so it names the
// total. Its amounts are written as every other amount is, when the
// breach's unit is the amount's; otherwise the breach's own words.
func heldReason(e mandate.Exception, breaches []map[string]any, amount heldMoney, hasAmount bool, w *words.Words) string {
	var b map[string]any
	if !e.CanApproveOnce() {
		for _, x := range breaches {
			if x["outcome"] != string(mandate.OutcomePerActionExceeded) {
				b = x
				break
			}
		}
	}
	if b == nil && len(breaches) > 0 {
		b = breaches[0]
	}
	if b != nil && hasAmount && amount.unit != "" {
		unit, _ := b["unit"].(string)
		ambiguous, _ := b["unit_ambiguous"].(bool)
		if limit, ok := b["limit"].(int64); ok && strings.EqualFold(unit, amount.unit) && !ambiguous {
			scale := amount.scale
			if s, ok := b["scale"].(int); ok {
				scale = s
			}
			cap := cardwords.Money(limit, unit, scale, w)
			have := cardwords.Money(amount.minor, amount.unit, amount.scale, w)
			switch b["outcome"] {
			case string(mandate.OutcomePerActionExceeded):
				return w.OverForOneCall(have, cap)
			case string(mandate.OutcomeLimitReached):
				return w.OverTheTotal(have, cap)
			}
		}
	}
	// A count of calls, said as the Card says it: a limit of 0 means each
	// call asks first; otherwise which call this would be.
	if b != nil && b["outcome"] == string(mandate.OutcomeLimitReached) && b["quantity"] == "count" {
		// As the view built them (int64), or read back as JSON (float64, json.Number).
		limit, used, draw := jsonInt(b["limit"]), jsonInt(b["used"]), jsonInt(b["draw"])
		if limit == 0 {
			return w.EachCallAsks
		}
		window, _ := b["window"].(string)
		nth := w.Ordinal(used + draw)
		if calls, ok := cardwords.Calls(limit, window, w); ok {
			if per, _ := b["per"].(string); per != "" {
				// "1 call in any 7 days for each invoice: this would be the 2nd for in_42"
				calls += " " + cardwords.EachItem(per, w)
				if v, _ := b["value"].(string); v != "" {
					return w.WouldBeFor(calls, nth, v)
				}
			}
			return w.WouldBe(calls, nth)
		}
		return w.OverCalls(limit, nth)
	}
	if b != nil {
		if r, _ := b["reason"].(string); r != "" {
			return r
		}
	}
	if e.Reason != "" {
		return e.Reason
	}
	if b == nil {
		return w.OutsideCard
	}
	switch b["outcome"] {
	case string(mandate.OutcomePerActionExceeded):
		return w.OverActionLimit
	case string(mandate.OutcomeLimitReached):
		return w.OverCardTotal
	case string(mandate.OutcomeResourceOutside):
		return w.NotCardResource
	}
	return w.OutsideCard
}

// heldDetails is what a held gateway call is on, one line an argument, in
// the order of their names: the amount isn't repeated, nor KIFF's own
// operation id.
func heldDetails(args json.RawMessage, params map[string]int64, units map[string]any) []string {
	out := []string{}
	var m map[string]json.RawMessage
	if len(args) == 0 || json.Unmarshal(args, &m) != nil {
		return out
	}
	// The amount is shown above, and the gateway's own arguments (the
	// operation's id, the taf's words) are said elsewhere.
	shown := map[string]bool{"kiff_operation_id": true, "kiff_reason": true, "kiff_about": true}
	for k := range params {
		shown[strings.ToLower(k)] = true
	}
	for k := range units {
		shown[strings.ToLower(k)] = true
	}
	for _, k := range argumentKeys(m) {
		if shown[strings.ToLower(k)] {
			continue
		}
		// An empty string at the top says nothing.
		var s string
		if strings.TrimSpace(string(m[k])) != "null" && json.Unmarshal(m[k], &s) == nil && s == "" {
			continue
		}
		out = append(out, argumentLines(visible(k), m[k], 0)...)
	}
	return out
}

// argumentLines is label's value as lines, as tafs'
// contracts/held-arguments.json writes them for every app: a plain value on
// its label's line; a list's items (1, 2, ...) and an object's fields (by
// key, at every level) below it, two spaces deeper each level. Nothing is
// shortened: a held browser call's steps are what the owner signs, and a
// step a line hid could click what the taf's words never said.
func argumentLines(label string, raw json.RawMessage, depth int) []string {
	pad := strings.Repeat("  ", depth)
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil && list != nil {
		if len(list) == 0 {
			return []string{pad + label + ": []"}
		}
		out := []string{pad + label + ":"}
		for i, item := range list {
			out = append(out, argumentLines(strconv.Itoa(i+1), item, depth+1)...)
		}
		return out
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil && obj != nil {
		if len(obj) == 0 {
			return []string{pad + label + ": {}"}
		}
		out := []string{pad + label + ":"}
		for _, k := range argumentKeys(obj) {
			out = append(out, argumentLines(visible(k), obj[k], depth+1)...)
		}
		return out
	}
	return []string{pad + label + ": " + visible(argumentText(raw))}
}

// argumentKeys is m's keys in byte order, which for UTF-8 is code point order.
func argumentKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// visible is a line from a taf as a card draws it: no direction mark
// (U+202A-U+202E, U+2066-U+2069, U+200E, U+200F, U+061C), so a payee's name
// can't reorder what's beside it, and any other control character, a line
// break too, a space, so a value can't draw a line of its own.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F || r == 0x061C:
		case unicode.IsControl(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// heldLineMax bounds a held call's summary, in runes. Its details are never
// cut: what the owner signs is every argument.
const heldLineMax = 200

// cut is s within max runes, ending in "…" when it was longer.
func cut(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// argumentText is one plain argument as text: a string as it is (an empty
// one as "", so a fill with nothing still shows), a number exactly as sent
// but written out plainly ("0.000015", never "1.5e-05"; an integer past 2^53
// too), a flag as true or false, a null as null.
func argumentText(raw json.RawMessage) string {
	t := strings.TrimSpace(string(raw))
	if t == "null" {
		return "null"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return `""`
		}
		return s
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return strconv.FormatBool(b)
	}
	return plainNumber(t)
}

// plainNumber is a JSON number written without an exponent, keeping its
// digits and scale as Java's BigDecimal.toPlainString does: "1.5e-05" is
// "0.000015", "1E+3" is "1000", "1.50" stays "1.50".
func plainNumber(n string) string {
	sign := ""
	if strings.HasPrefix(n, "-") {
		sign, n = "-", n[1:]
	}
	mant, exp := n, 0
	if i := strings.IndexAny(n, "eE"); i >= 0 {
		e, err := strconv.Atoi(strings.TrimPrefix(n[i+1:], "+"))
		if err != nil {
			return sign + n
		}
		mant, exp = n[:i], e
	}
	whole, frac, _ := strings.Cut(mant, ".")
	digits := strings.TrimLeft(whole+frac, "0")
	scale := len(frac) - exp
	if digits == "" {
		return "0"
	}
	if scale <= 0 {
		return sign + digits + strings.Repeat("0", -scale)
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	return sign + digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
}
