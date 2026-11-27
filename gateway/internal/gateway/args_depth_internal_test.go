package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// A call's arguments nest at most ArgsDepthMax levels (the arguments object
// is level 1). Deeper, the call is refused before it's decided or held, with
// words the taf can act on: a desktop reader that stops at 64 or 128 levels
// would otherwise fail the owner's whole Needs-you list on one held call.
func TestArgumentsNestedTooDeepAreRefusedBeforeAnythingIsHeld(t *testing.T) {
	nested := func(levels int) json.RawMessage {
		// levels objects, the outermost being the arguments themselves.
		return json.RawMessage(strings.Repeat(`{"a":`, levels-1) + `{"a":1}` + strings.Repeat("}", levels-1))
	}
	if _, _, _, err := splitArgs(nested(ArgsDepthMax)); err != nil {
		t.Fatalf("%d levels: %v", ArgsDepthMax, err)
	}
	for _, raw := range []json.RawMessage{
		nested(ArgsDepthMax + 1),
		nested(10000),
		// Lists count as levels too, and a list that never closes is still refused once it's too deep.
		json.RawMessage(`{"a":` + strings.Repeat("[", ArgsDepthMax) + `1` + strings.Repeat("]", ArgsDepthMax) + `}`),
		json.RawMessage(`{"a":` + strings.Repeat("[", 100000)),
	} {
		_, _, _, err := splitArgs(raw)
		if err == nil || !strings.Contains(err.Error(), "deeper than 32 levels") {
			t.Fatalf("%.40s…: %v", raw, err)
		}
	}
	// A list at the edge: the arguments object, then 31 lists.
	if _, _, _, err := splitArgs(json.RawMessage(`{"a":` + strings.Repeat("[", ArgsDepthMax-1) + `1` + strings.Repeat("]", ArgsDepthMax-1) + `}`)); err != nil {
		t.Fatalf("31 lists: %v", err)
	}
	// What isn't an object keeps its own words.
	if _, _, _, err := splitArgs(json.RawMessage(`[1]`)); err == nil || err.Error() != "arguments must be a JSON object" {
		t.Fatalf("a list: %v", err)
	}
}
