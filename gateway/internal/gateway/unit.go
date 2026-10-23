package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
)

// A Card counts a tool's amounts in one unit (AmountUnit), and a call can
// be in another: a Stripe refund is in its payment's currency, which the
// call doesn't say. A tool's UnitLookup names one
// of the agent's own read tools on the same connection that reads the
// call's resource (the payment) and where the unit is in its result. The
// gateway makes that read, as the agent and through its normal path, before
// KIFF decides the call, and refuses a call in another unit, or one whose
// unit it can't read. The Card then only ever counts amounts in its unit.

// UnitLookup is where a tool's calls say their unit: the read tool Tool,
// called with the call's resource as Argument, and the unit at Path in its
// result (dotted, "currency").
type UnitLookup struct {
	Tool     string `json:"tool"`
	Argument string `json:"argument"`
	Path     string `json:"path"`
}

// UnitParam is the parameter KIFF is told a refused call's unit in: as the
// read said it, or UnitUnverified (apps/api's GatewayUnitParam).
const (
	UnitParam      = "kiff_amount_unit"
	UnitUnverified = "unverified"
)

// Reasons a unit check refuses a call for.
const (
	ReasonUnitMismatch   = "amount_unit_mismatch"
	ReasonUnitUnverified = "amount_unit_unverified"
)

// Valid reports what is wrong with l for tool t, "" when nothing: t needs an
// amount in a unit and a resource for the read to look up.
func (l UnitLookup) Valid(t Tool) string {
	switch {
	case t.AmountArg == "" || t.AmountUnit == "" || t.ResourceArg == "":
		return "unit_lookup needs the tool's amount argument, its amount unit and its resource argument"
	case !ValidToolName(l.Tool) || l.Tool == t.Name:
		return "unit_lookup.tool must name another of the account's tools: a read on the same connection"
	case !ValidToolName(l.Argument):
		return "unit_lookup.argument must name the read's argument that takes the resource"
	}
	if _, ok := PathSegments(l.Path); !ok {
		return "unit_lookup.path must be a dotted path in the read's result, such as currency"
	}
	return ""
}

// checkUnit is the refusal for a call to t in another unit than its
// Card's, or whose unit can't be read, and the unit it read (or
// UnitUnverified); "" when the unit matches or t has no lookup. A resource's unit is read once and kept for unitTTL: a
// payment's currency doesn't change. callArgs are the call's own arguments,
// which the read shares where it takes them (lookupArgs).
func (a *agent) checkUnit(ctx context.Context, t Tool, params map[string]any, callArgs json.RawMessage) (reason, message, observed string) {
	l := t.UnitLookup
	if l == nil {
		return "", "", ""
	}
	unverified := func(why string) (string, string, string) {
		return ReasonUnitUnverified, "KIFF couldn't check this call's currency (" + why + "), so it didn't send it. " +
			"Do not retry it; tell the account owner.", UnitUnverified
	}
	resource, _ := params[t.ResourceArg].(string)
	read, err := a.tool(ctx, l.Tool)
	if err != nil || !read.ReadOnly || read.UpstreamURL != t.UpstreamURL || read.Mode != t.Mode || read.Audience != t.Audience {
		return unverified("the read set up to check it, " + l.Tool + ", isn't a read on the same connection")
	}
	args, _ := json.Marshal(lookupArgs(read, t, *l, resource, callArgs))
	// The unit is kept per resource and scope: the same id in another
	// account is another resource.
	key := unitKey{a.me.TenantID, l.Tool, string(args)}
	unit, ok := a.g.units.get(key, a.g.now())
	if !ok {
		out, _, err := a.callTool(ctx, read.Name, args)
		if err != nil {
			return unverified("the read to check it failed")
		}
		var why string
		if unit, why = unitIn(out, l.Path); why != "" {
			return unverified(why)
		}
		a.g.units.put(key, unit, a.g.now())
	}
	if !strings.EqualFold(unit, t.AmountUnit) {
		return ReasonUnitMismatch, fmt.Sprintf("This call is in %s and the Card's limits are in %s, so KIFF didn't send it. "+
			"Do not retry it; tell the account owner.", strings.ToUpper(unit), strings.ToUpper(t.AmountUnit)), strings.ToUpper(unit)
	}
	return "", "", ""
}

// lookupArgs is what the read is called with: the resource under the read's
// argument, and each other argument of the call that both tools' schemas
// declare, whatever the vendor calls it (the account, workspace or region
// the call is in). Without them a vendor that scopes every call refused the
// read, and so KIFF refused every call it checked (kiff/tafs#138, item 50).
// Only names both schemas declare: the gateway doesn't hold a call to its
// tool's schema, so an argument only the read takes (another id) would let
// the agent choose what its own check reads. Left out
// too: both tools' fixed arguments (KIFF sets the read's own, and a call
// that sets one is refused), their idempotency arguments, and the call's
// amount and resource.
func lookupArgs(read, t Tool, l UnitLookup, resource string, callArgs json.RawMessage) map[string]any {
	in := map[string]any{l.Argument: resource}
	var args map[string]any
	var readSchema, toolSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(callArgs, &args) != nil || json.Unmarshal(read.InputSchema, &readSchema) != nil ||
		json.Unmarshal(t.InputSchema, &toolSchema) != nil {
		return in
	}
	skip := map[string]bool{l.Argument: true, OperationArg: true, t.AmountArg: true, t.ResourceArg: true}
	for _, tool := range []Tool{read, t} {
		if tool.IdempotencyArg != "" {
			skip[tool.IdempotencyArg] = true
		}
		if fixed, err := tool.fixedArgs(); err == nil {
			for k := range fixed {
				skip[k] = true
			}
		}
	}
	for name := range readSchema.Properties {
		if _, declared := toolSchema.Properties[name]; !declared || skip[name] {
			continue
		}
		if v, ok := args[name]; ok {
			in[name] = v
		}
	}
	return in
}

// unitIn is the unit at path in a read's result: its structured content,
// else its text as JSON. why says, in words, why there is none.
func unitIn(raw json.RawMessage, path string) (unit, why string) {
	var res struct {
		IsError    bool                    `json:"isError"`
		Structured map[string]any          `json:"structuredContent"`
		Content    []struct{ Text string } `json:"content"`
		// A vendor's own _meta keys are anything (Stripe's is a session
		// id string): only KIFF's is a Call (kiff/tafs#138, item 50).
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return "", "the read's answer couldn't be read"
	}
	if m, ok := res.Meta[MetaKey]; ok {
		var c Call
		if json.Unmarshal(m, &c) != nil {
			return "", "the read's answer couldn't be read"
		}
		if c.State != string(invocation.StateForwarded) {
			return "", lookupNotDone(c)
		}
	}
	if res.IsError {
		return "", "the read to check it returned an error"
	}
	docs := []map[string]any{res.Structured}
	for _, c := range res.Content {
		var m map[string]any
		if json.Unmarshal([]byte(c.Text), &m) == nil {
			docs = append(docs, m)
		}
	}
	for _, d := range docs {
		if d == nil {
			continue
		}
		if v, ok := lookupPath(d, path); ok {
			if s, ok := v.(string); ok && amountUnit.MatchString(s) {
				return s, ""
			}
		}
	}
	return "", "the read's answer doesn't say it"
}

// lookupNotDone says why the read KIFF made to check a call's unit didn't
// run: what its own Card or KIFF said.
func lookupNotDone(c Call) string {
	for _, r := range c.Reasons {
		switch r {
		case "mandate_limit_reached":
			return "the Card's reads for this period are used up"
		case "no_mandate_issued", "mandate_not_covered":
			return "the agent's Card doesn't cover the read that checks it"
		}
	}
	if c.State == string(invocation.StateHeld) {
		return "the read that checks it is waiting for the owner"
	}
	return "the read that checks it was " + c.State
}

// unitTTL is how long a resource's unit is kept.
const unitTTL = 24 * time.Hour

// unitKey is a resource's unit's key: the account, the read, and the exact
// arguments the read was called with (the resource and its scope).
type unitKey struct{ tenant, tool, read string }

type unitEntry struct {
	unit string
	at   time.Time
}

// unitCache keeps each resource's unit, per account and read.
type unitCache struct {
	mu sync.Mutex
	m  map[unitKey]unitEntry
}

func (c *unitCache) get(k unitKey, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || now.Sub(e.at) >= unitTTL {
		return "", false
	}
	return e.unit, true
}

// maxUnits bounds the cache; a full one starts again.
const maxUnits = 10000

func (c *unitCache) put(k unitKey, unit string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= maxUnits {
		c.m = map[unitKey]unitEntry{}
	}
	c.m[k] = unitEntry{unit, now}
}
