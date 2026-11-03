package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
)

// After a write the vendor answered 2xx, a profile's verify read reads it
// back: the amount the vendor used, or a field that shows it took effect
// (connection profiles: the verify read). It's made as the agent, through its own
// Card, like any read. What it found is kept on the call. A difference, a
// failed read or a missing path flags the call and pauses the operation:
// nothing more is sent through it until the owner resumes it. It detects a
// mismatch after the fact; it can't undo the first one.

// ReasonOperationPaused refuses a call to an operation its verify read
// paused.
const ReasonOperationPaused = "operation_paused"

// VerifyRead is a profile operation's verify read.
type VerifyRead struct {
	Tool       string            `json:"tool"`
	Args       map[string]string `json:"args"`
	AmountPath string            `json:"amount_path"`
	DoneWhen   *struct {
		Path   string `json:"path"`
		Equals string `json:"equals"`
	} `json:"done_when"`
}

// effectSurcharger is the part of apps/api that counts a vendor's larger
// figure on the Card.
type effectSurcharger interface {
	SurchargeEffects(ctx context.Context, key, proposal string) error
}

// OperationPauser pauses one operation of an account.
type OperationPauser interface {
	PauseOperation(ctx context.Context, tenantID, tool, reason string) error
}

// PauseOperation pauses tool until the owner resumes it. A paused one
// keeps its first reason.
func (c PostgresCatalog) PauseOperation(ctx context.Context, tenantID, tool, reason string) error {
	_, err := c.Pool.Exec(ctx, `UPDATE gateway.tools SET paused_reason = $3, paused_at = now()
		WHERE tenant_id = $1 AND name = $2 AND paused_reason = ''`, tenantID, tool, reason)
	return err
}

// profileOp is what the profile says of tool that the gateway acts on: its
// verify read, or nil, and whether it's on probation (a money operation
// no test call proved).
func profileOp(profile []byte, tool string) (*VerifyRead, bool, error) {
	var p struct {
		TestProven bool `json:"test_proven"`
		Operations []struct {
			Tool       string      `json:"tool"`
			Effect     string      `json:"effect"`
			VerifyRead *VerifyRead `json:"verify_read"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(profile, &p); err != nil {
		return nil, false, err
	}
	for _, o := range p.Operations {
		if o.Tool == tool {
			money := o.Effect == "records_money" || o.Effect == "moves_money"
			return o.VerifyRead, money && !p.TestProven, nil
		}
	}
	return nil, false, nil
}

// verifyEvidence is what a verify read found, as the call keeps it.
type verifyEvidence struct {
	Check  string `json:"check"`  // amount or done
	Result string `json:"result"` // match, mismatch or unreadable
	Read   string `json:"read"`
	// AmountArg is the Card parameter the amount was decided under.
	AmountArg string `json:"amount_argument,omitempty"`
	Decided   *int64 `json:"decided_amount,omitempty"`
	Vendor    *int64 `json:"vendor_amount,omitempty"`
	Why       string `json:"why,omitempty"`
	At        string `json:"at"`
	Revision  *int64 `json:"profile_revision,omitempty"`
}

// verify runs t's verify read for in, which the tool answered with result,
// keeps what it found, and pauses t on anything but a match. It answers a
// line for the agent when the call was flagged, else "".
func (a *agent) verify(ctx context.Context, t Tool, in invocation.Invocation, result json.RawMessage) string {
	v := t.Verify
	e := verifyEvidence{Check: "done", Read: v.Tool, At: a.g.now().UTC().Format(time.RFC3339), Revision: in.ProfileRevision}
	if v.AmountPath != "" {
		e.Check, e.AmountArg = "amount", t.AmountArg
	}
	e.Result, e.Why = a.verifyResult(ctx, t, in, result, &e)
	raw, _ := json.Marshal(e)
	if err := a.g.Store.RecordVerify(ctx, in.Key, raw); err != nil && a.g.Logger != nil {
		a.g.Logger.WarnContext(ctx, "gateway: a verify read was not recorded", slog.String("invocation", in.Key), slog.String("err", err.Error()))
	}
	if e.Result == "match" {
		return ""
	}
	if e.Vendor != nil && e.Decided != nil && *e.Vendor > *e.Decided {
		// The Card counts the larger figure, from this recorded read.
		if s, ok := a.g.API.(effectSurcharger); ok {
			if err := s.SurchargeEffects(ctx, a.key, in.Key); err != nil && a.g.Logger != nil {
				a.g.Logger.WarnContext(ctx, "gateway: a surcharge was not recorded", slog.String("invocation", in.Key), slog.String("err", err.Error()))
			}
		}
	}
	if a.g.Operations != nil {
		if err := a.g.Operations.PauseOperation(ctx, a.me.TenantID, t.Name, e.Why); err != nil && a.g.Logger != nil {
			a.g.Logger.WarnContext(ctx, "gateway: an operation was not paused", slog.String("tool", t.Name), slog.String("err", err.Error()))
		}
	}
	return "KIFF read this back and it didn't check out: " + e.Why + ". This call was sent and is flagged for the owner; " +
		t.Name + " is paused, and nothing more is sent through it until the owner resumes it."
}

func (a *agent) verifyResult(ctx context.Context, t Tool, in invocation.Invocation, result json.RawMessage, e *verifyEvidence) (string, string) {
	v := t.Verify
	read, err := a.tool(ctx, v.Tool)
	if err != nil || !read.ReadOnly || read.UpstreamURL != t.UpstreamURL || read.Mode != t.Mode || read.Audience != t.Audience ||
		read.AmountLookup != nil || read.UnitLookup != nil || read.Verify != nil {
		return "unreadable", "its verify read, " + v.Tool + ", isn't a plain read on the same connection"
	}
	// The call's arguments as it was decided: numbers kept exact, with the
	// connection's fixed arguments.
	var args map[string]any
	dec := json.NewDecoder(strings.NewReader(string(in.Arguments)))
	dec.UseNumber()
	_ = dec.Decode(&args)
	if fixed, err := t.withFixed(args); err == nil {
		args = fixed
	}
	docs := resultDocs(result)
	readArgs := map[string]any{}
	for k, ref := range v.Args {
		val, ok := resolveRef(ref, args, docs)
		if !ok {
			return "unreadable", "the call or its answer has no " + ref + " for the verify read"
		}
		readArgs[k] = val
	}
	body, _ := json.Marshal(readArgs)
	out, _, err := a.callTool(ctx, read.Name, body)
	if err != nil {
		return "unreadable", "the verify read failed"
	}
	if why := readFailed(out); why != "" {
		return "unreadable", why
	}
	got := resultDocs(out)
	if v.DoneWhen != nil {
		want, ok := resolveRef(v.DoneWhen.Equals, args, docs)
		val, found := firstIn(got, v.DoneWhen.Path)
		switch {
		case !ok || !found:
			return "unreadable", "the verify read's answer doesn't have " + v.DoneWhen.Path
		case fmt.Sprint(val) != fmt.Sprint(want):
			return "mismatch", v.DoneWhen.Path + " isn't " + fmt.Sprint(want)
		}
		return "match", ""
	}
	params, err := t.params(args)
	decided, isInt := params[t.AmountArg].(int64)
	if err != nil || !isInt {
		return "unreadable", "the amount KIFF decided couldn't be read again"
	}
	e.Decided = &decided
	val, found := firstIn(got, v.AmountPath)
	if !found {
		return "unreadable", "the verify read's answer doesn't have " + v.AmountPath
	}
	var vendor int64
	if s, isText := val.(string); isText && t.AmountFormat == FormatDecimal {
		if vendor, err = MinorUnits(s, t.AmountScale); err != nil {
			return "unreadable", "the vendor's amount " + err.Error()
		}
	} else if n, ok := wholeAmount(val); ok && t.AmountFormat != FormatDecimal {
		vendor = n
	} else {
		return "unreadable", "the vendor's amount isn't in the form the profile says"
	}
	e.Vendor = &vendor
	if vendor != decided {
		return "mismatch", "the vendor used " + strconv.FormatInt(vendor, 10) + ", KIFF decided " + strconv.FormatInt(decided, 10)
	}
	return "match", ""
}

// readFailed says why a read's result isn't a sent read's answer, or "".
func readFailed(raw json.RawMessage) string {
	var res struct {
		IsError bool                       `json:"isError"`
		Meta    map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return "the verify read's answer couldn't be read"
	}
	if m, ok := res.Meta[MetaKey]; ok {
		var c Call
		if json.Unmarshal(m, &c) != nil || c.State != string(invocation.StateForwarded) {
			return "the verify read wasn't made"
		}
	}
	if res.IsError {
		return "the verify read returned an error"
	}
	return ""
}

// resultDocs are the JSON objects a tool's result carries: its structured
// content, then each text part that is JSON.
func resultDocs(raw json.RawMessage) []any {
	var res struct {
		Structured map[string]any          `json:"structuredContent"`
		Content    []struct{ Text string } `json:"content"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&res) != nil {
		return nil
	}
	docs := []any{}
	if res.Structured != nil {
		docs = append(docs, res.Structured)
	}
	for _, c := range res.Content {
		d := json.NewDecoder(strings.NewReader(c.Text))
		d.UseNumber()
		var v any
		if d.Decode(&v) == nil {
			docs = append(docs, v)
		}
	}
	return docs
}

// resolveRef is a verify read's argument: $args.<path> from the call,
// $result.<path> from the tool's answer, or the text as written.
func resolveRef(ref string, args map[string]any, result []any) (any, bool) {
	if p, ok := strings.CutPrefix(ref, "$args."); ok {
		return valueAt(args, p)
	}
	if p, ok := strings.CutPrefix(ref, "$result."); ok {
		return firstIn(result, p)
	}
	return ref, true
}

func firstIn(docs []any, path string) (any, bool) {
	for _, d := range docs {
		if v, ok := valueAt(d, path); ok {
			return v, true
		}
	}
	return nil, false
}

var pathPart = regexp.MustCompile(`^([A-Za-z0-9_]+)((\[[0-9]+\])*)$`)
var indexPart = regexp.MustCompile(`\[([0-9]+)\]`)

// valueAt reads a dotted path with [n] array indexes: data[0].amount.
func valueAt(doc any, path string) (any, bool) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		m := pathPart.FindStringSubmatch(seg)
		if m == nil {
			return nil, false
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[m[1]]; !ok {
			return nil, false
		}
		for _, ix := range indexPart.FindAllStringSubmatch(m[2], -1) {
			arr, ok := cur.([]any)
			i, _ := strconv.Atoi(ix[1])
			if !ok || i >= len(arr) {
				return nil, false
			}
			cur = arr[i]
		}
	}
	return cur, true
}

// withNote adds a line of text to a tool result.
func withNote(out json.RawMessage, note string) json.RawMessage {
	var r map[string]any
	if json.Unmarshal(out, &r) != nil {
		return out
	}
	content, _ := r["content"].([]any)
	r["content"] = append(content, map[string]any{"type": "text", "text": note})
	b, err := json.Marshal(r)
	if err != nil {
		return out
	}
	return b
}

// vendorUsedMore reports whether a recorded verify read found the vendor
// used more than KIFF decided.
func vendorUsedMore(evidence json.RawMessage) bool {
	var e verifyEvidence
	return len(evidence) > 0 && json.Unmarshal(evidence, &e) == nil && e.Result == "mismatch" &&
		e.Vendor != nil && e.Decided != nil && *e.Vendor > *e.Decided
}

// measureOf is what t's amount means as a call is decided: its unit, scale
// and title. A unit lookup refuses any call in another unit than
// AmountUnit, so the unit a call is decided in is always this one.
func measureOf(t Tool) json.RawMessage {
	if t.AmountArg == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"unit": t.AmountUnit, "scale": t.AmountScale, "title": t.Title})
	return b
}
