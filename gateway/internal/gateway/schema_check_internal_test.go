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
	browser := Tool{InputSchema: json.RawMessage(browseActSchema)}
	for _, ok := range []string{
		`{"url":"https://shop.example/","read":"r1","steps":[{"fill":"f1","value":"Test","label":"Customer name"}]}`,
		`{"url":"https://shop.example/","steps":[{"click":"c1","label":"Submit order","submits":true},{"back":true}]}`,
		`{"url":"https://shop.example/","steps":[{"press":"f1","key":"Enter","label":"Search","submits":true}]}`,
		// the top takes other keys
		`{"url":"https://shop.example/","steps":[{"select":"s1","option":"Large","label":"Size"}],"extra":"kept"}`,
	} {
		if why := toolSchemaRefusal(browser, argsOf(t, ok)); why != "" {
			t.Errorf("%s: %s", ok, why)
		}
	}
	for _, c := range []struct{ raw, want string }{
		{`{"url":"https://shop.example/","steps":[{"fill":"f1","value":"Test","option":"Customer name:"}]}`, "arguments.steps.1 is none of the forms"},
		{`{"url":"https://shop.example/","steps":[{"fill":"f1","value":"Test"}]}`, "arguments.steps.1 is none of the forms"},
		{`{"url":"https://shop.example/","steps":[{"click":"c1","label":"Go","select":"s1"}]}`, "arguments.steps.1 is none of the forms"},
		{`{"url":"https://shop.example/","steps":[{"press":"f1","key":"Tab","label":"Search"}]}`, "arguments.steps.1 is none of the forms"},
		{`{"url":"https://shop.example/","steps":[{"back":false}]}`, "arguments.steps.1 is none of the forms"},
		{`{"steps":[]}`, "arguments is missing url"},
		{`{"url":"https://shop.example/","steps":"click it"}`, "arguments.steps isn't an array"},
		{`{"url":42,"steps":[]}`, "arguments.url isn't a string"},
		{`{"url":"https://shop.example/","steps":[` + strings.Repeat(`{"back":true},`, 20) + `{"back":true}]}`, "arguments.steps has 21 items, more than 20"},
	} {
		why := toolSchemaRefusal(browser, argsOf(t, c.raw))
		if !strings.Contains(why, c.want) || !strings.HasPrefix(why, "the arguments don't fit the tool's schema: ") {
			t.Errorf("%.80s: %q, want %q", c.raw, why, c.want)
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
		// a oneOf of a non-object
		`{"type":"object","properties":{"x":{"oneOf":[{"type":"string"}]}},"required":["url"]}`,
		`not json`,
		``,
	} {
		if why := toolSchemaRefusal(Tool{InputSchema: json.RawMessage(s)}, bad); why != "" {
			t.Errorf("%s: checked (%q) though the check can't read it", s, why)
		}
	}
	// The same arguments against a schema it reads: refused.
	if why := toolSchemaRefusal(Tool{InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"https"}}}`)}, bad); why == "" {
		t.Error("a schema the check reads didn't refuse a number for a string")
	}
}

// Numbers read as the gateway reads them (json.Number), by value as JSON
// Schema reads them: 3.0 is an integer, 2.5 isn't; bounds and enums compare
// by value.
func TestASchemasNumbersAreReadAsSent(t *testing.T) {
	s := Tool{InputSchema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","minimum":1,"maximum":5},"k":{"type":"number","enum":[1.5,2]}}}`)}
	for raw, refused := range map[string]bool{
		`{"n":3}`: false, `{"n":3.0}`: false, `{"n":30e-1}`: false, `{"n":2.5}`: true, `{"n":1e1}`: true, `{"n":0}`: true, `{"n":6}`: true,
		`{"k":1.5}`: false, `{"k":2.0}`: false, `{"k":3}`: true, `{"k":"2"}`: true,
	} {
		if why := toolSchemaRefusal(s, argsOf(t, raw)); (why != "") != refused {
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

// A whole number is told from its digits, never by arithmetic on its
// exponent: 1e999999999 is answered at once.
func TestIntegralReadsDigitsOnly(t *testing.T) {
	for n, want := range map[string]bool{
		"0": true, "-0.0": true, "7": true, "7.0": true, "7.10": false, "1e2": true, "1.5e1": true, "1.25e1": false,
		"100e-2": true, "101e-2": false, "1e999999999": true, "1e-999999999": false, "0e-999999999999999999999": true,
		"2e99999999999999999999": true, "2e-99999999999999999999": false,
	} {
		if got := integral(n); got != want {
			t.Errorf("integral(%s) = %v", n, got)
		}
	}
}

// The schema checked is the one the agent is served: the idempotency argument
// and the connection's fixed arguments are KIFF's to set, so a fixed required
// argument is never asked of the call, and the gateway's own request
// argument isn't the tool's.
func TestTheSchemaCheckedIsTheOneTheAgentIsServed(t *testing.T) {
	tool := Tool{
		InputSchema:    json.RawMessage(`{"type":"object","required":["url","steps","op_key"],"additionalProperties":false,"properties":{"url":{"type":"string"},"op_key":{"type":"string"},"steps":{"type":"array"}}}`),
		IdempotencyArg: "op_key",
		FixedArgs:      json.RawMessage(`{"url":"https://shop.example/"}`),
	}
	if why := toolSchemaRefusal(tool, argsOf(t, `{"steps":[]}`)); why != "" {
		t.Fatalf("the agent followed the schema it was served: %s", why)
	}
	if why := toolSchemaRefusal(tool, argsOf(t, `{"steps":[],"`+RequestArg+`":{"x":1}}`)); why != "" {
		t.Fatalf("the gateway's own request argument: %s", why)
	}
	if why := toolSchemaRefusal(tool, argsOf(t, `{}`)); !strings.Contains(why, "arguments is missing steps") {
		t.Fatalf("still checked: %q", why)
	}
}

// A value that fits two forms is told so, not "none".
func TestAValueThatFitsTwoFormsIsToldSo(t *testing.T) {
	tool := Tool{InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"object","oneOf":[` +
		`{"type":"object","properties":{"a":{"type":"string"}}},{"type":"object","properties":{"b":{"type":"string"}}}]}}}`)}
	if why := toolSchemaRefusal(tool, argsOf(t, `{"x":{}}`)); !strings.Contains(why, "arguments.x fits more than one of the forms") {
		t.Fatalf("%q", why)
	}
}
