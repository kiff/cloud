package permit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// maxSafeInteger is 2^53 - 1, the largest integer every JSON number maps
// to exactly.
var maxSafeInteger = big.NewInt(1<<53 - 1)

// ArgsSHA256 is the hex SHA-256 of the arguments canonicalized with RFC
// 8785 (JCS): the value a permit carries as args_sha256 and a verifier
// recomputes from the arguments it receives.
func ArgsSHA256(raw json.RawMessage) (string, error) {
	c, err := Canonicalize(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// Canonicalize returns raw in RFC 8785 form: object members sorted by
// their UTF-16 code units, no insignificant whitespace, strings escaped
// as ECMAScript's JSON.stringify does, and numbers as IEEE 754 doubles in
// ECMAScript's shortest form.
func Canonicalize(raw json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("jcs: %w", err)
	}
	if dec.More() {
		return nil, errors.New("jcs: trailing data after the JSON value")
	}
	var b bytes.Buffer
	if err := encode(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func encode(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		// Integers beyond 2^53 - 1 alias as doubles, so two different ids
		// could hash the same while the tool receives either. Refused, as
		// the Python verifier refuses them.
		if i, ok := new(big.Int).SetString(string(x), 10); ok && new(big.Int).Abs(i).Cmp(maxSafeInteger) > 0 {
			return fmt.Errorf("jcs: integer %s is beyond 2^53 - 1 and cannot be hashed without ambiguity", x)
		}
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			return fmt.Errorf("jcs: number %s: %w", x, err)
		}
		s, err := number(f)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case string:
		if !utf8.ValidString(x) {
			return errors.New("jcs: string is not valid UTF-8")
		}
		str(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := encode(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			str(b, k)
			b.WriteByte(':')
			if err := encode(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("jcs: unsupported value %T", v)
	}
	return nil
}

func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// str writes s as JSON.stringify does: only the quote, the backslash and
// control characters are escaped, with the short forms where they exist.
func str(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// number formats f as ECMAScript's Number.prototype.toString does, which
// is what RFC 8785 §3.2.2.3 requires.
func number(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.New("jcs: NaN and Infinity are not JSON numbers")
	}
	if f == 0 {
		return "0", nil
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// Shortest round-trip digits and the decimal exponent: d.ddde±x.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	k, n := len(digits), x+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k), nil
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:], nil
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits, nil
	}
	es := "+"
	if n-1 < 0 {
		es = "-"
	}
	m := digits[:1]
	if k > 1 {
		m += "." + digits[1:]
	}
	return sign + m + "e" + es + strconv.Itoa(abs(n-1)), nil
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
