package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// Check first (kiff/tafs#498). When a money call ends without a known
// outcome (its answer was lost, or the vendor answered an error with
// nothing proving no effect), and its profile names a status read, KIFF
// looks for it at the vendor before anyone is asked "Did this happen?".
// The read is made as the agent, through its own Card, on the same
// connection, like a verify read. A record carrying this call's operation
// id, which the connection wrote at the profile's marker, settles it: the
// call went through, it's recorded as done with what the read found, and
// nothing is asked. Anything else (no such record, a failed or refused
// read, a timeout) settles nothing: the call ends as it would have, and
// is asked as before. Not finding it is never proof it didn't happen.

// StatusRead is a profile operation's status read: the read that finds
// the operation at the vendor without sending it again.
type StatusRead struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args"`
	// Match is the result path holding the operation id. A path through a
	// list (data[0].metadata.kiff_op) looks at every element, never only
	// the first: a record made meanwhile in the vendor's dashboard isn't
	// this call's.
	Match string `json:"match"`
}

// StatusWait is how long KIFF waits before the status read, so a write
// that lands a moment late is found; StatusTimeout bounds the read.
var (
	StatusWait    = 2 * time.Second
	StatusTimeout = 10 * time.Second
)

// FoundMessage is a call's message once its status read found it.
const FoundMessage = "Its outcome wasn't known, so KIFF looked for it at the vendor and found it by its operation id: it went through. It was not sent again."

// statusEvidence is what a status read found, as the call keeps it.
type statusEvidence struct {
	Read string `json:"read"`
	// Match is the path that held the operation id.
	Match string `json:"match"`
	// Record is the vendor's id for the record found, when it has one.
	Record string `json:"record,omitempty"`
	// Was is how the call had ended: "unknown" (its answer lost) or
	// "error" (the vendor's error).
	Was      string `json:"was"`
	At       string `json:"at"`
	Revision *int64 `json:"profile_revision,omitempty"`
}

// checks reports whether a call of t that ended without a known outcome
// is looked for at the vendor first. A refund thune governs isn't: thune
// finds its own by its marker (refund.go).
func (a *agent) checks(t Tool) bool {
	return t.Status != nil && t.Money && !t.ReadOnly && !a.governsRefund(t)
}

// checkFirst runs t's status read for in, whose outcome isn't known (was
// is "unknown" or "error"), and answers what it found when a record
// carries in's operation id, else nil.
func (a *agent) checkFirst(ctx context.Context, t Tool, in invocation.Invocation, was string) json.RawMessage {
	s := t.Status
	read, err := a.tool(ctx, s.Tool)
	if err != nil || !read.ReadOnly || read.UpstreamURL != t.UpstreamURL || read.Mode != t.Mode || read.Audience != t.Audience ||
		read.AmountLookup != nil || read.UnitLookup != nil || read.Verify != nil {
		return nil
	}
	var args map[string]any
	dec := json.NewDecoder(strings.NewReader(string(in.Arguments)))
	dec.UseNumber()
	_ = dec.Decode(&args)
	if fixed, err := t.withFixed(args); err == nil {
		args = fixed
	}
	readArgs := map[string]any{}
	for k, ref := range s.Args {
		// No answer came back to read from: only the call's arguments.
		val, ok := resolveRef(ref, args, nil)
		if !ok {
			return nil
		}
		readArgs[k] = val
	}
	body, _ := json.Marshal(readArgs)
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(StatusWait):
	}
	rctx, cancel := context.WithTimeout(ctx, StatusTimeout)
	out, _, err := a.callTool(rctx, read.Name, body)
	cancel()
	if err != nil || readFailed(out) != "" {
		return nil
	}
	rec, found := markedRecord(resultDocs(out), s.Match, in.Key)
	if !found {
		return nil
	}
	e := statusEvidence{Read: read.Name, Match: s.Match, Was: was, At: a.g.now().UTC().Format(time.RFC3339), Revision: in.ProfileRevision}
	if id, ok := valueAt(rec, "id"); ok {
		if text, isText := id.(string); isText {
			e.Record = text
		}
	}
	raw, _ := json.Marshal(e)
	return raw
}

// lostCheck is checkFirst for a call whose answer was lost, or nil when
// t isn't checked.
func (a *agent) lostCheck(ctx context.Context, t Tool, in invocation.Invocation) json.RawMessage {
	if !a.checks(t) {
		return nil
	}
	return a.checkFirst(ctx, t, in, "unknown")
}

// found ends a call its status read found at the vendor: done, with the
// evidence, and nothing asked.
func (a *agent) found(ctx context.Context, key string, from []invocation.State, evidence json.RawMessage, toolError string) (json.RawMessage, error) {
	return a.finish(ctx, key, from, invocation.StateForwarded, invocation.Update{
		Result: mcp.TextResult(FoundMessage, false), Message: FoundMessage, ToolOutcome: "done", ToolError: toolError, StatusEvidence: evidence,
	}, nil)
}

// markedRecord is the record in docs whose value at match is op. A match
// through a list (data[0].metadata.kiff_op) looks at every element of the
// list; one that isn't answers its single value. found is false when no
// record carries op, and always for an empty op: an empty marker is
// nobody's.
func markedRecord(docs []any, match, op string) (rec any, found bool) {
	if op == "" {
		return nil, false
	}
	i, j := strings.Index(match, "["), strings.Index(match, "]")
	if i <= 0 || j < i {
		for _, d := range docs {
			if v, ok := valueAt(d, match); ok && fmt.Sprint(v) == op {
				return d, true
			}
		}
		return nil, false
	}
	list, rest := match[:i], strings.TrimPrefix(match[j+1:], ".")
	if rest == "" {
		return nil, false
	}
	for _, d := range docs {
		v, ok := valueAt(d, list)
		elems, isList := v.([]any)
		if !ok || !isList {
			continue
		}
		for _, el := range elems {
			if m, has := valueAt(el, rest); has && fmt.Sprint(m) == op {
				return el, true
			}
		}
	}
	return nil, false
}
