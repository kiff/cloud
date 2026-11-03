package gateway

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Amount formats a profile can give (connection profiles: amounts). FormatMinor, or none,
// is a whole number in the unit's minor units; FormatDecimal is a
// decimal string, converted exactly at the tool's scale.
const (
	FormatMinor   = "integer_minor"
	FormatDecimal = "decimal_string"
)

var decimalString = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,18})?$`)

// errDecimal and errTooPrecise say why a decimal amount was refused.
var (
	errDecimal    = errors.New("must be a decimal number written as text, such as \"80.00\"")
	errTooPrecise = errors.New("has more decimals than its unit takes")
)

// MinorUnits converts a decimal string exactly to minor units at scale:
// "80.5" at scale 2 is 8050. It refuses signs, exponents, spaces, more
// decimals than scale, and anything past 2^53 - 1 minor units, so the
// amount KIFF counts is never rounded.
func MinorUnits(s string, scale int) (int64, error) {
	m := decimalString.FindStringSubmatch(s)
	if m == nil || scale < 0 || scale > 8 {
		return 0, errDecimal
	}
	frac := strings.TrimPrefix(m[2], ".")
	if len(strings.TrimRight(frac, "0")) > scale {
		return 0, errTooPrecise
	}
	if len(frac) > scale {
		frac = frac[:scale]
	}
	frac += strings.Repeat("0", scale-len(frac))
	n, err := strconv.ParseInt(m[1]+frac, 10, 64)
	if err != nil || n > 1<<53-1 {
		return 0, errDecimal
	}
	return n, nil
}
