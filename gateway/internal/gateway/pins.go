package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// VendorPin is a value a relay sets in the vendor's own body after the
// permit (Qonto's status: draft, Stripe's account, a refund's marker), as
// it says at registration. KIFF signs them into the permit, for a tenant
// on KIFF_SIGN_PINS, so the relay can check what it sends against them.
type VendorPin = kiffapi.Pin

// The bounds on a tool's pins.
const (
	MaxVendorPins   = 8
	MaxPinDepth     = 4
	MaxPinPath      = 128
	MaxPinValueSize = 64
)

// VendorPinsRefusal says why pins can't be kept, or "": each a dotted
// vendor path of object keys ([a-z0-9_]{1,64}, no array index), no deeper than MaxPinDepth and given once,
// with a string or boolean value, or a marker with no value.
func VendorPinsRefusal(pins []VendorPin) string {
	if len(pins) > MaxVendorPins {
		return fmt.Sprintf("vendor_pins: at most %d", MaxVendorPins)
	}
	seen := map[string]bool{}
	for _, p := range pins {
		if !pinPath(p.Path) {
			return fmt.Sprintf("vendor_pins: %q isn't a dotted path of names, at most %d deep", p.Path, MaxPinDepth)
		}
		if seen[p.Path] {
			return fmt.Sprintf("vendor_pins: %s is given twice", p.Path)
		}
		seen[p.Path] = true
		if p.Marker {
			if p.Value != nil {
				return fmt.Sprintf("vendor_pins: %s is a marker, which takes no value", p.Path)
			}
			continue
		}
		switch v := p.Value.(type) {
		case string:
			if len(v) > MaxPinValueSize {
				return fmt.Sprintf("vendor_pins: %s's value is longer than %d", p.Path, MaxPinValueSize)
			}
		case bool:
		default:
			// A number is never pinned: a permit signs only what it can
			// compare exactly, and no pin today is one.
			return fmt.Sprintf("vendor_pins: %s's value must be a string or a boolean", p.Path)
		}
	}
	return ""
}

// DecodeVendorPins reads pins as stored, numbers kept exact; none for
// NULL or an empty list.
func DecodeVendorPins(b []byte) ([]VendorPin, error) {
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var pins []VendorPin
	if err := dec.Decode(&pins); err != nil {
		return nil, err
	}
	if len(pins) == 0 {
		return nil, nil
	}
	return pins, nil
}

func pinPath(path string) bool {
	if path == "" || len(path) > MaxPinPath {
		return false
	}
	parts := strings.Split(path, ".")
	if len(parts) > MaxPinDepth {
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

// tools is the tenant's tools from the catalog, each marked PinsSigned
// when the tenant is on SignPins: its permits carry its vendor pins.
func (g *Gateway) tools(ctx context.Context, tenantID string) ([]Tool, error) {
	tools, err := g.Catalog.Tools(ctx, tenantID)
	if err != nil || !g.SignPins[tenantID] {
		return tools, err
	}
	for i := range tools {
		tools[i].PinsSigned = len(tools[i].VendorPins) > 0
	}
	return tools, nil
}

// signedPin is the value the permit signs at path, when the tool's pins
// are signed and one is a value there; ok false otherwise.
func (t Tool) signedPin(path string) (any, bool) {
	if !t.PinsSigned {
		return nil, false
	}
	for _, p := range t.VendorPins {
		if p.Path == path && !p.Marker {
			return p.Value, true
		}
	}
	return nil, false
}
