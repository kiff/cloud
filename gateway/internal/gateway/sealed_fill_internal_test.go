package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// Invariant: a call whose arguments name a sealed handle tells KIFF kiff_owner_ask sealed_fill, whatever the tool's own code, so every sealed fill waits for the owner and no Card's room lets one through without the phone's release.
// Category: authority
// Attack: fill the owner's saved password into a page through a browser tool the Card allows, with no one asked
// Refused: the call's parameters carry kiff_owner_ask sealed_fill, even on a tool whose own code is each_call or none; a call with no sealed handle asks as before
func TestASealedFillAsksItsOwnerWhateverTheTool(t *testing.T) {
	args := func(s string) map[string]any {
		var m map[string]any
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		if err := d.Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	for name, tool := range map[string]Tool{
		"plain":    {Name: "browse_act"},
		"each":     {Name: "browse_act", AskEachCall: true},
		"readonly": {Name: "browse_act", ReadOnly: true},
	} {
		p, err := tool.params(args(`{"steps":[{"fill":"f3","sealed":"sec_abc:password"}],"read":"r1"}`))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p[OwnerAskParam] != OwnerAskSealedFill {
			t.Fatalf("%s: a sealed fill told KIFF %v", name, p[OwnerAskParam])
		}
	}

	// No sealed handle: the tool's own code, or none.
	if p, _ := (Tool{Name: "browse_act", AskEachCall: true}).params(args(`{"steps":[{"fill":"f3","value":"hi"}]}`)); p[OwnerAskParam] != OwnerAskEachCall {
		t.Fatalf("a plain fill on an each_call tool told KIFF %v", p[OwnerAskParam])
	}
	if p, _ := (Tool{Name: "browse_act"}).params(args(`{"steps":[{"fill":"f3","value":"sec_abc:password"}]}`)); p[OwnerAskParam] != nil {
		t.Fatalf("a handle's shape under another key asked %v", p[OwnerAskParam])
	}
}

// namesSealed is apps/api's: a "sealed" key with a sec_ value and a part,
// at any depth, in a map or a list; nothing else.
func TestNamesSealedFindsAHandleAtAnyDepthAndNothingElse(t *testing.T) {
	for in, want := range map[string]bool{
		`{"sealed":"sec_a:password"}`:                     true,
		`{"steps":[{"x":1},{"sealed":"sec_a:username"}]}`: true,
		`{"a":{"b":[{"c":{"sealed":"sec_z:password"}}]}}`: true,
		`{"sealed":"sec_a"}`:                              false, // no part
		`{"sealed":"abc:password"}`:                       false,
		`{"sealed":7}`:                                    false,
		`{"value":"sec_a:password"}`:                      false,
		`{"steps":[]}`:                                    false,
	} {
		var v any
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			t.Fatal(err)
		}
		if got := namesSealed(v); got != want {
			t.Fatalf("namesSealed(%s) = %v", in, got)
		}
	}
}
