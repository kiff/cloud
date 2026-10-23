package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// PendingTool is the gateway's own read-only tool for calls an agent may
// have lost track of: its held calls, with the owner's answer once
// given, its calls being sent, and its calls whose outcome is unknown, each
// with the exact call that collects it. Agents lose their memory between
// sessions; this state lives in the gateway, and the agent reads it at the
// start. Answered by the gateway: never forwarded, recorded or decided,
// and it sends nothing. Collecting a call is still the agent's own retry.
const PendingTool = "kiff_pending"

const (
	// pendingLimit is how many calls one read lists, newest first. More
	// are reached with next_cursor, so none is left out.
	pendingLimit = 50
	// holdsPerRead is the API's limit on ids per GET /v1/me/holds.
	holdsPerRead = 20
	// pendingSince is how far back it looks: the longest hold.
	pendingSince = 7 * 24 * time.Hour
)

const pendingToolDescription = "Lists your calls that KIFF holds for the owner's approval, with the owner's answer once given, " +
	"your calls being sent, and your calls whose outcome is unknown, each with the call that collects it, newest first. " +
	"When there are more, pass the next_cursor it returns as cursor to read on. " +
	"Reading it changes nothing and sends nothing. Call it when you start work, or to see whether the owner has answered."

const pendingToolSchema = `{"type":"object","properties":{"cursor":{"type":"string","maxLength":200,` +
	`"description":"Optional. The next_cursor from an earlier read, to list the calls after it."}}}`

func pendingToolListing() mcp.Tool {
	title := "Show my pending KIFF calls"
	return mcp.Tool{Name: PendingTool, Title: title, Description: pendingToolDescription,
		InputSchema: json.RawMessage(pendingToolSchema),
		Annotations: &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true}}
}

// pendingCursor is where a listing goes on, as kiff_pending hands it out:
// the last call's creation time and key, opaque to the agent.
func pendingCursor(in invocation.Invocation) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%s", in.CreatedAt.UnixNano(), in.Key)))
}

// pendingPage reads kiff_pending's arguments: an optional cursor.
func pendingPage(raw json.RawMessage) (*invocation.PendingPage, error) {
	var p struct {
		Cursor string `json:"cursor"`
	}
	if len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &p) != nil {
		return nil, invalid(PendingTool + ` takes {"cursor": "<next_cursor>"} or nothing`)
	}
	if p.Cursor == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(p.Cursor)
	if err != nil || len(p.Cursor) > 200 {
		return nil, invalid("cursor is not one " + PendingTool + " returned")
	}
	at, key, ok := strings.Cut(string(b), "|")
	ns, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || !strings.HasPrefix(key, "tc-") {
		return nil, invalid("cursor is not one " + PendingTool + " returned")
	}
	return &invocation.PendingPage{At: time.Unix(0, ns).UTC(), Key: key}, nil
}

// PendingCall is one entry of kiff_pending's structured result.
type PendingCall struct {
	Tool string `json:"tool"`
	// Arguments are the call's own, exactly as the agent sent them and
	// KIFF stored them, so sending them again collects the call; absent
	// once the call has finished. KIFF's own idempotency value is never
	// among them: it is added only to what the tool receives. Nor are the
	// connection's fixed arguments: KIFF adds them again, and an
	// agent may not set them.
	Arguments map[string]any `json:"arguments,omitempty"`
	// OperationID is what to pass as kiff_operation_id to collect the
	// call: the agent's own, or KIFF's key for a call made without one.
	OperationID string `json:"kiff_operation_id"`
	// State: held, sending or unknown.
	State string `json:"state"`
	// Answer, for a held call: waiting, approved, refused, withdrawn (the
	// agent's own run withdrew it), ended (the hold ran out, or its Card or
	// an approval was withdrawn), or unavailable when KIFF could not say.
	Answer        string     `json:"answer,omitempty"`
	ExceptionID   string     `json:"exception_id,omitempty"`
	ReviewURL     string     `json:"review_url,omitempty"`
	HoldExpiresAt *time.Time `json:"hold_expires_at,omitempty"`
	// NextStep and Note are, for a refused call, what the owner asked the
	// agent to do instead, e.g. return_first. The note is the
	// owner's own words.
	NextStep  string    `json:"next_step,omitempty"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Next is what to do, in words.
	Next string `json:"next"`
}

// pendingResult answers kiff_pending: one page of the agent's pending
// calls, and next_cursor when there are more.
func (a *agent) pendingResult(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	page, err := pendingPage(raw)
	if err != nil {
		return nil, err
	}
	ins, err := a.g.Store.Pending(ctx, a.credentialID, a.g.now().Add(-pendingSince), page, pendingLimit+1)
	if err != nil {
		return nil, err
	}
	next := ""
	if len(ins) > pendingLimit {
		ins = ins[:pendingLimit]
		next = pendingCursor(ins[pendingLimit-1])
	}
	tools, err := a.g.Catalog.Tools(ctx, a.me.TenantID)
	if err != nil {
		return nil, err
	}
	byName := map[string]Tool{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	answers, answered := a.ownerAnswers(ctx, ins)
	calls := make([]PendingCall, 0, len(ins))
	for _, in := range ins {
		calls = append(calls, pendingCall(in, byName[in.Tool], answers, answered))
	}
	var b strings.Builder
	switch {
	case len(calls) == 0 && page != nil:
		b.WriteString("No more pending calls.")
	case len(calls) == 0:
		b.WriteString("You have no calls waiting on the owner, being sent, or with an unknown outcome.")
	default:
		fmt.Fprintf(&b, "Your pending KIFF calls, newest first (%d):", len(calls))
		for _, c := range calls {
			fmt.Fprintf(&b, "\n- %s (%s, %s=%s, %s): %s", c.Tool, c.State, OperationArg, c.OperationID,
				c.CreatedAt.UTC().Format(time.RFC3339), c.Next)
		}
	}
	if next != "" {
		fmt.Fprintf(&b, "\nThere are more: read %s again with cursor %s.", PendingTool, next)
	}
	res := mcp.TextResult(b.String(), false)
	var m map[string]any
	if json.Unmarshal(res, &m) == nil {
		sc := map[string]any{"calls": calls}
		if next != "" {
			sc["next_cursor"] = next
		}
		m["structuredContent"] = sc
		res, _ = json.Marshal(m)
	}
	return res, nil
}

// ownerAnswers reads where the held calls stand, by exception id. answered
// is false when KIFF could not be asked: every held call then says so.
func (a *agent) ownerAnswers(ctx context.Context, ins []invocation.Invocation) (map[string]kiffapi.Hold, bool) {
	var ids []string
	for _, in := range ins {
		if in.State == invocation.StateHeld && in.ExceptionID != "" {
			ids = append(ids, in.ExceptionID)
		}
	}
	out := map[string]kiffapi.Hold{}
	if len(ids) == 0 {
		return out, true
	}
	// The API reads at most holdsPerRead ids at a time.
	for i := 0; i < len(ids); i += holdsPerRead {
		holds, err := a.g.API.Holds(ctx, a.key, ids[i:min(i+holdsPerRead, len(ids))])
		if err != nil {
			return out, false
		}
		for _, h := range holds {
			out[h.ExceptionID] = h
		}
	}
	return out, true
}

func pendingCall(in invocation.Invocation, t Tool, answers map[string]kiffapi.Hold, answered bool) PendingCall {
	op := in.OperationID
	if op == "" {
		// Begin takes a call's own key as its operation id, so a
		// call made without one is still collected after the window.
		op = in.Key
	}
	c := PendingCall{Tool: in.Tool, OperationID: op, CreatedAt: in.CreatedAt}
	if in.State != invocation.StateUnknown {
		var args map[string]any
		dec := json.NewDecoder(strings.NewReader(string(in.Arguments)))
		dec.UseNumber()
		// Listed exactly as stored and hashed: leaving out
		// an idempotency value the agent sent itself would make the listed
		// call a different one and its retry a reused id. The connection's
		// fixed arguments are the exception: the retry adds them
		// back, so it is still the stored call, and sending them is refused.
		if dec.Decode(&args) == nil {
			if fixed, err := t.fixedArgs(); err == nil {
				for k := range fixed {
					delete(args, k)
				}
			}
			if len(args) > 0 {
				c.Arguments = args
			}
		}
	}
	again := fmt.Sprintf("Call %s again with these arguments and %s %s", in.Tool, OperationArg, op)
	switch in.State {
	case invocation.StateHeld:
		c.State, c.ExceptionID, c.ReviewURL, c.HoldExpiresAt = "held", in.ExceptionID, in.ReviewURL, utc(in.HoldExpiresAt)
		h, known := answers[in.ExceptionID]
		switch {
		case !answered || (known && h.Status == "unavailable"):
			c.Answer, c.Next = "unavailable", "KIFF could not be asked for the owner's answer just now. Nothing has been sent; read "+PendingTool+" again later."
		case !known || h.Status == "held":
			c.Answer, c.Next = "waiting", "Waiting for the owner's answer. Nothing has been sent."
		case h.Status == "approved_no_room":
			c.Answer, c.Next = "approved_no_room", "Approved, but the Card has no room left. Nothing was sent. The owner can change the Card or reject; read "+PendingTool+" again later. Do not collect until room returns."
		case h.Status == "approved" || h.Status == "changed":
			c.Answer, c.Next = "approved", "The owner answered. "+again+" to get the result; it is sent once."
		case h.Status == "rejected":
			c.Answer, c.Next = "refused", "The owner refused it. Nothing was sent. Do not call it again unless the user asks."
			if h.NextStep != "" || h.Note != "" {
				c.NextStep, c.Note = h.NextStep, h.Note
				c.Next = "The owner refused it and said what to do instead"
				if h.NextStep != "" {
					c.Next += ": next step " + h.NextStep
				}
				if h.Note != "" {
					c.Next += ", with the note " + strconv.Quote(h.Note)
				}
				c.Next += ". Nothing was sent. Do that instead; do not call this again unless the user asks."
			}
		case h.Status == "consumed":
			// The approval was used by a retry already under way.
			c.Answer, c.Next = "approved", "The approval has been used. "+again+" to see that call's recorded outcome; it is not sent twice."
		case h.Withdrawn:
			// The agent's own run stopped first: not the owner's
			// answer, so the item is still to do.
			c.Answer, c.Next = "withdrawn", "Your run withdrew this call when it stopped. Nothing was sent, and the item is still to do: if it still needs doing, make the call again with a new "+OperationArg+"."
		default: // expired, invalidated
			c.Answer, c.Next = "ended", "The hold ended without approval. Nothing was sent."
		}
	case invocation.StateForwarding:
		c.State, c.Next = "sending", "Sent to the tool; its result is not in yet. "+again+" in a few seconds to get it; it is not sent twice."
	default: // unknown
		c.State, c.Next = "unknown", in.Message
	}
	return c
}
