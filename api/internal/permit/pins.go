package permit

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Pin is one value a relay sets in the vendor's body after the permit:
// Value at Path, a vendor's dotted path, or, for a Marker, the permit's op.
type Pin struct {
	Path   string `json:"path"`
	Value  any    `json:"value,omitempty"`
	Marker bool   `json:"marker,omitempty"`
}

// The bounds on a call's pins, as the gateway keeps them.
const (
	maxPins      = 8
	maxPinDepth  = 4
	maxPinPath   = 128
	maxPinString = 64
)

var errPins = errors.New("permit: the pins aren't ones a permit signs")

// canonicalPins checks pins and returns them as the binding keeps them:
// sorted by path, as JSON; "" for none. A path is given once; a value is a
// string or a boolean; a marker has none.
func canonicalPins(pins []Pin) (string, error) {
	if len(pins) == 0 {
		return "", nil
	}
	if len(pins) > maxPins {
		return "", errPins
	}
	out := make([]Pin, 0, len(pins))
	seen := map[string]bool{}
	for _, p := range pins {
		if !pinPath(p.Path) || seen[p.Path] {
			return "", errPins
		}
		seen[p.Path] = true
		switch v := p.Value.(type) {
		case nil:
			if !p.Marker {
				return "", errPins
			}
		case string:
			if p.Marker || len(v) > maxPinString {
				return "", errPins
			}
		case bool:
			if p.Marker {
				return "", errPins
			}
		default:
			return "", errPins
		}
		out = append(out, Pin{Path: p.Path, Value: p.Value, Marker: p.Marker})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	b, err := json.Marshal(out)
	if err != nil {
		return "", errPins
	}
	return string(b), nil
}

// signedPins is the permit's pins claim from a binding's: each path's
// value, a marker's being the op. Nil for none.
func signedPins(stored, op string) map[string]any {
	if stored == "" {
		return nil
	}
	var pins []Pin
	if json.Unmarshal([]byte(stored), &pins) != nil || len(pins) == 0 {
		return nil
	}
	out := make(map[string]any, len(pins))
	for _, p := range pins {
		if p.Marker {
			out[p.Path] = op
		} else {
			out[p.Path] = p.Value
		}
	}
	return out
}

func pinPath(path string) bool {
	if path == "" || len(path) > maxPinPath {
		return false
	}
	parts := strings.Split(path, ".")
	if len(parts) > maxPinDepth {
		return false
	}
	for _, part := range parts {
		// Object keys only, never an array index: [a-z0-9_]{1,64}.
		if part == "" || len(part) > 64 || strings.Trim(part, "0123456789") == "" {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
				return false
			}
		}
	}
	return true
}
