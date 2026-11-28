package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// A pay's describe (Thune). KIFF opens a purchase's case from planque's
// signed read of its checkout, never from the taf's words, so before a
// relay's browse_pay is decided the gateway asks planque for that read: the
// same tool, with {url, ref} and the gateway's own header
// CheckoutDescribeHeader naming the pay's operation (a taf can't set it),
// decided by KIFF on its own as "<call>:describe" and sent under its own
// permit. planque answers from the
// checkout it kept at browse_checkout, signed as planque/checkout/v1,
// starting no browser and sending nothing to the site; the pay's decision
// carries the block (kiffapi.Proposal.BrowserBlock). Only with purchases on
// (Gateway.Purchases).

// CheckoutDescribeHeader asks planque's browse_pay to describe the checkout
// of the pay it names (the pay's operation) instead of paying.
const CheckoutDescribeHeader = "KIFF-Checkout-Describe"

// payTool is planque's call that pays a checkout.
const payTool = "browse_pay"

// ReasonPayNotDescribed: a pay whose checkout couldn't be read back from
// planque, so it was refused, never decided.
const ReasonPayNotDescribed = "pay_not_described"

// describesPay says t's call is a relay's pay the gateway describes first.
func (g *Gateway) describesPay(t Tool) bool {
	return g.Purchases && t.UpstreamTool == payTool && t.needsPermit()
}

// describePay gets planque's signed checkout block for the pay in, or why it
// can't be had: then the pay is refused, and nothing is decided or bought.
func (a *agent) describePay(ctx context.Context, t Tool, in invocation.Invocation) (json.RawMessage, string) {
	var args struct {
		URL string `json:"url"`
		Ref string `json:"ref"`
	}
	if json.Unmarshal(in.Arguments, &args) != nil || args.URL == "" || args.Ref == "" {
		return nil, "a pay names its checkout's page and the control it clicks"
	}
	id := in.Key + ":describe"
	ask := map[string]any{"url": args.URL, "ref": args.Ref}
	if t.IdempotencyArg != "" {
		ask[t.IdempotencyArg] = id
	}
	body, _ := json.Marshal(ask)
	params, err := t.params(map[string]any{"url": args.URL, "ref": args.Ref})
	if err != nil {
		return nil, "the checkout couldn't be asked for"
	}
	if err := a.g.API.RequestToolCall(ctx, a.key, Domain, id, a.me.AgentID, a.g.now()); err != nil {
		return nil, "KIFF couldn't record the read of the checkout"
	}
	p := kiffapi.Proposal{ID: id, Domain: Domain, EntityID: id, EntityType: "ToolCall", ActionName: t.Name, ActorID: a.me.AgentID,
		Parameters: params, TrialDraft: a.trialDraft, RunGrant: a.runGrant, AgentRun: a.agentRun,
		Permit: &kiffapi.PermitRequest{Audience: t.Audience, Tool: t.UpstreamTool, Arguments: body}}
	d, err := a.g.API.Decide(ctx, a.key, p)
	if err != nil {
		return nil, "KIFF couldn't decide the read of the checkout"
	}
	if d.Outcome != "allowed" || d.Permit == "" {
		return nil, "the read of the checkout isn't allowed now (" + d.Outcome + ")"
	}
	timeout := a.g.ForwardTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fctx = mcp.WithCallHeader(fctx, PermitHeader, d.Permit)
	fctx = mcp.WithCallHeader(fctx, CheckoutDescribeHeader, in.Key)
	res, err := a.g.Forward.CallTool(fctx, t.UpstreamURL, "", t.UpstreamTool, body)
	if err != nil {
		if a.g.Logger != nil {
			a.g.Logger.WarnContext(ctx, "gateway: a pay's checkout wasn't read back", slog.String("invocation", in.Key), slog.String("err", firstLine(err.Error())))
		}
		return nil, "planque couldn't give the checkout it read"
	}
	var out struct {
		IsError    bool `json:"isError"`
		Structured struct {
			Block json.RawMessage `json:"browser_block"`
			Code  string          `json:"code"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(res, &out) != nil || out.IsError || len(out.Structured.Block) == 0 || len(out.Structured.Block) > maxBrowserBlock {
		why := "planque couldn't give the checkout it read"
		if out.Structured.Code != "" && codeShape(out.Structured.Code) {
			why += " (" + out.Structured.Code + ")"
		}
		return nil, why
	}
	return out.Structured.Block, ""
}

// payNotDescribed refuses a pay whose checkout couldn't be read back.
func (a *agent) payNotDescribed(ctx context.Context, in invocation.Invocation, from []invocation.State, why string) (json.RawMessage, error) {
	return a.finish(ctx, in.Key, from, invocation.StateRefused, invocation.Update{
		Message: "Not sent: KIFF buys only what planque read at the checkout, and " + why + ". Nothing was bought; read the checkout again (browse_checkout), then pay.",
		Reasons: []string{ReasonPayNotDescribed},
	}, nil)
}
