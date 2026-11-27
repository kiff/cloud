package gateway

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A call's arguments are checked against the tool's stored schema before
// anything is decided, held or stored, so the owner is never asked about a
// call the tool would refuse for its shape (a held browse_act that put a
// field's words in option, with no label, could never run). For now only
// planque's browser tools (schemaChecked); every other tool goes on as before.
//
// The check reads a small, exact subset of JSON Schema: type, required,
// properties, additionalProperties (false, or left out), items, enum, oneOf
// (of object schemas), and the bounds maxItems, minItems, maxLength, minimum
// and maximum; description, title, default, examples and format are words for
// the agent. A schema that uses anything else anywhere is not checked at all:
// the check refuses only what it can read for certain, never on a keyword it
// doesn't know.

// schemaChecked says whether t's calls are checked against its schema: a
// planque connection's browser tool, for the caller's own grant.
func schemaChecked(t Tool, relays []string, tenant string) bool {
	return connectionGrant(t, relays, tenant) != "" && strings.HasPrefix(t.UpstreamTool, "browse_")
}

// schemaRefusal is why args don't fit schema, as the agent reads it, or ""
// when they do, when there's no schema, or when it isn't one this check reads.
func schemaRefusal(schema json.RawMessage, args map[string]any) string {
	if len(schema) == 0 {
		return ""
	}
	var s map[string]any
	if json.Unmarshal(schema, &s) != nil || !readable(s) {
		return ""
	}
	if why := fits(s, args, "arguments"); why != "" {
		return "the arguments don't fit the tool's schema: " + why
	}
	return ""
}

var (
	checkedKeywords    = []string{"type", "required", "properties", "additionalProperties", "items", "enum", "oneOf", "maxItems", "minItems", "maxLength", "minimum", "maximum"}
	annotationKeywords = []string{"description", "title", "default", "examples", "format"}
)

// readable says every keyword in s, at every level, is one this check reads
// or a word for the agent, and each has the shape it expects.
func readable(s map[string]any) bool {
	for k, v := range s {
		switch {
		case slices.Contains(annotationKeywords, k):
		case !slices.Contains(checkedKeywords, k):
			return false
		case k == "properties":
			props, ok := v.(map[string]any)
			if !ok {
				return false
			}
			for _, p := range props {
				ps, ok := p.(map[string]any)
				if !ok || !readable(ps) {
					return false
				}
			}
		case k == "items":
			is, ok := v.(map[string]any)
			if !ok || !readable(is) {
				return false
			}
		case k == "oneOf":
			alts, ok := v.([]any)
			if !ok || len(alts) == 0 {
				return false
			}
			for _, a := range alts {
				as, ok := a.(map[string]any)
				if !ok || as["type"] != "object" || !readable(as) {
					return false
				}
			}
		case k == "additionalProperties":
			if b, ok := v.(bool); !ok || b {
				return false // true is the default, said; a schema here isn't read
			}
		case k == "required":
			if _, ok := v.([]any); !ok {
				return false
			}
		case k == "enum":
			if _, ok := v.([]any); !ok {
				return false
			}
		case k == "type":
			t, ok := v.(string)
			if !ok || !slices.Contains([]string{"object", "array", "string", "boolean", "integer", "number", "null"}, t) {
				return false
			}
		default: // the bounds
			if _, ok := v.(float64); !ok {
				return false
			}
		}
	}
	return true
}

var integerText = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// fits is why v doesn't fit s, at path, or "".
func fits(s map[string]any, v any, path string) string {
	if t, ok := s["type"].(string); ok {
		if why := typed(t, v, path); why != "" {
			return why
		}
	}
	if enum, ok := s["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return same(e, v) }) {
		return path + " isn't one of the values it allows"
	}
	if alts, ok := s["oneOf"].([]any); ok {
		n := 0
		for _, a := range alts {
			if fits(a.(map[string]any), v, path) == "" {
				n++
			}
		}
		if n != 1 {
			return path + " is none of the forms it allows: give it exactly one form's keys, each the form requires"
		}
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if name, ok := r.(string); ok {
					if _, there := x[name]; !there {
						return path + " is missing " + name
					}
				}
			}
		}
		for _, k := range sortedKeys(x) {
			p, listed := props[k].(map[string]any)
			if !listed {
				if s["additionalProperties"] == false {
					return path + " has " + k + ", which it doesn't take"
				}
				continue
			}
			if why := fits(p, x[k], path+"."+k); why != "" {
				return why
			}
		}
	case []any:
		if m, ok := s["maxItems"].(float64); ok && float64(len(x)) > m {
			return fmt.Sprintf("%s has %d items, more than %d", path, len(x), int(m))
		}
		if m, ok := s["minItems"].(float64); ok && float64(len(x)) < m {
			return fmt.Sprintf("%s has %d items, fewer than %d", path, len(x), int(m))
		}
		if is, ok := s["items"].(map[string]any); ok {
			for i, item := range x {
				if why := fits(is, item, path+"."+strconv.Itoa(i+1)); why != "" {
					return why
				}
			}
		}
	case string:
		if m, ok := s["maxLength"].(float64); ok && float64(len([]rune(x))) > m {
			return fmt.Sprintf("%s is longer than %d characters", path, int(m))
		}
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return "" // a number past a float's range isn't compared here
		}
		if m, ok := s["minimum"].(float64); ok && f < m {
			return fmt.Sprintf("%s is below %v", path, m)
		}
		if m, ok := s["maximum"].(float64); ok && f > m {
			return fmt.Sprintf("%s is above %v", path, m)
		}
	}
	return ""
}

// typed is why v isn't of JSON type t, at path, or "".
func typed(t string, v any, path string) string {
	ok := false
	switch t {
	case "object":
		_, ok = v.(map[string]any)
	case "array":
		_, ok = v.([]any)
	case "string":
		_, ok = v.(string)
	case "boolean":
		_, ok = v.(bool)
	case "number":
		_, ok = v.(json.Number)
	case "integer":
		n, isNum := v.(json.Number)
		ok = isNum && integerText.MatchString(string(n))
	case "null":
		ok = v == nil
	}
	if ok {
		return ""
	}
	return path + " isn't " + article(t) + " " + t
}

func article(t string) string {
	if strings.ContainsAny(t[:1], "aeiou") {
		return "an"
	}
	return "a"
}

// same says an enum's value e (as the schema's JSON reads it) is v (as the
// call's arguments read it, numbers as json.Number).
func same(e, v any) bool {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		ef, ok := e.(float64)
		return err == nil && ok && f == ef
	case string, bool, nil:
		return e == v
	}
	return false
}
