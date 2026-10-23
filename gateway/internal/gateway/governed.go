package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A generic vendor tool takes the operation as an argument and the amount
// inside a nested object: Stripe's stripe_api_write takes
// stripe_api_operation_id and parameters.amount. A connection can
// pin the operation with fixed arguments and read the amount and resource
// at a path, so one operation of such a tool is its own governed tool.

// MaxPathSegments bounds how deep a governed argument can sit.
const MaxPathSegments = 4

// MaxFixedArgs bounds how many arguments a connection can fix.
const MaxFixedArgs = 16

// PathSegments splits a dotted argument path ("parameters.amount") and
// reports whether every segment is a valid argument name.
func PathSegments(path string) ([]string, bool) {
	segs := strings.Split(path, ".")
	if len(segs) == 0 || len(segs) > MaxPathSegments {
		return nil, false
	}
	for _, s := range segs {
		if !ValidToolName(s) {
			return nil, false
		}
	}
	return segs, true
}

// lookupPath reads the value at a dotted path in the call's arguments.
func lookupPath(args map[string]any, path string) (any, bool) {
	segs, ok := PathSegments(path)
	if !ok {
		return nil, false
	}
	var cur any = args
	for _, s := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[s]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// amountPath and resourcePath are where the call carries the governed
// arguments: the path when the connection names one, else the argument.
func (t Tool) amountPath() string {
	if t.AmountPath != "" {
		return t.AmountPath
	}
	return t.AmountArg
}

func (t Tool) resourcePath() string {
	if t.ResourcePath != "" {
		return t.ResourcePath
	}
	return t.ResourceArg
}

// fixedArgs decodes the connection's fixed arguments. Numbers stay
// json.Number, as in the agent's arguments.
func (t Tool) fixedArgs() (map[string]any, error) {
	if len(t.FixedArgs) == 0 || string(t.FixedArgs) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(t.FixedArgs)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, errors.New("the tool's fixed arguments are not a JSON object")
	}
	return m, nil
}

// withFixed refuses a call that sets an argument the connection fixes,
// then adds the fixed arguments. The result is what is decided, recorded
// and forwarded, so the permit binds the fixed arguments too.
func (t Tool) withFixed(args map[string]any) (map[string]any, error) {
	fixed, err := t.fixedArgs()
	if err != nil {
		return nil, err
	}
	for _, k := range sortedKeys(fixed) {
		if _, set := args[k]; set {
			return nil, invalid(k + " is set by KIFF for this tool; leave it out")
		}
	}
	for k, v := range fixed {
		args[k] = v
	}
	return args, nil
}

// fixedNote tells the agent which arguments KIFF sends for it.
func (t Tool) fixedNote() string {
	fixed, err := t.fixedArgs()
	if err != nil || len(fixed) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fixed))
	for _, k := range sortedKeys(fixed) {
		v, _ := json.Marshal(fixed[k])
		parts = append(parts, fmt.Sprintf("%s = %s", k, v))
	}
	return "KIFF always sends " + strings.Join(parts, ", ") + " for this tool; don't pass them."
}

// dropArg removes an argument from a tool's input schema: from its
// properties and from required.
func dropArg(schema map[string]any, props map[string]any, name string) {
	delete(props, name)
	if req, ok := schema["required"].([]any); ok {
		kept := req[:0]
		for _, r := range req {
			if r != name {
				kept = append(kept, r)
			}
		}
		schema["required"] = kept
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
