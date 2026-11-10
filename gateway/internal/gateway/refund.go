package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/thune"
)

// A refund whose profile operation has a case block goes through thune's
// governed path (thune design v3, §4): before KIFF decides it, the gateway
// reads the charge from the processor as the agent, builds the case under
// the charge's lock, and takes the claim on the request; after it forwards,
// it records how the call ended, and the watch follows the money. A Card
// can't opt out of it. It adds no second execution record: the call is
// still one invocation, sent once through the connection's permit.

// RequestArg is the argument the agent names a refund's request in: the id
// of what asked for it in a connected app (a ticket, a message, an order
// line). KIFF keeps it with the call and never sends it to the processor.
const RequestArg = "kiff_request_id"

// Reasons a refund is refused for before KIFF decides it.
const (
	ReasonCaseUnreadable = "refund_case_unreadable"
	ReasonChargeBusy     = thune.ReasonChargeBusy
)

var requestSchema = map[string]any{
	"type":      "string",
	"maxLength": MaxResourceLength,
	"description": "The id of what asked for this refund in a connected app: the support ticket, the message or the order line. " +
		"KIFF hands one request to one taf. Without it, the refund goes to the owner.",
}

// CaseProfile is a profile operation's case block: what the operation is,
// where the call names its charge, and the reads that give the facts.
type CaseProfile struct {
	Kind string `json:"kind"`
	// ChargeArg is the dotted path of the call's argument naming the charge.
	ChargeArg string `json:"charge_arg"`
	// AccountRead says which processor account the connection is on: the
	// account comes from the processor, never from a connection's name.
	AccountRead CaseRead `json:"account_read"`
	// ChargeRead gives the charge's currency, captured amount and dispute
	// state.
	ChargeRead CaseChargeRead `json:"charge_read"`
	// RefundsRead lists the charge's refunds, every one, KIFF's or not.
	RefundsRead CaseRefundsRead `json:"refunds_read"`
	// RefundEvent, when set, is the app event that carries one refund's
	// state, which the connection hands to KIFF (refund_events.go).
	RefundEvent *CaseRefundEvent `json:"refund_event,omitempty"`
}

// CaseRefundEvent is the app event that carries a refund: its slug, where
// the refund is in the payload, and where the refund names its charge. The
// refund's id, amount, status and marker are read from it at the refunds
// read's paths.
type CaseRefundEvent struct {
	Event  string `json:"event"`
	Object string `json:"object"`
	Charge string `json:"charge"`
}

// CaseRead is one read of the same profile: a read-only tool, its
// arguments ("$args.<charge_arg>" is the call's charge, the only reference
// a case read may make; anything else is sent as written), and the path of
// the value it gives.
type CaseRead struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args,omitempty"`
	Path string            `json:"path"`
}

// CaseChargeRead reads a charge.
type CaseChargeRead struct {
	Tool     string            `json:"tool"`
	Args     map[string]string `json:"args,omitempty"`
	Currency string            `json:"currency"`
	Captured string            `json:"captured"`
	// Disputed is the path of a boolean that is true while a dispute is
	// open; DisputeStatus, a status whose open values are DisputeOpen.
	Disputed      string   `json:"disputed,omitempty"`
	DisputeStatus string   `json:"dispute_status,omitempty"`
	DisputeOpen   []string `json:"dispute_open,omitempty"`
}

// CaseRefundsRead lists a charge's refunds.
type CaseRefundsRead struct {
	Tool   string            `json:"tool"`
	Args   map[string]string `json:"args,omitempty"`
	List   string            `json:"list"`
	ID     string            `json:"id"`
	Amount string            `json:"amount"`
	Status string            `json:"status"`
	Marker string            `json:"marker"`
}

// refundStatus is the processor's refund status as thune keeps it. An
// unknown status counts as pending: it takes from the remainder until the
// processor says it ended.
func refundStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "succeeded", "success", "completed":
		return thune.RefundSucceeded
	case "failed":
		return thune.RefundFailed
	case "canceled", "cancelled":
		return thune.RefundCancelled
	default:
		return thune.RefundPending
	}
}

// caseReader reads a charge for one agent's call, through its own Card,
// as a verify read is made.
type caseReader struct {
	a *agent
	t Tool
}

func (r caseReader) read(ctx context.Context, tool string, args map[string]string, charge string) ([]any, error) {
	read, err := r.a.tool(ctx, tool)
	if err != nil || !read.ReadOnly || read.UpstreamURL != r.t.UpstreamURL || read.Mode != r.t.Mode || read.Audience != r.t.Audience {
		return nil, fmt.Errorf("the read %s isn't a read on the same connection", tool)
	}
	// A read with a lookup or a case of its own could loop back here.
	if read.AmountLookup != nil || read.UnitLookup != nil || read.Case != nil {
		return nil, fmt.Errorf("the read %s has a lookup of its own", tool)
	}
	in := map[string]any{}
	ref := "$args." + r.t.Case.ChargeArg
	for k, v := range args {
		switch {
		case v == ref:
			in[k] = charge
		case strings.HasPrefix(v, "$"):
			// apps/api refuses such a profile; never sent as written.
			return nil, fmt.Errorf("the read %s references %s, which a case read can't", tool, v)
		default:
			in[k] = v
		}
	}
	raw, _ := json.Marshal(in)
	out, _, err := r.a.callTool(ctx, read.Name, raw)
	if err != nil {
		return nil, fmt.Errorf("the read %s failed", tool)
	}
	if why := readFailed(out); why != "" {
		return nil, errors.New(why)
	}
	docs := resultDocs(out)
	if len(docs) == 0 {
		return nil, fmt.Errorf("the read %s gave nothing KIFF could read", tool)
	}
	return docs, nil
}

// account is the processor account the connection is on.
func (r caseReader) account(ctx context.Context) (string, error) {
	c := r.t.Case
	k := caseAccountKey{tenant: r.a.me.TenantID, upstream: r.t.UpstreamURL, tool: c.AccountRead.Tool}
	if v, ok := r.a.g.accounts.get(k, r.a.g.now()); ok {
		return v, nil
	}
	docs, err := r.read(ctx, c.AccountRead.Tool, c.AccountRead.Args, "")
	if err != nil {
		return "", err
	}
	v, ok := firstIn(docs, c.AccountRead.Path)
	s, isText := v.(string)
	if !ok || !isText || s == "" {
		return "", errors.New("the processor account couldn't be read")
	}
	r.a.g.accounts.put(k, s, r.a.g.now())
	return s, nil
}

// ReadCharge is thune.Reader: the charge's facts, read now.
func (r caseReader) ReadCharge(ctx context.Context, _, account, charge string) (thune.Facts, error) {
	c := r.t.Case
	f := thune.Facts{Account: account, Charge: charge}
	docs, err := r.read(ctx, c.ChargeRead.Tool, c.ChargeRead.Args, charge)
	if err != nil {
		return f, err
	}
	cur, ok := firstIn(docs, c.ChargeRead.Currency)
	if s, isText := cur.(string); ok && isText && s != "" {
		f.Currency = strings.ToLower(s)
	} else {
		return f, errors.New("the charge's currency couldn't be read")
	}
	capt, ok := firstIn(docs, c.ChargeRead.Captured)
	n, whole := wholeAmount(capt)
	if !ok || !whole {
		return f, errors.New("the charge's captured amount couldn't be read")
	}
	f.Captured = n
	switch {
	case c.ChargeRead.Disputed != "":
		// Fails closed: a charge read with no dispute flag is not read as
		// undisputed, as the currency and the captured amount aren't.
		v, ok := firstIn(docs, c.ChargeRead.Disputed)
		b, isBool := v.(bool)
		if !ok || !isBool {
			return f, errors.New("the charge's dispute state couldn't be read")
		}
		f.Disputed = b
	case c.ChargeRead.DisputeStatus != "":
		// A charge with no dispute has no dispute status: absent is none.
		if v, ok := firstIn(docs, c.ChargeRead.DisputeStatus); ok {
			f.Disputed = slices.Contains(c.ChargeRead.DisputeOpen, fmt.Sprint(v))
		}
	}
	docs, err = r.read(ctx, c.RefundsRead.Tool, c.RefundsRead.Args, charge)
	if err != nil {
		return f, err
	}
	v, ok := firstIn(docs, c.RefundsRead.List)
	list, isList := v.([]any)
	if !ok || !isList {
		return f, errors.New("the charge's refunds couldn't be read")
	}
	for _, el := range list {
		id, _ := valueAt(el, c.RefundsRead.ID)
		amt, _ := valueAt(el, c.RefundsRead.Amount)
		st, _ := valueAt(el, c.RefundsRead.Status)
		marker, _ := valueAt(el, c.RefundsRead.Marker)
		n, whole := wholeAmount(amt)
		ids, isText := id.(string)
		if !whole || !isText || ids == "" {
			return f, errors.New("a refund on the charge couldn't be read")
		}
		m, _ := marker.(string)
		f.Refunds = append(f.Refunds, thune.Refund{ID: ids, Amount: n, Status: refundStatus(fmt.Sprint(st)), Marker: m})
	}
	f.ReadAt = r.a.g.now()
	return f, nil
}

// caseAccountKey caches the processor account a connection is on: it
// doesn't change while the connection stands, and reads are limited.
type caseAccountKey struct{ tenant, upstream, tool string }

type accountCache struct {
	mu sync.Mutex
	m  map[caseAccountKey]accountEntry
}

type accountEntry struct {
	v  string
	at time.Time
}

const accountCacheFor = 10 * time.Minute

func (c *accountCache) get(k caseAccountKey, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || now.Sub(e.at) > accountCacheFor {
		return "", false
	}
	return e.v, true
}

func (c *accountCache) put(k caseAccountKey, v string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[caseAccountKey]accountEntry{}
	}
	c.m[k] = accountEntry{v: v, at: now}
}

// governsRefund reports whether t's calls go through thune.
func (a *agent) governsRefund(t Tool) bool {
	return t.Case != nil && !t.ReadOnly && a.g.Thune != nil
}

func (a *agent) gate(t Tool) *thune.Gate {
	return &thune.Gate{Store: a.g.Thune, Reader: caseReader{a: a, t: t}, Calls: ThuneCalls{Store: a.g.Store}, Lease: a.g.lease()}
}

// caseIdentity is the refund's identity and ask, from the call and the
// processor; why says what couldn't be read.
func (a *agent) caseIdentity(ctx context.Context, t Tool, in invocation.Invocation, params map[string]any) (thune.Identity, thune.Ask, string) {
	var args map[string]any
	dec := json.NewDecoder(strings.NewReader(string(in.Arguments)))
	dec.UseNumber()
	_ = dec.Decode(&args)
	charge, _ := lookupPath(args, t.Case.ChargeArg)
	cs, ok := charge.(string)
	if !ok || cs == "" || len(cs) > MaxResourceLength {
		return thune.Identity{}, thune.Ask{}, t.Case.ChargeArg + " is required and must name the charge"
	}
	req, _ := args[RequestArg].(string)
	req = strings.TrimSpace(req)
	account, err := caseReader{a: a, t: t}.account(ctx)
	if err != nil {
		return thune.Identity{}, thune.Ask{}, err.Error()
	}
	amount, _ := params[t.AmountArg].(int64)
	// The Card counts the amount in the tool's unit: a charge in another
	// currency is refused, never counted as if it were in this one.
	return thune.Identity{TenantID: a.me.TenantID, Account: account, Object: cs, Kind: thune.KindRefund, Request: req},
		thune.Ask{Amount: amount, Currency: t.AmountUnit}, ""
}

// Owner ask codes thune sends: apps/api maps each to KIFF's own words in
// the owner's language, never the caller's.
const (
	OwnerAskSecondRefund = "second_refund"
	OwnerAskNoRequestID  = "no_request_id"
)

// ownerAskCode is the code a case thune sends to the owner is asked with.
// A refund with no request is asked about that first: without a request,
// whether it's a second refund for the same need can't be told.
func ownerAskCode(c thune.Case) string {
	switch {
	case slices.Contains(c.Reasons, thune.ReasonNoRequest):
		return OwnerAskNoRequestID
	case slices.Contains(c.Reasons, thune.ReasonSecondRefund):
		return OwnerAskSecondRefund
	}
	return ""
}

// caseRefusal is the agent's words for a case thune refused.
func caseRefusal(c thune.Case, holder thune.ClaimResult) string {
	f := c.Facts
	left := strconv.FormatInt(f.Remainder(), 10)
	switch {
	case slices.Contains(c.Reasons, thune.ReasonClaimed):
		return "Not sent: another call is already handling this request (" + holder.Holder + ", " + holder.HolderExecution +
			"). Do not refund it again; check that call's outcome."
	case slices.Contains(c.Reasons, thune.ReasonOverRemainder):
		return "Not sent: the charge has " + left + " left to refund (in minor units of " + f.Currency + "), less than asked. KIFF never changes the amount; ask for what is left, or tell the owner."
	case slices.Contains(c.Reasons, thune.ReasonNothingLeft):
		return "Not sent: the charge has nothing left to refund."
	case slices.Contains(c.Reasons, thune.ReasonDisputeOpen):
		return "Not sent: a dispute on this charge is open. Refunding it now could pay twice; tell the owner."
	case slices.Contains(c.Reasons, thune.ReasonFactsChanged):
		return "Not sent: the charge changed while this refund waited for its approval (another refund, or a dispute), so the approval no longer applies. " +
			"Call again with a new " + OperationArg + " to have it decided on what the charge says now."
	case slices.Contains(c.Reasons, thune.ReasonBadAmount):
		return "Not sent: the refund's amount must be more than 0."
	default:
		return "Not sent: KIFF couldn't build this refund's case (" + strings.Join(c.Reasons, ", ") + ")."
	}
}

// refundFirst decides a governed refund's first call: its case first, then
// KIFF's decision on the authorized amount, then how it ended.
func (a *agent) refundFirst(ctx context.Context, t Tool, in invocation.Invocation, params map[string]any, from []invocation.State) (json.RawMessage, error) {
	id, ask, why := a.caseIdentity(ctx, t, in, params)
	if why != "" {
		return a.finish(ctx, in.Key, from, invocation.StateRefused, invocation.Update{
			Message: "Not sent: KIFF couldn't build this refund's case: " + why + ".", Reasons: []string{ReasonCaseUnreadable}}, nil)
	}
	g := a.gate(t)
	b, err := g.Before(ctx, id, ask, in.Key)
	if errors.Is(err, thune.ErrChargeBusy) {
		return a.finish(ctx, in.Key, from, invocation.StateRefused, invocation.Update{
			Message: "Not sent: another refund on this charge is being decided right now. Call again in a minute with a new " + OperationArg + ".",
			Reasons: []string{ReasonChargeBusy}}, nil)
	}
	if err != nil {
		return a.finish(ctx, in.Key, from, invocation.StateRefused, invocation.Update{
			Message: "Not sent: KIFF couldn't read the charge to build this refund's case: " + err.Error() + ".",
			Reasons: []string{ReasonCaseUnreadable}}, nil)
	}
	if b.Case.Verdict == thune.VerdictRefuse {
		out, err := a.finish(ctx, in.Key, from, invocation.StateRefused, invocation.Update{
			Message: caseRefusal(b.Case, b.Claim), Reasons: b.Case.Reasons}, nil)
		a.caseAfter(ctx, t, id, in.Key)
		return out, err
	}
	p := maps.Clone(params)
	if _, sealed := p[OwnerAskParam]; !sealed && b.Case.Verdict == thune.VerdictAsk {
		// A money tool KIFF holds the key to asks already (sealed_money).
		p[OwnerAskParam] = ownerAskCode(b.Case)
	}
	out, err := a.decided(ctx, t, in, p, from)
	a.caseAfter(ctx, t, id, in.Key)
	a.watchAccount(ctx, t, id)
	return out, err
}

// watchAccount reads, as this agent, the account's refunds whose money
// hasn't ended and are due a read: the watch holds no authority of its
// own, so it reads when an agent's governed call touches the account.
func (a *agent) watchAccount(ctx context.Context, t Tool, id thune.Identity) {
	if a.g.Watch == nil {
		return
	}
	if _, err := a.g.Watch.RunAccount(ctx, id.TenantID, id.Account, caseReader{a: a, t: t}); err != nil && a.g.Logger != nil {
		a.g.Logger.WarnContext(ctx, "gateway: refunds on an account weren't watched", slog.String("err", err.Error()))
	}
}

// refundRetry re-reads a held refund's charge before it is decided again:
// a changed charge voids the approval.
func (a *agent) refundRetry(ctx context.Context, t Tool, in invocation.Invocation, params map[string]any) (json.RawMessage, error) {
	held := []invocation.State{invocation.StateHeld}
	id, ask, why := a.caseIdentity(ctx, t, in, params)
	if why != "" {
		// Stays held: never decided without its case.
		return a.heldResult(ctx, in), nil
	}
	b, err := a.gate(t).Before(ctx, id, ask, in.Key)
	if err != nil {
		return a.heldResult(ctx, in), nil
	}
	if b.Void {
		out, err := a.finish(ctx, in.Key, held, invocation.StateRefused, invocation.Update{
			Message: caseRefusal(b.Case, b.Claim), Reasons: []string{thune.ReasonFactsChanged}}, nil)
		a.caseAfter(ctx, t, id, in.Key)
		return out, err
	}
	p := maps.Clone(params)
	if _, sealed := p[OwnerAskParam]; !sealed {
		if prev, err := a.g.Thune.Get(ctx, in.Key); err == nil && prev.Verdict == thune.VerdictAsk {
			p[OwnerAskParam] = ownerAskCode(caseOfAdjustment(prev))
		}
	}
	out, err := a.decided(ctx, t, in, p, held)
	a.caseAfter(ctx, t, id, in.Key)
	return out, err
}

func caseOfAdjustment(a thune.Adjustment) thune.Case {
	return thune.Case{Identity: a.Identity, Verdict: a.Verdict, Reasons: a.Reasons}
}

// caseAfter records in thune how the call ended, from the gateway's own
// record, and frees the charge.
func (a *agent) caseAfter(ctx context.Context, t Tool, id thune.Identity, key string) {
	in, err := a.g.Store.Get(ctx, key)
	if err != nil {
		a.caseLog(ctx, key, err)
		return
	}
	g := a.gate(t)
	switch in.State {
	case invocation.StateHeld:
		err = g.Held(ctx, id, key)
	case invocation.StateForwarded:
		end := thune.ExecSent
		switch {
		case in.ConnectionRefusal != "":
			// The connection refused it and says it sent nothing: not sent,
			// and the request's claim is released for another call.
			end = thune.ExecNotSent
		case in.ToolError != "":
			// The processor answered with an error. A status its profile
			// says has no effect is not sent; any other is unknown, and
			// watched, never sent again.
			end = thune.ExecUnknown
			if st, err := strconv.Atoi(statusOf(in.ToolError)); err == nil && slices.Contains(t.NoEffect, st) {
				end = thune.ExecNotSent
			}
		}
		err = g.After(ctx, id, key, end)
	case invocation.StateUnknown:
		err = g.After(ctx, id, key, thune.ExecUnknown)
	case invocation.StateFailed:
		err = g.After(ctx, id, key, thune.ExecNotSent)
	case invocation.StateRefused:
		err = g.After(ctx, id, key, thune.ExecRefused)
	default:
		// Still deciding or forwarding: a retry records it.
		return
	}
	a.caseLog(ctx, key, err)
}

// caseSending records, as the call is about to leave, that it is sending.
// A refund thune doesn't hold as allowed or held is never sent.
func (a *agent) caseSending(ctx context.Context, key string) (bool, error) {
	return a.g.Thune.SetExecution(ctx, key, []string{thune.ExecAllowed, thune.ExecHeld}, thune.ExecSending)
}

func (a *agent) caseLog(ctx context.Context, key string, err error) {
	if err != nil && a.g.Logger != nil {
		a.g.Logger.WarnContext(ctx, "gateway: a refund's record was not updated", slog.String("invocation", key), slog.String("err", err.Error()))
	}
}

// ThuneCalls settles the gateway's record of a call whose answer was lost,
// once thune found its refund by its marker: unknown becomes forwarded,
// with the refund done. It never sends anything.
type ThuneCalls struct{ Store Store }

func (c ThuneCalls) Found(ctx context.Context, key string) error {
	_, err := c.Store.Transition(ctx, key, []invocation.State{invocation.StateUnknown}, invocation.StateForwarded, invocation.Update{
		Message:     "Its answer was lost, and KIFF found the refund it made at the processor by its marker. It was not sent again.",
		ToolOutcome: "done",
	})
	return err
}

// profileCase is tool's case block and no-effect statuses, from its
// profile.
func profileCase(profile []byte, tool string) (*CaseProfile, []int, error) {
	var p struct {
		Operations []struct {
			Tool     string       `json:"tool"`
			Case     *CaseProfile `json:"case"`
			NoEffect *struct {
				Statuses []int `json:"statuses"`
			} `json:"no_effect"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(profile, &p); err != nil {
		return nil, nil, err
	}
	for _, o := range p.Operations {
		if o.Tool == tool {
			var st []int
			if o.NoEffect != nil {
				st = o.NoEffect.Statuses
			}
			if o.Case != nil && o.Case.Kind != thune.KindRefund {
				return nil, nil, fmt.Errorf("case kind %q is not handled", o.Case.Kind)
			}
			return o.Case, st, nil
		}
	}
	return nil, nil, nil
}
