// Package gateway decides every tool call an agent makes through KIFF
// and forwards, holds or refuses it.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
	"github.com/kiff/cloud/gateway/internal/secret"
)

// Domain is the gateway domain every account's ToolCall contract lives
// in: one per account.
const Domain = "gateway"

// OperationArg is the argument the gateway adds to every tool.
const OperationArg = "kiff_operation_id"

// Tool is a tool an account has connected.
type Tool struct {
	Name string
	// Title is the name people see, as stored at connect; empty for a row
	// the gateway doesn't know one for.
	Title        string
	Description  string
	InputSchema  json.RawMessage
	UpstreamURL  string
	UpstreamTool string
	AmountArg    string
	// AmountUnit and AmountScale say what AmountArg means, for display:
	// unit EUR with scale 2 makes 8000 read as 80.00 EUR. Empty unit: bare.
	AmountUnit     string
	AmountScale    int
	IdempotencyArg string
	// ResourceArg is the string argument naming what a call is on.
	// Its value is sent to KIFF with the amount, so a Card's resource scope
	// can read it. Empty: none is sent.
	ResourceArg string
	// AmountPath and ResourcePath, when set, say where the call carries
	// the amount and the resource ("parameters.amount"); AmountArg and
	// ResourceArg stay the names a Card uses for them. Empty: the
	// argument of that name at the top level.
	AmountPath   string
	ResourcePath string
	// FixedArgs are arguments KIFF always sends and the agent can't set,
	// e.g. {"stripe_api_operation_id": "PostRefunds"}.
	FixedArgs json.RawMessage
	// ReadOnly: the owner declared the tool only reads. Its calls are
	// decided as any other, but never treated as retries.
	ReadOnly bool
	// Mode is who holds the downstream credential: ModeSealed
	// (KIFF), ModeVerify (nobody; the tool checks the permit) or
	// ModeRelay (the customer's relay). Audience is what the tool or
	// relay expects in the permit.
	Mode                string
	Audience            string
	OutcomeContract     string
	OutcomeLookupTool   string
	ConnectionCreatedAt time.Time
	// UnitLookup, when set, is the read that says which unit a call is in
	// (a payment's currency): a call in another unit than AmountUnit is
	// refused before KIFF decides it.
	UnitLookup *UnitLookup
}

// Custody modes.
const (
	ModeSealed = "sealed"
	ModeVerify = "verify"
	ModeRelay  = "relay"
)

// PermitHeader carries the execution permit on a forwarded tools/call.
const PermitHeader = "KIFF-Permit"

// needsPermit reports whether the tool runs only with an execution permit.
func (t Tool) needsPermit() bool { return t.Mode == ModeVerify || t.Mode == ModeRelay }

// forwardArgs is the exact argument object sent to the tool: the agent's
// arguments plus the operation id under the tool's idempotency argument.
// apps/api hashes these bytes into the permit, so they are built once.
func forwardArgs(t Tool, in invocation.Invocation) json.RawMessage {
	var args map[string]any
	_ = json.Unmarshal(in.Arguments, &args)
	if args == nil {
		args = map[string]any{}
	}
	if t.IdempotencyArg != "" {
		args[t.IdempotencyArg] = in.Key
	}
	body, _ := json.Marshal(args)
	return body
}

// Catalog lists an account's connected tools.
type Catalog interface {
	Tools(ctx context.Context, tenantID string) ([]Tool, error)
}

// API is the part of apps/api the gateway uses.
type API interface {
	Me(ctx context.Context, key string) (kiffapi.Me, error)
	// AgentCard reads the agent's Cards; trial is the request's KIFF-Trial.
	AgentCard(ctx context.Context, key, trial string) (kiffapi.AgentCard, error)
	RequestToolCall(ctx context.Context, key, domain, entityID, agentID string, at time.Time) error
	Decide(ctx context.Context, key string, p kiffapi.Proposal) (kiffapi.Decision, error)
	Holds(ctx context.Context, key string, ids []string) ([]kiffapi.Hold, error)
	Trial(ctx context.Context, key string, t kiffapi.TrialCall) (kiffapi.TrialDecision, error)
	// TrialOpen reports whether a draft is being tried (kiff/tafs#173).
	TrialOpen(ctx context.Context, key, draft string) (bool, error)
}

// Credentials decrypts an account's stored credential for a tool, or
// returns "" when the tool has none.
type Credentials interface {
	Credential(ctx context.Context, tenantID, tool string) (string, error)
}

// Forwarder calls a connected tool.
type Forwarder interface {
	CallTool(ctx context.Context, url, bearer, tool string, args json.RawMessage) (json.RawMessage, error)
}

// Store is the invocation store.
type Store interface {
	Begin(ctx context.Context, c invocation.Call) (invocation.Invocation, bool, error)
	Get(ctx context.Context, key string) (invocation.Invocation, error)
	Transition(ctx context.Context, key string, from []invocation.State, to invocation.State, u invocation.Update) (bool, error)
	BindExecution(ctx context.Context, key string, binding json.RawMessage) (bool, error)
	RecoverStale(ctx context.Context, key string, staleBefore time.Time) (int64, error)
	PurgeResults(ctx context.Context, before time.Time) (int64, error)
	Pending(ctx context.Context, credentialID string, since time.Time, page *invocation.PendingPage, limit int) ([]invocation.Invocation, error)
}

// Gateway serves agents.
type Gateway struct {
	API     API
	Catalog Catalog
	Store   Store
	Forward Forwarder
	// Credentials is nil when no tool has a stored credential.
	Credentials Credentials
	// AppURL absolutizes Needs you links (https://app.kiff.dev).
	AppURL string
	// ForwardTimeout bounds one call to a tool.
	ForwardTimeout time.Duration
	// Lease is how long a call may stay deciding or forwarding before it
	// is taken as abandoned by a process that died. It must exceed the
	// longest a live handler takes (API calls plus ForwardTimeout).
	Lease time.Duration
	// Resource is the gateway's public MCP URL
	// (https://mcp.kiff.dev/mcp) and Issuer its authorization server
	// (https://api.kiff.dev), for OAuth discovery. Empty
	// Resource turns discovery off.
	Resource string
	Issuer   string
	// ResultRetention is how long a forwarded call keeps the tool's
	// result; zero is DefaultResultRetention.
	ResultRetention time.Duration
	Logger          *slog.Logger
	Now             func() time.Time
	// ChatClients may be offered a held call in the chat;
	// empty offers it to none.
	ChatClients ChatClients
	// Clients records which MCP clients each agent uses; nil skips it.
	Clients ClientRecorder
	prompts prompts
	seen    seenCache
	units   unitCache
}

// DefaultLease is the in-flight lease when none is set.
const DefaultLease = 2 * time.Minute

func (g *Gateway) lease() time.Duration {
	if g.Lease > 0 {
		return g.Lease
	}
	return DefaultLease
}

// ResourceMetadataURL is where the gateway's protected resource
// metadata is served (RFC 9728 path insertion), or "".
func (g *Gateway) ResourceMetadataURL() string { return metadataURL(g.Resource) }

func metadataURL(resource string) string {
	u, err := url.Parse(resource)
	if resource == "" || err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + u.Path
}

// LinkResource is a connect link's URL: the gateway origin, /a/<id>/mcp.
func (g *Gateway) LinkResource(linkID string) string {
	u, err := url.Parse(g.Resource)
	if g.Resource == "" || err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/a/" + linkID + "/mcp"
}

// ValidLinkID matches apps/api's link ids: "cl_" and base64url.
func ValidLinkID(id string) bool {
	if !strings.HasPrefix(id, "cl_") || len(id) < 10 || len(id) > 64 {
		return false
	}
	for _, r := range id[3:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// ResourceMetadata serves RFC 9728 metadata for the generic URL.
func (g *Gateway) ResourceMetadata(w http.ResponseWriter, r *http.Request) {
	g.resourceMetadata(w, g.Resource)
}

// LinkResourceMetadata serves it for a connect link's URL.
func (g *Gateway) LinkResourceMetadata(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !ValidLinkID(id) {
		http.NotFound(w, r)
		return
	}
	g.resourceMetadata(w, g.LinkResource(id))
}

func (g *Gateway) resourceMetadata(w http.ResponseWriter, resource string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":                 resource,
		"authorization_servers":    []string{g.Issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"gateway"},
		"resource_name":            "KIFF gateway",
	})
}

// SweepStale resolves every abandoned in-flight call, so the record is
// truthful even if the agent never retries.
func (g *Gateway) SweepStale(ctx context.Context) (int64, error) {
	return g.Store.RecoverStale(ctx, "", g.now().Add(-g.lease()))
}

// DefaultResultRetention is how long a forwarded call keeps the tool's
// own result, so a retry of it gets the same answer.
const DefaultResultRetention = 24 * time.Hour

// SweepResults drops the tool results kept past their retention.
func (g *Gateway) SweepResults(ctx context.Context) (int64, error) {
	keep := g.ResultRetention
	if keep <= 0 {
		keep = DefaultResultRetention
	}
	return g.Store.PurgeResults(ctx, g.now().Add(-keep))
}

func (g *Gateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Server is the MCP endpoint for agents.
func (g *Gateway) Server(version string) *mcp.Server {
	return g.serverFor(version, g.Resource, false)
}

// LinkServer serves a connect link's URL, /a/<id>/mcp.
// It accepts only OAuth tokens issued for that URL.
func (g *Gateway) LinkServer(version, linkID string) *mcp.Server {
	return g.serverFor(version, g.LinkResource(linkID), true)
}

func (g *Gateway) serverFor(version, resource string, link bool) *mcp.Server {
	return &mcp.Server{
		ResourceMetadata: metadataURL(resource),
		Tasks:            true,
		Experimental:     map[string]any{TrialCapability: true, TrialSendsCapability: true},
		Auth: func(r *http.Request) (mcp.Handler, error) {
			return g.authenticate(r, resource, link)
		},
		Name:    "kiff-gateway",
		Version: version,
		Instructions: "Tools reached through KIFF are checked against the Cards their owner issued. " +
			"A call may be held for the owner's approval; nothing runs until they answer. " +
			"Each tool's description says what your Card allows; " + CardTool + " shows what is left now. " +
			"Give each new action a new " + OperationArg + " and reuse it when retrying the same action. " +
			"When you start work, call " + PendingTool + " to pick up calls that wait on the owner or whose outcome is unknown.",
	}
}

// agent is one authenticated caller.
type agent struct {
	g            *Gateway
	key          string
	credentialID string
	me           kiffapi.Me
	// trial is the request's KIFF-Trial: whose Cards decide a trial's
	// writes, which are never sent (trial.go). Empty: not a trial.
	// trialRun is its KIFF-Trial-Run, the trial's run.
	trial, trialRun string
	// trialDraft is set once KIFF said the KIFF-Trial draft is being tried
	// (kiff/tafs#173): its calls are sent, decided on its trial Cards.
	trialDraft string
}

// authenticate checks the bearer for the URL it was called on. An OAuth
// token is accepted only at the URL it was issued for; a link URL
// accepts nothing else, so it always means the agent it names.
func (g *Gateway) authenticate(r *http.Request, resource string, link bool) (mcp.Handler, error) {
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(key) == "" {
		return nil, errors.New("no key")
	}
	key = strings.TrimSpace(key)
	me, err := g.API.Me(r.Context(), key)
	if errors.Is(err, kiffapi.ErrUnavailable) {
		return nil, errors.Join(mcp.ErrUnavailable, err)
	}
	if err != nil {
		return nil, err
	}
	// Only a key bound to one agent can act through the gateway: every
	// decision names the agent, and each can be revoked alone.
	if me.AgentID == "" || me.TenantID == "" {
		return nil, errors.New("key is not bound to an agent")
	}
	oauth := strings.HasPrefix(key, OAuthTokenPrefix)
	if oauth && me.CredentialID == "" {
		return nil, errors.New("KIFF did not name the key behind this token")
	}
	switch {
	case link && !oauth:
		return nil, errors.New("a connect link URL accepts only a token issued through it")
	case oauth && resource != "" && me.Resource != resource:
		return nil, errors.New("this token was issued for another KIFF gateway URL")
	}
	return &agent{g: g, key: key, credentialID: credentialID(key, me), me: me,
		trial: strings.TrimSpace(r.Header.Get(TrialHeader)), trialRun: strings.TrimSpace(r.Header.Get(TrialRunHeader))}, nil
}

// OAuthTokenPrefix marks an OAuth access token.
const OAuthTokenPrefix = "kiff_oat_"

// credentialID is what retries are keyed on (invocation keys). A gateway
// key is its own stable credential, keyed by its hash as before. An
// OAuth access token rotates every hour, so it is keyed by the key behind
// it: a retry after a refresh is still the same caller, and is never
// forwarded twice.
func credentialID(bearer string, me kiffapi.Me) string {
	if strings.HasPrefix(bearer, OAuthTokenPrefix) && me.CredentialID != "" {
		sum := sha256.Sum256([]byte("key:" + me.CredentialID))
		return hex.EncodeToString(sum[:16])
	}
	sum := sha256.Sum256([]byte(bearer))
	return hex.EncodeToString(sum[:16])
}

// operationSchema is added to every tool's input schema.
var operationSchema = map[string]any{
	"type": "string",
	"description": "Optional. A new id for each new action; the same id when retrying the same action. " +
		"Without it, an identical call within 10 minutes is treated as a retry of the first.",
}

func (a *agent) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	a.seeClient(ctx)
	tools, err := a.g.Catalog.Tools(ctx, a.me.TenantID)
	if err != nil {
		return nil, err
	}
	// A key without the gateway role (an agent that holds no tools, such as
	// kiff/agents' setup agent) can call none of the
	// account's tools, since the contract grants them only to that role, so
	// it isn't shown them: only KIFF's own.
	if !slices.Contains(a.me.Roles, Role) {
		tools = nil
	}
	// What the agent's Card allows goes on each tool, so the agent knows
	// its limits before it calls. Without the Card the tools are
	// listed as they are: every call is still decided when it is made.
	// In a trial, the Cards its calls are decided on (kiff/tafs#107).
	card, cardErr := a.g.API.AgentCard(ctx, a.key, a.trial)
	out := make([]mcp.Tool, 0, len(tools)+1)
	for _, t := range tools {
		if ReservedToolName(t.Name) {
			continue // connected before the name was reserved; kiff_card answers it
		}
		desc := t.Description
		if note := t.fixedNote(); note != "" {
			desc = strings.TrimSpace(desc + "\n\n" + note)
		}
		if cardErr == nil {
			desc = withCardLine(desc, card, t)
		}
		var schema map[string]any
		if err := json.Unmarshal(t.InputSchema, &schema); err != nil || schema == nil {
			schema = map[string]any{"type": "object"}
		}
		props, _ := schema["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
		}
		props[OperationArg] = operationSchema
		if t.IdempotencyArg != "" {
			// KIFF sets it from the invocation; the agent never does.
			dropArg(schema, props, t.IdempotencyArg)
		}
		if fixed, err := t.fixedArgs(); err == nil {
			// KIFF sends these; a call that sets one is refused.
			for k := range fixed {
				dropArg(schema, props, k)
			}
		}
		schema["properties"] = props
		b, _ := json.Marshal(schema)
		title := t.Title
		if title == "" {
			title = ToolTitle(t.Name)
		}
		out = append(out, mcp.Tool{Name: t.Name, Title: title, Description: desc, InputSchema: b,
			Execution: &mcp.ToolExecution{TaskSupport: "optional"},
			// From the owner's read-only flag. Any other tool can change
			// things, so it is marked destructive and Claude always asks.
			Annotations: &mcp.ToolAnnotations{Title: title, ReadOnlyHint: t.ReadOnly, DestructiveHint: !t.ReadOnly}})
	}
	return append(out, cardToolListing(), pendingToolListing()), nil
}

// ToolTitle is the fallback name people see for a tool whose vendor gives
// none: its name with spaces for underscores, capitalized ("issue_refund" is
// "Issue refund"). The vendor's own title comes first (connection.create).
func ToolTitle(name string) string {
	s := strings.Join(strings.Fields(strings.ReplaceAll(name, "_", " ")), " ")
	if s == "" {
		return name
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (a *agent) tool(ctx context.Context, name string) (Tool, error) {
	tools, err := a.g.Catalog.Tools(ctx, a.me.TenantID)
	if err != nil {
		return Tool{}, err
	}
	for _, t := range tools {
		if t.Name == name {
			return t, nil
		}
	}
	return Tool{}, mcp.UnknownTool(name)
}

// splitArgs takes kiff_operation_id out of the tool's arguments.
func splitArgs(raw json.RawMessage) (map[string]any, string, error) {
	args := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return nil, "", errors.New("arguments must be a JSON object")
		}
	}
	op := ""
	if v, ok := args[OperationArg]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, "", errors.New(OperationArg + " must be a string")
		}
		op = strings.TrimSpace(s)
		delete(args, OperationArg)
	}
	return args, op, nil
}

func invalid(msg string) error { return &mcp.RPCError{Code: -32602, Message: msg} }

func (a *agent) CallTool(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, error) {
	// Every spelling ListTools and connection creation reserve: a tool
	// connected under one before the reservation is never reached.
	if ReservedToolName(name) {
		a.seeClient(ctx)
		if strings.EqualFold(name, PendingTool) {
			return a.pendingResult(ctx, raw)
		}
		return a.cardResult(ctx)
	}
	out, key, err := a.callTool(ctx, name, raw)
	if err != nil {
		return nil, err
	}
	a.answerAsTask(ctx, key)
	return out, nil
}

// params are the proposal parameters a Card limits: the tool's amount and
// the resource the call is on.
func (t Tool) params(args map[string]any) (map[string]any, error) {
	params := map[string]any{}
	if t.AmountArg != "" {
		// Required wherever it sits: a missing amount is never zero (for a
		// Stripe refund it would mean the whole charge).
		path := t.amountPath()
		v, ok := lookupPath(args, path)
		n, isNum := v.(json.Number)
		if !ok || !isNum {
			return nil, invalid(path + " is required and must be a whole number")
		}
		i, err := n.Int64()
		if err != nil {
			return nil, invalid(path + " must be a whole number")
		}
		params[t.AmountArg] = i
	}
	if t.ResourceArg != "" {
		path := t.resourcePath()
		v, _ := lookupPath(args, path)
		s, ok := v.(string)
		if !ok || s == "" || len(s) > MaxResourceLength {
			return nil, invalid(path + " is required and must be text of up to " + strconv.Itoa(MaxResourceLength) + " characters")
		}
		params[t.ResourceArg] = s
	}
	return params, nil
}

// MaxResourceLength is the longest resource value a Card scope can list.
const MaxResourceLength = 256

// callTool decides and, if allowed, forwards one call. key is the
// invocation it ran as ("" when none was recorded).
func (a *agent) callTool(ctx context.Context, name string, raw json.RawMessage) (out json.RawMessage, key string, err error) {
	a.seeClient(ctx)
	t, err := a.tool(ctx, name)
	if err != nil {
		return nil, "", err
	}
	args, op, err := splitArgs(raw)
	if err != nil {
		return nil, "", invalid(err.Error())
	}
	if args, err = t.withFixed(args); err != nil {
		return nil, "", err
	}
	params, err := t.params(args)
	if err != nil {
		return nil, "", err
	}
	if a.trial != "" && a.trialDraft == "" {
		sends, err := a.trialSends(ctx)
		if err != nil {
			// Fail closed: neither sent nor shown as a decision it isn't.
			return kiffResult(fmt.Sprintf("KIFF could not say whether this version is being tried, so the call was not sent. "+
				"Retry the same call in about %d seconds.", retryKIFFDown),
				Call{State: string(invocation.StateFailed), OperationID: op, Sent: "no",
					Reasons: []string{ReasonDecideUnavailable}, Retry: "same_call", RetryAfterS: retryKIFFDown}), "", nil
		}
		if !sends {
			// A trial's call is decided and never sent: no invocation, no
			// proposal, nothing forwarded. Reads included: a tool's read-only
			// flag can be a model's claim no vendor confirmed (kiff/agents#139),
			// so it can't decide what a trial sends.
			return a.trialResult(ctx, t, params), "", nil
		}
		a.trialDraft = a.trial
	}
	argsJSON, _ := json.Marshal(args)

	in, created, err := a.g.Store.Begin(ctx, invocation.Call{
		TenantID: a.me.TenantID, CredentialID: a.credentialID, AgentID: a.me.AgentID,
		Tool: name, OperationID: op, Arguments: argsJSON, ReadOnly: t.ReadOnly,
	})
	if errors.Is(err, invocation.ErrOperationReused) {
		return kiffResult("Refused: "+OperationArg+" "+op+" was already used for this tool with different arguments. "+
			"Use a new "+OperationArg+" for a new action.",
			Call{State: string(invocation.StateRefused), OperationID: op, Sent: "no",
				Reasons: []string{ReasonOperationIDReused}, Retry: "new_call"}), "", nil
	}
	if err != nil {
		return nil, "", err
	}
	// The work below must finish even if the agent disconnects: a
	// decision or a forward without its record is the failure to avoid.
	work := context.WithoutCancel(ctx)
	if !created {
		out, err = a.retry(work, t, in, params)
	} else {
		out, err = a.first(work, t, in, params)
	}
	return withTrialDraft(withResource(out, t), a.trialDraft), in.Key, err
}

func (a *agent) proposal(t Tool, in invocation.Invocation, params map[string]any, body json.RawMessage) kiffapi.Proposal {
	p := kiffapi.Proposal{ID: in.Key, Domain: Domain, EntityID: in.Key, EntityType: "ToolCall",
		ActionName: t.Name, ActorID: a.me.AgentID, Parameters: params, TrialDraft: a.trialDraft}
	if t.needsPermit() {
		p.Permit = &kiffapi.PermitRequest{Audience: t.Audience, Tool: t.UpstreamTool, Arguments: body}
	}
	return p
}

func (a *agent) first(ctx context.Context, t Tool, in invocation.Invocation, params map[string]any) (json.RawMessage, error) {
	from := []invocation.State{invocation.StateDeciding}
	if err := a.g.API.RequestToolCall(ctx, a.key, Domain, in.Key, a.me.AgentID, in.CreatedAt); err != nil {
		// Fail closed: KIFF may or may not have recorded the call, and
		// nothing is sent without a decision.
		return a.finish(ctx, in.Key, from, invocation.StateFailed, invocation.Update{
			Message: "KIFF could not record this call, so it was not sent to the tool. Retry with a new " + OperationArg + ".",
			Reasons: []string{ReasonNotRecorded},
		}, err)
	}
	// A call in another unit than its Card counts is never decided on the
	// Card: KIFF records it as blocked, for the owner's record, and it is
	// refused here whatever KIFF answers.
	if reason, msg, unit := a.checkUnit(ctx, t, params, in.Arguments); reason != "" {
		blocked := maps.Clone(params)
		blocked[UnitParam] = unit
		if _, err := a.g.API.Decide(ctx, a.key, a.proposal(t, in, blocked, forwardArgs(t, in))); err != nil && a.g.Logger != nil {
			a.g.Logger.WarnContext(ctx, "gateway: a call refused for its unit was not recorded by KIFF",
				slog.String("invocation", in.Key), slog.String("err", err.Error()))
		}
		return a.finish(ctx, in.Key, from, invocation.StateRefused, invocation.Update{Message: msg, Reasons: []string{reason}}, nil)
	}
	return a.decided(ctx, t, in, params, from)
}

// decided asks apps/api and acts on the answer. Asking again for the
// same proposal never draws twice.
func (a *agent) decided(ctx context.Context, t Tool, in invocation.Invocation, params map[string]any, from []invocation.State) (json.RawMessage, error) {
	body := forwardArgs(t, in)
	d, err := a.g.API.Decide(ctx, a.key, a.proposal(t, in, params, body))
	if err != nil {
		if in.State == invocation.StateHeld {
			c := callOf(in)
			c.Reasons, c.RetryAfterS = []string{ReasonDecideUnavailable}, retryKIFFDown
			return kiffResult(fmt.Sprintf("KIFF could not be asked for the owner's answer just now. Nothing was sent. "+
				"Retry the same call in about %d seconds.", retryKIFFDown), c), nil
		}
		return a.finish(ctx, in.Key, from, invocation.StateFailed, invocation.Update{
			Message: "KIFF could not be asked for a decision, so the call was not sent to the tool. Retry with a new " + OperationArg + ".",
			Reasons: []string{ReasonDecideUnavailable},
		}, err)
	}
	switch d.Outcome {
	case "allowed":
		return a.forward(ctx, t, in, from, body, d)
	case "approved_no_room":
		// Keep the operation held, with its original arguments and binding.
		// No forwarding claim, permit, result or terminal refusal is made.
		// A later identical retry must ask the API under its Card locks again.
		u := invocation.Update{Message: d.Message, Reasons: d.Reasons}
		if d.Exception != nil {
			u.ExceptionID, u.ReviewURL = d.Exception.ID, a.g.absolute(d.Exception.ReviewURL)
			u.HoldExpiresAt = d.Exception.ExpiresAt
		}
		if _, err := a.g.Store.Transition(ctx, in.Key, from, invocation.StateHeld, u); err != nil {
			return nil, err
		}
		return a.replay(ctx, in.Key)
	case "owner_required":
		if in.State == invocation.StateHeld {
			return a.heldResult(ctx, in), nil
		}
		u := invocation.Update{Message: "Held for the owner's approval."}
		if d.Exception != nil {
			u.ExceptionID, u.ReviewURL = d.Exception.ID, a.g.absolute(d.Exception.ReviewURL)
			u.HoldExpiresAt = d.Exception.ExpiresAt
		}
		if _, err := a.g.Store.Transition(ctx, in.Key, from, invocation.StateHeld, u); err != nil {
			return nil, err
		}
		return a.replay(ctx, in.Key)
	default:
		return a.finish(ctx, in.Key, from, invocation.StateRefused, invocation.Update{
			Message: refusalMessage(d.Outcome, d.Reasons, d.Message), Reasons: d.Reasons}, nil)
	}
}

func (a *agent) forward(ctx context.Context, t Tool, in invocation.Invocation, from []invocation.State, body json.RawMessage, d kiffapi.Decision) (json.RawMessage, error) {
	permit := d.Permit
	if t.needsPermit() && permit == "" {
		// Two retries of an approved hold can both ask: apps/api issues the
		// operation's one permit to the first, and tells the second it was
		// already issued. The second must not end the call the first is
		// about to send. It ends it only once that permit
		// can no longer start the call.
		if slices.Contains(d.Reasons, reasonPermitAlreadyIssued) && d.PermitIssuedAt != nil &&
			a.g.now().Before(d.PermitIssuedAt.Add(permitUsable)) {
			cur, err := a.g.Store.Get(ctx, in.Key)
			if err != nil {
				return nil, err
			}
			if !slices.Contains(from, cur.State) {
				return a.replay(ctx, in.Key)
			}
			c := callOf(cur)
			c.Sent, c.Retry, c.RetryAfterS = "unknown", "same_call", retryForwarding
			return kiffResult(fmt.Sprintf("Another attempt at this call received its execution permit and is sending it. "+
				"Retry the same call in about %d seconds for its result.", retryForwarding), c), nil
		}
		// The tool would refuse the call without its permit, so it is not
		// sent at all. Nothing reached the tool.
		return a.finish(ctx, in.Key, from, invocation.StateFailed, invocation.Update{
			Message: "KIFF allowed this call but its tool's execution permit was not issued, or expired before it was sent, so it was not sent. Retry with a new " + OperationArg + ".",
			Reasons: []string{ReasonPermitMissing},
		}, nil)
	}
	won, err := a.g.Store.Transition(ctx, in.Key, from, invocation.StateForwarding, invocation.Update{})
	if err != nil {
		return nil, err
	}
	if !won {
		// Another retry claimed it; report what that one recorded.
		return a.replay(ctx, in.Key)
	}
	forwarding := []invocation.State{invocation.StateForwarding}
	credential := ""
	// In verify mode KIFF holds no credential for the tool; in relay mode
	// the stored credential is the relay's transport credential.
	if a.g.Credentials != nil && t.Mode != ModeVerify {
		c, err := a.g.Credentials.Credential(ctx, a.me.TenantID, t.Name)
		if err != nil {
			return a.finish(ctx, in.Key, forwarding, invocation.StateFailed, invocation.Update{
				Message: "The tool's stored credential could not be read, so the call was not sent. The account owner should test the connection.",
				Reasons: []string{ReasonCredentialUnreadable},
			}, err)
		}
		credential = c
	}
	// Bind a later read-only reconciliation to this exact executor and
	// transport identity before any business call leaves the gateway.
	if t.OutcomeLookupTool != "" {
		won, err := a.g.Store.BindExecution(ctx, in.Key, outcomeBinding(t, credential))
		if err != nil {
			return nil, err
		}
		if !won {
			return a.replay(ctx, in.Key)
		}
	}
	timeout := a.g.ForwardTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	fctx, cancel := context.WithTimeout(ctx, timeout)
	if permit != "" {
		fctx = mcp.WithCallHeader(fctx, PermitHeader, permit)
	}
	res, err := a.g.Forward.CallTool(fctx, t.UpstreamURL, credential, t.UpstreamTool, body)
	cancel()
	// Whatever is logged from here carries no credential.
	logged := err
	if err != nil {
		logged = errors.New(secret.Redact(err.Error(), credential))
	}
	var ce *mcp.CallError
	var rpc *mcp.RPCError
	switch {
	case err == nil:
		// The tool's answer is stored and replayed to the agent; a tool
		// that echoes its credential must not hand it to either, and it
		// cannot speak for KIFF in the dev.kiff/ _meta namespace.
		stored := withoutKIFFMeta(secret.RedactJSON(res, credential))
		te := toolError(stored)
		return a.finish(ctx, in.Key, forwarding, invocation.StateForwarded,
			invocation.Update{Result: stored, ToolError: te, ToolOutcome: toolOutcome(te), NoEffectEvidence: terminalNoEffect(t, in, stored)}, nil)
	// A transport failure is checked first: a rejection before the tool
	// ran wraps the server's error object but was never delivered.
	case errors.As(err, &ce) && !ce.Sent:
		return a.finish(ctx, in.Key, forwarding, invocation.StateFailed, invocation.Update{
			Message: "The tool could not be reached, so the call was not sent. Retry with a new " + OperationArg + ".",
			Reasons: []string{ReasonToolUnreachable},
		}, logged)
	case errors.As(err, &ce):
		return a.finish(ctx, in.Key, forwarding, invocation.StateUnknown, invocation.Update{
			Message: "Sent to the tool, but its response was lost. KIFF will not send it again; check the tool before retrying with a new " + OperationArg + ".",
			Reasons: []string{ReasonResponseLost},
		}, logged)
	case errors.As(err, &rpc):
		// The tool answered with an error: delivered, and its answer.
		msg := secret.Redact(rpc.Message, credential)
		return a.finish(ctx, in.Key, forwarding, invocation.StateForwarded, invocation.Update{
			Result: mcp.TextResult("The tool returned an error: "+msg, true), ToolError: toolErrorText(msg), ToolOutcome: "error"}, nil)
	default:
		return a.finish(ctx, in.Key, forwarding, invocation.StateUnknown, invocation.Update{
			Message: "Sent to the tool, but its response was lost. KIFF will not send it again; check the tool before retrying with a new " + OperationArg + ".",
			Reasons: []string{ReasonResponseLost},
		}, logged)
	}
}

func (a *agent) finish(ctx context.Context, key string, from []invocation.State, to invocation.State, u invocation.Update, cause error) (json.RawMessage, error) {
	if cause != nil && a.g.Logger != nil {
		a.g.Logger.WarnContext(ctx, "gateway: call ended", slog.String("invocation", key),
			slog.String("state", string(to)), slog.String("err", cause.Error()))
	}
	if _, err := a.g.Store.Transition(ctx, key, from, to, u); err != nil {
		return nil, err
	}
	return a.replay(ctx, key)
}

// retry answers a call that is a retry of an invocation.
func (a *agent) retry(ctx context.Context, t Tool, in invocation.Invocation, params map[string]any) (json.RawMessage, error) {
	if in.State == invocation.StateDeciding || in.State == invocation.StateForwarding {
		n, err := a.g.Store.RecoverStale(ctx, in.Key, a.g.now().Add(-a.g.lease()))
		if err != nil {
			return nil, err
		}
		if n > 0 {
			return a.replay(ctx, in.Key)
		}
	}
	if in.State == invocation.StateHeld {
		// Ask again: the answer is the owner's, once they have given it.
		return a.decided(ctx, t, in, params, []invocation.State{invocation.StateHeld})
	}
	out, cur, err := a.replayed(ctx, in.Key)
	if err != nil || in.OperationID != "" || t.ReadOnly {
		return out, err
	}
	// Without an operation id this may have been a deliberate second
	// action; say what happened so the agent can tell, in the text and
	// in the call state.
	var r map[string]any
	if json.Unmarshal(out, &r) != nil {
		return out, nil
	}
	content, _ := r["content"].([]any)
	note := fmt.Sprintf("KIFF treated this as a repeat of the identical call made at %s, so it was not done again. "+
		"For a new action, call again with a new %s.", in.CreatedAt.UTC().Format(time.RFC3339), OperationArg)
	r["content"] = append(content, map[string]any{"type": "text", "text": note})
	b, _ := json.Marshal(r)
	c := callOf(cur)
	c.Repeat = true
	return withCall(b, c), nil
}

// replay is the agent-facing result of an invocation's recorded state.
func (a *agent) replay(ctx context.Context, key string) (json.RawMessage, error) {
	out, _, err := a.replayed(ctx, key)
	return out, err
}

// replayed is replay, also returning the invocation it read. Every result
// carries the call state at MetaKey, a forwarded tool's own too.
func (a *agent) replayed(ctx context.Context, key string) (json.RawMessage, invocation.Invocation, error) {
	in, err := a.g.Store.Get(ctx, key)
	if err != nil {
		return nil, in, err
	}
	switch in.State {
	case invocation.StateForwarded:
		a.reconcileEffects(ctx, in)
		if len(in.Result) == 0 {
			return kiffResult("This call was sent to the tool at "+in.UpdatedAt.UTC().Format(time.RFC3339)+
				". KIFF no longer keeps the tool's result, so check the tool for it. It will not be sent again.", callOf(in)), in, nil
		}
		// The tool's own result, with KIFF's key for the call and its
		// state, so the agent's runtime can file it (kiff/tafs#167).
		return withCall(in.Result, callOf(in)), in, nil
	case invocation.StateHeld:
		return a.heldResult(ctx, in), in, nil
	case invocation.StateDeciding:
		return kiffResult(fmt.Sprintf("This call is still being decided. Nothing has been sent yet; retry the same call in about %d seconds.",
			retryDeciding), callOf(in)), in, nil
	case invocation.StateForwarding:
		return kiffResult(fmt.Sprintf("This call has been sent to the tool and its result is not in yet. It will not be sent again; "+
			"retry the same call in about %d seconds.", retryForwarding), callOf(in)), in, nil
	default:
		return kiffResult(in.Message, callOf(in)), in, nil
	}
}

func (a *agent) heldResult(ctx context.Context, in invocation.Invocation) json.RawMessage {
	for _, reason := range in.Reasons {
		if reason == "mandate_approved_no_room" {
			msg := "Approved, but the Card has no room left. Nothing was sent. The owner can change the Card or reject this call. " +
				"Read kiff_pending to check for room; retry the same operation only after room returns."
			return kiffResult(msg, callOf(in))
		}
	}
	a.offerInChat(ctx, in)
	msg := "Waiting for the owner's approval: this call is outside the agent's Card. Nothing has been sent to the tool. "
	if in.ReviewURL != "" {
		msg += "The owner can answer at " + in.ReviewURL + ". "
	}
	msg += "Retry the same call later to get their answer"
	if in.OperationID != "" {
		msg += " (same " + OperationArg + ")"
	}
	msg = fmt.Sprintf("%s; checking again in about %d seconds is enough.", msg, retryHeld)
	if in.HoldExpiresAt != nil {
		msg += " If the owner has not answered by " + in.HoldExpiresAt.UTC().Format(time.RFC3339) +
			", the call is refused and nothing is sent."
	}
	return kiffResult(msg, callOf(in))
}

func (g *Gateway) absolute(path string) string {
	if path == "" || strings.Contains(path, "://") || g.AppURL == "" {
		return path
	}
	return strings.TrimRight(g.AppURL, "/") + path
}

// PostgresCatalog reads gateway.tools.
type PostgresCatalog struct{ Pool *pgxpool.Pool }

func (c PostgresCatalog) Tools(ctx context.Context, tenantID string) ([]Tool, error) {
	rows, err := c.Pool.Query(ctx, `SELECT name, title, description, input_schema, upstream_url, upstream_tool, amount_arg, amount_unit, amount_scale, idempotency_arg, read_only,
		mode, audience, outcome_contract, outcome_lookup_tool, created_at, resource_arg, amount_path, resource_path, fixed_args, unit_lookup
		FROM gateway.tools WHERE tenant_id = $1 ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tool
	for rows.Next() {
		var t Tool
		var schema, fixed, unit []byte
		if err := rows.Scan(&t.Name, &t.Title, &t.Description, &schema, &t.UpstreamURL, &t.UpstreamTool, &t.AmountArg, &t.AmountUnit, &t.AmountScale, &t.IdempotencyArg, &t.ReadOnly,
			&t.Mode, &t.Audience, &t.OutcomeContract, &t.OutcomeLookupTool, &t.ConnectionCreatedAt, &t.ResourceArg, &t.AmountPath, &t.ResourcePath, &fixed, &unit); err != nil {
			return nil, err
		}
		if len(unit) > 0 {
			var l UnitLookup
			if err := json.Unmarshal(unit, &l); err != nil {
				return nil, fmt.Errorf("gateway: unit_lookup of %s: %w", t.Name, err)
			}
			t.UnitLookup = &l
		}
		t.InputSchema = schema
		t.FixedArgs = fixed
		out = append(out, t)
	}
	return out, rows.Err()
}
