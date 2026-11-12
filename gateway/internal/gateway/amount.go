package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"math"

	"github.com/kiff/cloud/gateway/internal/invocation"
)

// Some writes act on an amount without carrying it: answering a dispute
// concedes or contests the dispute's amount, and the call names only the
// dispute (amount lookups). A Card can still limit such a call by
// that amount. The tool's AmountLookup names one of the agent's own read
// tools on the same connection that reads the call's resource, and where the
// amount is in its result. The gateway makes that read, as the agent and
// through its normal path, and puts what it says under the tool's AmountArg
// before KIFF decides. The amount never comes from the agent's arguments: a
// call that carries one is refused, and so is a call whose amount can't be
// read. Nothing is sent then.

// ReasonAmountUnverified is the refusal for a call whose amount KIFF couldn't
// read from its resource.
const ReasonAmountUnverified = "amount_unverified"

// ValidAmount reports what is wrong with l as t's amount lookup, "" when
// nothing: t needs the amount's name a Card uses and a resource to read, and
// the read is another tool.
func (l UnitLookup) ValidAmount(t Tool) string {
	switch {
	case t.ReadOnly:
		return "amount_lookup is for a write: a read has no amount for a Card to limit"
	case t.AmountArg == "" || t.ResourceArg == "":
		return "amount_lookup needs the tool's amount argument (the name its Card uses) and its resource argument"
	case t.AmountPath != "":
		return "amount_lookup reads the amount from the resource, so the tool can't also take it from the call (amount_path)"
	case !ValidToolName(l.Tool) || l.Tool == t.Name:
		return "amount_lookup.tool must name another of the account's tools: a read on the same connection"
	case !ValidToolName(l.Argument):
		return "amount_lookup.argument must name the read's argument that takes the resource"
	}
	if _, ok := PathSegments(l.Path); !ok {
		return "amount_lookup.path must be a dotted path in the read's result, such as amount"
	}
	return ""
}

// readAmount is the amount t's AmountLookup reads for the call's resource
// (params, as Tool.params made them), or why it couldn't. The read gets the
// call's own arguments both tools' schemas declare, as a unit lookup's does.
func (a *agent) readAmount(ctx context.Context, t Tool, params map[string]any, callArgs json.RawMessage) (int64, string) {
	l := t.AmountLookup
	resource, _ := params[t.ResourceArg].(string)
	read, err := a.tool(ctx, l.Tool)
	if err != nil || !read.ReadOnly || read.UpstreamURL != t.UpstreamURL || read.Mode != t.Mode || read.Audience != t.Audience {
		return 0, "the read set up to check it, " + l.Tool + ", isn't a read on the same connection"
	}
	// The read is called through callTool, which would run its own lookups:
	// a read that has one (a row stored before connect refused it) could
	// loop back here until the stack runs out and the gateway stops for
	// every account. A read with a lookup is never used.
	if read.AmountLookup != nil || read.UnitLookup != nil {
		return 0, "the read set up to check it, " + l.Tool + ", has a lookup of its own"
	}
	args, _ := json.Marshal(lookupArgs(read, t, *l, resource, callArgs))
	out, _, err := a.callTool(ctx, read.Name, args)
	if err != nil {
		return 0, "the read to check it failed"
	}
	n, why := amountIn(out, l.Path)
	if why == "" && t.ClaimRelated != nil {
		// The same read names the related object (a dispute's charge).
		if v, ok := firstIn(resultDocs(out), t.ClaimRelated.Path); ok {
			if s, isText := v.(string); isText {
				a.related = s
			}
		}
	}
	return n, why
}

// amountIn is the whole amount at path in a read's result, as unitIn reads a
// unit: its structured content, else its text as JSON. why says, in words, why
// there is none.
func amountIn(raw json.RawMessage, path string) (int64, string) {
	var res struct {
		IsError    bool                       `json:"isError"`
		Structured map[string]any             `json:"structuredContent"`
		Content    []struct{ Text string }    `json:"content"`
		Meta       map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return 0, "the read's answer couldn't be read"
	}
	if m, ok := res.Meta[MetaKey]; ok {
		var c Call
		if json.Unmarshal(m, &c) != nil {
			return 0, "the read's answer couldn't be read"
		}
		if c.State != string(invocation.StateForwarded) {
			return 0, lookupNotDone(c)
		}
	}
	if res.IsError {
		return 0, "the read to check it returned an error"
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
			if n, ok := wholeAmount(v); ok {
				return n, ""
			}
			return 0, "the read's amount isn't a whole number of at least 0"
		}
	}
	return 0, "the read's answer doesn't say it"
}

// wholeAmount is v as a whole number of at least 0, the way a Card counts.
func wholeAmount(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil && i >= 0
	case float64:
		if n < 0 || n != math.Trunc(n) || n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

// withLookedUpAmount completes params with the amount t's resource has, when
// t reads it (AmountLookup); why is set when it couldn't be read, and the call
// must not be decided or sent.
func (a *agent) withLookedUpAmount(ctx context.Context, t Tool, params map[string]any, callArgs json.RawMessage) (why string) {
	if t.AmountLookup == nil {
		return ""
	}
	n, why := a.readAmount(ctx, t, params, callArgs)
	if why != "" {
		return why
	}
	params[t.AmountArg] = n
	return ""
}

// AmountUnverifiedParam is the parameter KIFF is told, true, that the
// gateway couldn't read a refused call's amount (apps/api's
// GatewayAmountUnverifiedParam), so the owner's record has it as blocked.
const AmountUnverifiedParam = "kiff_amount_unverified"

// amountRefused refuses a call whose amount couldn't be read, and has KIFF
// record it as blocked, as a call in another unit is: the owner sees it in
// the record. Nothing is sent.
//
// An operation already recorded keeps its outcome and its key, whatever the
// read says now: a call that was sent, or whose outcome is unknown, never
// comes back as not sent, which could lead the agent to do it again under a
// new id. A held one stays held: it is
// never decided without its amount.
func (a *agent) amountRefused(ctx context.Context, t Tool, name, op string, args, params map[string]any, why string) (json.RawMessage, string, error) {
	argsJSON, _ := json.Marshal(args)
	in, created, err := a.g.Store.Begin(ctx, invocation.Call{
		TenantID: a.me.TenantID, CredentialID: a.credentialID, AgentID: a.me.AgentID,
		Tool: name, OperationID: op, Arguments: argsJSON, ReadOnly: t.ReadOnly,
	})
	if errors.Is(err, invocation.ErrOperationReused) {
		return operationReused(op), "", nil
	}
	if err != nil {
		return nil, "", err
	}
	work := context.WithoutCancel(ctx)
	if !created {
		var out json.RawMessage
		if in.State == invocation.StateHeld {
			out, err = a.replay(work, in.Key)
		} else {
			// retry decides only a held call; here it recovers a stale
			// one and answers with what was recorded.
			out, err = a.retry(work, t, in, params)
		}
		return withTrialDraft(withResource(out, t), a.trialDraft), in.Key, err
	}
	from := []invocation.State{invocation.StateDeciding}
	if err := a.g.API.RequestToolCall(work, a.key, Domain, in.Key, a.me.AgentID, in.CreatedAt); err == nil {
		blocked := maps.Clone(params)
		blocked[AmountUnverifiedParam] = true
		p := a.proposal(work, t, in, blocked, nil)
		// Nothing is sent, so nothing needs a permit.
		p.Permit = nil
		if _, err := a.g.API.Decide(work, a.key, p); err != nil && a.g.Logger != nil {
			a.g.Logger.WarnContext(work, "gateway: a call refused for its amount was not recorded by KIFF",
				slog.String("invocation", in.Key), slog.String("err", err.Error()))
		}
	} else if a.g.Logger != nil {
		a.g.Logger.WarnContext(work, "gateway: a call refused for its amount was not recorded by KIFF",
			slog.String("invocation", in.Key), slog.String("err", err.Error()))
	}
	out, err := a.finish(work, in.Key, from, invocation.StateRefused,
		invocation.Update{Message: amountUnverified(why), Reasons: []string{ReasonAmountUnverified}}, nil)
	return withTrialDraft(withResource(out, t), a.trialDraft), in.Key, err
}

// amountUnverified is the words for a call whose amount couldn't be read.
func amountUnverified(why string) string {
	return "KIFF couldn't read this call's amount from its resource (" + why + "), so it didn't send it. " +
		"Do not retry it; tell the account owner."
}

// asJSON is a call's arguments as the lookup reads them.
func asJSON(args map[string]any) json.RawMessage {
	b, _ := json.Marshal(args)
	return b
}
