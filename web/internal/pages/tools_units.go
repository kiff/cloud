package pages

import (
	"strconv"
	"strings"
)

// AmountUnitOption is one answer to "what is this amount?" when a tool is
// connected. The value is "<unit>:<scale>" as the gateway stores it
// (amount_unit, amount_scale); "" is a plain number. Display only: a Card
// still limits and sums the whole numbers the tool receives.
type AmountUnitOption struct{ Value, Label string }

var amountUnitOptions = []AmountUnitOption{
	{"", "A plain number"},
	{"EUR:0", "Whole euros (EUR)"},
	{"EUR:2", "Euro cents (EUR, 2 decimals)"},
	{"USD:0", "Whole US dollars (USD)"},
	{"USD:2", "US cents (USD, 2 decimals)"},
	{"GBP:0", "Whole pounds (GBP)"},
	{"GBP:2", "Pence (GBP, 2 decimals)"},
}

// ParseAmountUnit reads a posted amount unit choice. ok is false for a
// value that is not one of the options.
func ParseAmountUnit(v string) (unit string, scale *int, ok bool) {
	if v == "" {
		return "", nil, true
	}
	for _, o := range amountUnitOptions {
		if o.Value == v {
			u, s, _ := strings.Cut(v, ":")
			n, _ := strconv.Atoi(s)
			return u, &n, true
		}
	}
	return "", nil, false
}

// suggestAmountUnit pre-selects the unit an argument's name suggests
// ("amount_eur" → whole euros, "amount_cents" with no currency → none).
// It is only a default: the owner reads and confirms it.
func suggestAmountUnit(arg string) string {
	a := strings.ToLower(arg)
	cents := strings.Contains(a, "cent") || strings.Contains(a, "pence") || strings.Contains(a, "minor")
	cur := ""
	switch {
	case strings.Contains(a, "eur"):
		cur = "EUR"
	case strings.Contains(a, "usd") || strings.Contains(a, "dollar"):
		cur = "USD"
	case strings.Contains(a, "gbp") || strings.Contains(a, "pound") || strings.Contains(a, "pence"):
		cur = "GBP"
	}
	if cur == "" {
		return ""
	}
	if cents {
		return cur + ":2"
	}
	return cur + ":0"
}

// amountUnitLabel is how a stored unit reads beside a number: "EUR",
// "EUR cents", or "" for a plain number.
func amountUnitLabel(unit string, scale int) string {
	switch {
	case unit == "":
		return ""
	case scale == 0:
		return unit
	case scale == 2 && unit == "GBP":
		return "GBP pence"
	case scale == 2:
		return unit + " cents"
	}
	return unit + " (" + strconv.Itoa(scale) + " decimals)"
}

// discoveredUnitDefault is the unit pre-selected for a discovered tool:
// the owner's earlier choice when the save was refused, else what its
// first amount-like argument suggests.
func discoveredUnitDefault(v ToolsNewView, name string, intArgs []string) string {
	if v.Chosen == name {
		return v.AmountUnit
	}
	for _, a := range intArgs {
		if strings.Contains(strings.ToLower(a), "amount") {
			return suggestAmountUnit(a)
		}
	}
	if len(intArgs) > 0 {
		return suggestAmountUnit(intArgs[0])
	}
	return ""
}
