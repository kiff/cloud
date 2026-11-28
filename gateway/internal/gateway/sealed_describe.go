package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// A sealed fill's describe. The owner's phone releases a sealed fill over
// the exact read the owner was shown, and the owner's Chrome names each
// read anew, so the call the owner allows must act on the read the card
// drew. Before the sealed call is decided, the gateway asks for that read:
// a plain browse_open of the call's page, on the same connection, decided
// by KIFF on its own as "<call>:open" (so a held sealed fill never opens
// more than an unheld open would), carrying SealedDescribeHeader. planque
// opens it as the sealed call's own run, keeps that run open for the
// owner, and answers with its signed block, which the sealed call's
// decision carries to KIFF (kiffapi.Proposal.BrowserBlock). KIFF keeps it
// on the hold, and a sealed fill can't be allowed without one.

// SealedDescribeHeader asks planque's browse_open to describe a sealed
// fill: base64url JSON of sealedDescribe. Only the gateway sends it.
const SealedDescribeHeader = "KIFF-Sealed-Describe"

// ReasonSealedNotDescribed: a sealed fill whose page couldn't be shown to
// its owner, so it was refused, never held.
const ReasonSealedNotDescribed = "sealed_fill_not_described"

// describeOpTool and describedTool are planque's browser tools a describe
// uses and serves.
const (
	describeOpTool = "browse_open"
	describedTool  = "browse_act"
)

// sealedDescribe is what planque is asked to describe: the sealed call's
// operation (its key), and the read, field and handle of its one step.
type sealedDescribe struct {
	Op     string `json:"op"`
	Read   string `json:"read"`
	Fill   string `json:"fill"`
	Sealed string `json:"sealed"`
}

// maxBrowserBlock bounds the block the gateway carries; KIFF bounds it too.
const maxBrowserBlock = 8 << 10

type browserBlockKey struct{}

// withBrowserBlock carries a described block to the sealed call's proposal.
func withBrowserBlock(ctx context.Context, block json.RawMessage) context.Context {
	return context.WithValue(ctx, browserBlockKey{}, block)
}

func browserBlockFrom(ctx context.Context) json.RawMessage {
	b, _ := ctx.Value(browserBlockKey{}).(json.RawMessage)
	return b
}

// describesSealed says t's call in is a sealed fill the gateway describes:
// planque's browse_act, marked sealed_fill (namesSealed).
func describesSealed(t Tool, params map[string]any) bool {
	code, _ := params[OwnerAskParam].(string)
	return code == OwnerAskSealedFill && t.UpstreamTool == describedTool
}

// describeSealed gets planque's signed block for the sealed call in, or
// why it can't be had: then the call is refused, and nothing is held.
func (a *agent) describeSealed(ctx context.Context, t Tool, in invocation.Invocation) (json.RawMessage, string) {
	var args struct {
		URL   string `json:"url"`
		Read  string `json:"read"`
		Steps []struct {
			Fill   string `json:"fill"`
			Sealed string `json:"sealed"`
		} `json:"steps"`
	}
	if json.Unmarshal(in.Arguments, &args) != nil || args.URL == "" || args.Read == "" || len(args.Steps) != 1 ||
		args.Steps[0].Fill == "" || args.Steps[0].Sealed == "" {
		return nil, "a call that fills a saved sign-in names its page, its read and that one step, and nothing else"
	}
	tools, err := a.g.tools(ctx, a.me.TenantID)
	if err != nil {
		return nil, "KIFF couldn't read this connection's tools"
	}
	var open Tool
	for _, c := range tools {
		if c.UpstreamURL == t.UpstreamURL && c.UpstreamTool == describeOpTool {
			open = c
		}
	}
	if open.Name == "" {
		return nil, "this connection has no " + describeOpTool + ", which shows the owner the page first"
	}
	// The open, as its own call: its own id, its arguments, KIFF's own
	// decision on it, and its own permit.
	id := in.Key + ":open"
	openArgs := map[string]any{"url": args.URL}
	if open.IdempotencyArg != "" {
		openArgs[open.IdempotencyArg] = id
	}
	body, _ := json.Marshal(openArgs)
	params, err := open.params(map[string]any{"url": args.URL})
	if err != nil {
		return nil, "the page couldn't be asked for"
	}
	if err := a.g.API.RequestToolCall(ctx, a.key, Domain, id, a.me.AgentID, a.g.now()); err != nil {
		return nil, "KIFF couldn't record the open that shows the owner the page"
	}
	p := kiffapi.Proposal{ID: id, Domain: Domain, EntityID: id, EntityType: "ToolCall", ActionName: open.Name, ActorID: a.me.AgentID,
		Parameters: params, TrialDraft: a.trialDraft, RunGrant: a.runGrant, AgentRun: a.agentRun}
	if open.needsPermit() {
		p.Permit = &kiffapi.PermitRequest{Audience: open.Audience, Tool: open.UpstreamTool, Arguments: body}
	}
	d, err := a.g.API.Decide(ctx, a.key, p)
	if err != nil {
		return nil, "KIFF couldn't decide the open that shows the owner the page"
	}
	if d.Outcome != "allowed" || (open.needsPermit() && d.Permit == "") {
		// Held or refused on its own: a sealed fill never gets an open its
		// taf couldn't get unheld.
		return nil, "the open that shows the owner the page isn't allowed now (" + d.Outcome + ")"
	}
	credential := ""
	if a.g.Credentials != nil && open.Mode != ModeVerify {
		if credential, err = a.g.Credentials.Credential(ctx, a.me.TenantID, open.Name); err != nil {
			return nil, "the connection's stored credential couldn't be read"
		}
	}
	ask, _ := json.Marshal(sealedDescribe{Op: in.Key, Read: args.Read, Fill: args.Steps[0].Fill, Sealed: args.Steps[0].Sealed})
	timeout := a.g.ForwardTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if d.Permit != "" {
		fctx = mcp.WithCallHeader(fctx, PermitHeader, d.Permit)
	}
	fctx = mcp.WithCallHeader(fctx, SealedDescribeHeader, base64.RawURLEncoding.EncodeToString(ask))
	res, err := a.g.Forward.CallTool(fctx, open.UpstreamURL, credential, open.UpstreamTool, body)
	if err != nil {
		if a.g.Logger != nil {
			a.g.Logger.WarnContext(ctx, "gateway: a sealed fill wasn't described", slog.String("invocation", in.Key), slog.String("err", firstLine(err.Error())))
		}
		return nil, "the owner's Chrome couldn't show the page"
	}
	var out struct {
		IsError    bool `json:"isError"`
		Structured struct {
			Block json.RawMessage `json:"browser_block"`
			Code  string          `json:"code"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(res, &out) != nil || out.IsError || len(out.Structured.Block) == 0 || len(out.Structured.Block) > maxBrowserBlock {
		why := "the owner's Chrome couldn't show the page"
		if out.Structured.Code != "" && codeShape(out.Structured.Code) {
			why += " (" + out.Structured.Code + ")"
		}
		return nil, why
	}
	return out.Structured.Block, ""
}

// sealedNotDescribed refuses a sealed fill that couldn't be described.
func (a *agent) sealedNotDescribed(ctx context.Context, in invocation.Invocation, from []invocation.State, why string) (json.RawMessage, error) {
	return a.finish(ctx, in.Key, from, invocation.StateRefused, invocation.Update{
		Message: "Not sent, and not held: this call fills a sign-in saved in the owner's Chrome, and " + why + ". Nothing was filled.",
		Reasons: []string{ReasonSealedNotDescribed},
	}, nil)
}

// codeShape says c is a refusal code planque would write: lower case and
// underscores, short. Anything else isn't passed on.
func codeShape(c string) bool {
	if len(c) == 0 || len(c) > 40 {
		return false
	}
	return strings.Trim(c, "abcdefghijklmnopqrstuvwxyz_") == ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
