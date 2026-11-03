package permit

import (
	"regexp"
	"strconv"
	"strings"
)

var decimalString = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,18})?$`)

// MinorUnits converts a decimal string exactly to minor units at scale,
// as the gateway does: "80.5" at scale 2 is 8050. Signs, exponents, more
// decimals than scale and amounts past 2^53 - 1 are refused, never rounded.
func MinorUnits(s string, scale int) (int64, bool) {
	m := decimalString.FindStringSubmatch(s)
	if m == nil || scale < 0 || scale > 8 {
		return 0, false
	}
	frac := strings.TrimPrefix(m[2], ".")
	if len(strings.TrimRight(frac, "0")) > scale {
		return 0, false
	}
	if len(frac) > scale {
		frac = frac[:scale]
	}
	frac += strings.Repeat("0", scale-len(frac))
	n, err := strconv.ParseInt(m[1]+frac, 10, 64)
	if err != nil || n > 1<<53-1 {
		return 0, false
	}
	return n, true
}
