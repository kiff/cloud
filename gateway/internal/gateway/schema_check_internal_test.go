package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// browseActSchema is planque's browse_act schema with its steps as closed
// kinds, each naming its ref by label (kiff/planque, browseActStep), trimmed
// to the keys these tests use.
const browseActSchema = `{"type":"object","required":["url","steps"],"properties":{"url":{"type":"string","description":"https"},"read":{"type":"string"},` +
	`"steps":{"type":"array","maxItems":20,"items":{"type":"object","oneOf":[` +
	`{"type":"object","required":["fill","label"],"additionalProperties":false,"properties":{"fill":{"type":"string"},"value":{"type":"string"},"label":{"type":"string"},"submits":{"type":"boolean"},"keys":{"type":"boolean"},"sealed":{"type":"string"}}},` +
	`{"type":"object","required":["select","option","label"],"additionalProperties":false,"properties":{"select":{"type":"string"},"option":{"type":"string"},"label":{"type":"string"}}},` +
	`{"type":"object","required":["click","label"],"additionalProperties":false,"properties":{"click":{"type":"string"},"label":{"type":"string"},"submits":{"type":"boolean"},"to":{"type":"string"}}},` +
	`{"type":"object","required":["press","key","label"],"additionalProperties":false,"properties":{"press":{"type":"string"},"key":{"type":"string","enum":["Enter","Escape","ArrowDown","ArrowUp"]},"label":{"type":"string"},"submits":{"type":"boolean"}}},` +
	`{"type":"object","required":["back"],"additionalProperties":false,"properties":{"back":{"type":"boolean","enum":[true]}}}]}}}}`

func argsOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A browser call that doesn't fit its schema is refused before anything is
// decided or held, with words that say where; one that fits goes on. The held
// call a red-team found, a fill with the field's words in option and no
// label, is refused here now, before the owner is asked about it.
func TestABrowserCallIsCheckedAgainstItsSchema(t *testing.T) {
	schema := json.RawMessage(browseActSchema)
	for _, ok := range []string{
		`{"url":"https://shop.example/","read":"r1","steps":[{"fill":"f1","value":"Test","label":"Customer name"}]}`,
		`{"url":"https://shop.example/","steps":[{"click":"c1","label":"Submit order","submits":true},{"back":true}]}`,
		`{"url":"https://shop.example/","steps":[{"press":"f1","key":"Enter","label":"Search","submits":true}]}`,
		`{"url":"https://shop.example/","steps":[{"select":"s1","option":"Large","label":"Size"}],"extra":"kept"}`, // the top takes other keys
	} {
		if why := schemaRefusal(schema, argsOf(t, ok)); why != "" {
			t.Errorf("%s: %s", ok, why)
		}
	}
	for raw, want := range map[string]string{
		`{"url":"https://shop.example/","steps":[{"fill":"f1","value":"Test","option":"Customer name:"}]}`: "arguments.steps.1 is none of the forms",
		`{"url":"https://shop.example/","steps":[{"fill":"f1","value":"Test"}]}`:                           "arguments.steps.1 is none of the forms",
		`{"url":"https://shop.example/","steps":[{"click":"c1","label":"Go","select":"s1"}]}`:              "arguments.steps.1 is none of the forms",
		`{"url":"https://shop.example/","steps":[{"press":"f1","key":"Tab","label":"Search"}]}`:            "arguments.steps.1 is none of the forms",
		`{"url":"https://shop.example/","steps":[{"back":false}]}`:                                         "arguments.steps.1 is none of the forms",
		`{"steps":[]}`: "arguments is missing url",
		`{"url":"https://shop.example/","steps":"click it"}`:        "arguments.steps isn't an array",
		`{"url":42,"steps":[]}`:                                     "arguments.url isn't a string",
		`{"url":"https://shop.example/","steps":[` + strings.Repeat(`{"back":true},`, 20) + `{"back":true}]}`: "arguments.steps has 21 items, more than 20",
	} {
		why := schemaRefusal(schema, argsOf(t, raw))
		if !strings.Contains(why, want) || !strings.HasPrefix(why, "the arguments don't fit the tool's schema: ") {
			t.Errorf("%.80s: %q, want %q", raw, why, want)
		}
	}
}

// The check refuses only what it can read for certain: a schema with a
// keyword outside its subset, anywhere, isn't checked at all, so a tool whose
// schema says more than the check knows is never refused on a guess.
func TestASchemaTheCheckCantReadIsNotChecked(t *testing.T) {
	bad := argsOf(t, `{"url":7}`) // fails any schema below that is checked
	for _, s := range []string{
		`{"type":"object","properties":{"url":{"type":"string","pattern":"^https://"}}}`,
		`{"type":"object","properties":{"url":{"anyOf":[{"type":"string"}]}}}`,
		`{"type":"object","additionalProperties":{"type":"string"},"properties":{"url":{"type":"string"}}}`,
		`{"type":"object","properties":{"url":{"$ref":"#/defs/u"}}}`,
		`{"type":"object","properties":{"url":{"type":["string","null"]}}}`,
		`{"type":"object","properties":{"x":{"oneOf":[{"type":"string"}]}},"required":["url"]}`, // oneOf of a non-object
		`not json`,
		``,
	} {
		if why := schemaRefusal(json.RawMessage(s), bad); why != "" {
			t.Errorf("%s: checked (%q) though the check can't read it", s, why)
		}
	}
	// The same arguments against a schema it reads: refused.
	if why := schemaRefusal(json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"https"}}}`), bad); why == "" {
		t.Error("a schema the check reads didn't refuse a number for a string")
	}
}

// Numbers read as the gateway reads them (json.Number): an integer is one
// with no fraction or exponent, and bounds and enums compare by value.
func TestASchemasNumbersAreReadAsSent(t *testing.T) {
	s := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","minimum":1,"maximum":5},"k":{"type":"number","enum":[1.5,2]}}}`)
	for raw, refused := range map[string]bool{
		`{"n":3}`: false, `{"n":3.0}`: true, `{"n":1e1}`: true, `{"n":0}`: true, `{"n":6}`: true,
		`{"k":1.5}`: false, `{"k":2.0}`: false, `{"k":3}`: true, `{"k":"2"}`: true,
	} {
		if why := schemaRefusal(s, argsOf(t, raw)); (why != "") != refused {
			t.Errorf("%s: %q, want refused %v", raw, why, refused)
		}
	}
}

// Only planque's browser tools are checked, for the caller's own grant:
// every other tool goes on as before until a day of logs says otherwise.
func TestOnlyAPlanqueBrowserToolIsChecked(t *testing.T) {
	relays := []string{"https://planque.example/"}
	tool := func(upstream, url string, mode string) Tool {
		return Tool{UpstreamTool: upstream, UpstreamURL: url, Audience: url, Mode: mode}
	}
	own := "https://planque.example/mcp/t1/grt_1"
	for _, c := range []struct {
		name string
		t    Tool
		want bool
	}{
		{"a browser tool on the caller's planque grant", tool("browse_act", own, ModeRelay), true},
		{"another browser tool there", tool("browse_open", own, ModeRelay), true},
		{"a planque tool that isn't the browser's", tool("create_refund", own, ModeRelay), false},
		{"a browser-named tool off planque", tool("browse_act", "https://relay.customer.example/mcp/t1/grt_1", ModeRelay), false},
		{"a browser tool on another account's grant", tool("browse_act", "https://planque.example/mcp/t2/grt_1", ModeRelay), false},
		{"a sealed tool", tool("browse_act", own, ModeSealed), false},
	} {
		if got := schemaChecked(c.t, relays, "t1"); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}
