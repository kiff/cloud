package permit

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// A relay's pins are signed beside the arguments: each value at its vendor
// path, a marker's as the permit's op. A retry with other pins is another
// call, as one with other arguments is.
func TestThePermitSignsTheRelaysPins(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	is := &Issuer{Key: testKey(t), Store: NewMemoryStore(), Iss: "https://api.kiff.dev", Now: func() time.Time { return now }}
	ctx := context.Background()
	target := Target{Action: "create_refund", Tool: "create_refund", Audience: "https://relay.example.com/mcp"}
	req := Request{Arguments: json.RawMessage(`{"charge":"ch_1"}`), Pins: []Pin{
		{Path: "stripe_context", Value: "acct_1"},
		{Path: "metadata.kiff_op", Marker: true},
		{Path: "draft", Value: true},
	}}
	b, err := req.BindingFor(target)
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := is.Bind(ctx, "t1", "op_1", b)
	if err != nil || !stored.Same(b) {
		t.Fatalf("bind: %+v %v", stored, err)
	}
	tok, _, err := is.Mint(ctx, "t1", "agent_1", "op_1", "card_1", "", "", "", stored)
	if err != nil {
		t.Fatal(err)
	}
	_, c := decodeToken(t, tok, is.Key.Public().Pub)
	pins := c.Details[0].Pins
	if len(pins) != 3 || pins["stripe_context"] != "acct_1" || pins["metadata.kiff_op"] != "op_1" || pins["draft"] != true {
		t.Fatalf("pins: %v", pins)
	}

	// The same pins in another order are the same call; other pins aren't.
	again := req
	again.Pins = []Pin{req.Pins[2], req.Pins[0], req.Pins[1]}
	if b2, _ := again.BindingFor(target); !b2.Same(b) {
		t.Fatal("the pins' order changed the call")
	}
	other := req
	other.Pins = []Pin{{Path: "stripe_context", Value: "acct_2"}}
	if b3, _ := other.BindingFor(target); b3.Same(b) {
		t.Fatal("other pins read as the same call")
	}

	// No pins: no claim.
	plain, _ := Request{Arguments: json.RawMessage(`{"charge":"ch_1"}`)}.BindingFor(target)
	if _, _, err := is.Bind(ctx, "t1", "op_2", plain); err != nil {
		t.Fatal(err)
	}
	tok, _, _ = is.Mint(ctx, "t1", "agent_1", "op_2", "card_1", "", "", "", plain)
	if _, c := decodeToken(t, tok, is.Key.Public().Pub); c.Details[0].Pins != nil {
		t.Fatalf("pins without any: %v", c.Details[0].Pins)
	}
}

func TestOnlyPinsAPermitCanSignAreTaken(t *testing.T) {
	target := Target{Action: "x", Tool: "x", Audience: "a"}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	for name, pins := range map[string][]Pin{
		"a number":            {{Path: "amount", Value: 5.0}},
		"an object":           {{Path: "metadata", Value: map[string]any{"a": "b"}}},
		"no value":            {{Path: "status"}},
		"a marker with value": {{Path: "m", Marker: true, Value: "x"}},
		"twice":               {{Path: "status", Value: "draft"}, {Path: "status", Value: "draft"}},
		"an index":            {{Path: "lines.0.status", Value: "draft"}},
		"upper case":          {{Path: "Status", Value: "draft"}},
		"too deep":            {{Path: "a.b.c.d.e", Value: "x"}},
		"a long key":          {{Path: string(long), Value: "x"}},
		"a long value":        {{Path: "status", Value: string(long)}},
	} {
		if _, err := (Request{Arguments: json.RawMessage(`{}`), Pins: pins}).BindingFor(target); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
}
