package gateway

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// MetaKey is the result _meta key under which the gateway reports, on
// every result it writes itself, what happened to the call. The
// text says the same for the model; this is for the programs running the
// agent, so they can branch without parsing English.
//
// It is _meta, not structuredContent: structuredContent is the tool's own
// output and must match its outputSchema, while _meta is the protocol's
// place for metadata about the exchange. dev.kiff is reverse DNS for
// kiff.dev and not a reserved prefix. A forwarded tool's own result is
// passed through unchanged, except for the repeat note (see retry).
const MetaKey = "dev.kiff/call"

// Call is the value at MetaKey.
type Call struct {
	// State is the invocation's state: held, refused, failed, unknown,
	// deciding, forwarding, or forwarded (only on a repeat).
	State string `json:"state"`
	// Invocation is KIFF's key for the call; empty when there is none.
	Invocation  string `json:"invocation,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	// Sent is whether the tool received the call: "no", "yes" or
	// "unknown".
	Sent    string   `json:"sent"`
	Reasons []string `json:"reasons,omitempty"`
	// ExceptionID and ReviewURL name the owner's decision on a hold, and
	// HoldExpiresAt when it stops waiting for one.
	ExceptionID   string     `json:"exception_id,omitempty"`
	ReviewURL     string     `json:"review_url,omitempty"`
	HoldExpiresAt *time.Time `json:"hold_expires_at,omitempty"`
	// Retry is what a retry does:
	//   "same_call": repeat the identical call (same kiff_operation_id)
	//                to get the answer;
	//   "new_call":  this call is over; a new attempt needs a new
	//                kiff_operation_id (after the next step, if any);
	//   "none":      do not retry this action.
	Retry string `json:"retry"`
	// RetryAfterS is how long to wait before retrying, in seconds.
	RetryAfterS int `json:"retry_after_s,omitempty"`
	// Repeat is set when an identical call without an operation id was
	// answered with an earlier call's result instead of being done again.
	Repeat bool `json:"repeat,omitempty"`
	// Trial is KIFF's decision on a trial's write, which was not sent
	// (kiff/tafs#107): only on a call made with KIFF-Trial.
	Trial *TrialDecision `json:"trial,omitempty"`
	// TrialDraft names the draft whose trial made the call, sent and
	// decided on the trial's own Cards (kiff/tafs#173). Trial, above, is
	// only on a trial's call that was decided and not sent.
	TrialDraft string `json:"trial_draft,omitempty"`
	// ResourceArgument names the argument the call's Card scopes on (the
	// tool's resource argument), and ResourcePath where its value is in
	// the call's arguments, dotted ("parameters.charge"): the name itself
	// at the top level. Names only, never the value: an agent's runtime
	// reads the value there to say what the call was on (kiff/tafs#167).
	ResourceArgument string `json:"resource_argument,omitempty"`
	ResourcePath     string `json:"resource_path,omitempty"`
}

// Retry hints, in seconds.
const (
	retryHeld       = 30
	retryDeciding   = 2
	retryForwarding = 5
	retryKIFFDown   = 30
)

// Reasons the gateway records for calls it ended itself.
const (
	ReasonOperationIDReused    = "operation_id_reused"
	ReasonNotRecorded          = "call_not_recorded"
	ReasonDecideUnavailable    = "decide_unavailable"
	ReasonCredentialUnreadable = "credential_unreadable"
	ReasonToolUnreachable      = "tool_unreachable"
	ReasonResponseLost         = "response_lost"
	// ReasonPermitMissing: the connection is in verify or relay mode and
	// apps/api allowed the call without its execution permit, so
	// it was not sent: the tool would refuse it.
	ReasonPermitMissing = "permit_missing"
)

// reasonPermitAlreadyIssued is apps/api's: another request for the same
// operation already received its one permit.
const reasonPermitAlreadyIssued = "permit_already_issued"

// permitUsable is how long after it is issued a permit can still start a
// call: the longest lifetime apps/api issues plus verifier clock tolerance.
const permitUsable = 330 * time.Second

// nextSteps are what the agent should do about a refusal, by reason. The
// codes are apps/api's (proposals_mandate.go, proposals.go). A reason not
// listed keeps the refusal text as it was.
//
// It covers apps/api's codes only. The gateway's own Reason* codes are set
// with an explicit Retry where they are written; callOf reads this map
// only for StateRefused, so a gateway code stored on a refused
// invocation would get Retry "new_call" and no next step. If the gateway
// ever ends a call as refused for its own reason, add that reason here.
var nextSteps = map[string]struct {
	text  string
	retry string
}{
	"no_mandate_issued":            {"No Card covers this tool for this agent. Ask the account owner to issue one, then call again with a new " + OperationArg + ".", "new_call"},
	"mandate_not_covered":          {"The agent's Card does not cover this call. Ask the account owner to change the Card, then call again with a new " + OperationArg + ".", "new_call"},
	"mandate_per_action_exceeded":  {"This call is above the Card's per-call limit. Ask the account owner to raise the limit, then call again with a new " + OperationArg + ".", "new_call"},
	"mandate_resource_not_on_card": {"This call is on something the agent's Card does not cover. Ask the account owner to add it to the Card, then call again with a new " + OperationArg + ".", "new_call"},
	"mandate_limit_reached":        {"This call would go over what is left on the Card for its period. Ask the account owner to raise the limit, or call again with a new " + OperationArg + " once the period has room.", "new_call"},
	"mandate_expired":              {"The agent's Card is revoked or outside its validity period. Ask the account owner to renew it, then call again with a new " + OperationArg + ".", "new_call"},
	"mandate_owner_rejected":       {"The account owner rejected this call. Do not retry it.", "none"},
	"mandate_withdrawn":            {"This call was withdrawn when the agent's run was cancelled, and nothing was sent. If it still needs doing, call again with a new " + OperationArg + ".", "new_call"},
	"mandate_owner_expired":        {"The account owner did not answer before the hold expired. To ask again, call again with a new " + OperationArg + ".", "new_call"},
	"mandate_uncountable_draw":     {"The Card could not count this call's amount. Check the amount is a whole number, then call again with a new " + OperationArg + ".", "new_call"},
	"mandate_proposal_reused":      {"KIFF had already used this call's authorization. Call again with a new " + OperationArg + ".", "new_call"},
	"mandate_unavailable":          {"KIFF could not check the Card just now. Call again with a new " + OperationArg + " shortly.", "new_call"},
	"tenant_runtime_unavailable":   {"KIFF could not check this call just now. Call again with a new " + OperationArg + " shortly.", "new_call"},
	"key_not_bound_to_agent":       {"This agent's key is not set up for the gateway. Ask the account owner for a key bound to this agent.", "none"},
	"actor_not_key_holder":         {"This agent's key belongs to a different agent. Ask the account owner for a key bound to this agent.", "none"},
	"plan_limit_reached":           {"The account has reached its plan's limit. Ask the account owner.", "none"},
	"ceiling_reached":              {"This account's tafs reached the tasks the owner allows this period. Nothing was sent. Don't retry: stop and tell the owner, who can raise the ceiling.", "none"},
	"plan_not_started":             {"This account hasn't started its trial. Nothing was sent. Don't retry: stop and tell the owner.", "none"},
	"plan_trial_ended":             {"This account's trial ended, and no plan followed it. Nothing was sent. Don't retry: stop and tell the owner.", "none"},
	"permits_unavailable":          {"KIFF could not issue the execution permit this tool requires. Call again with a new " + OperationArg + " shortly; if it persists, ask the account owner.", "new_call"},
	"invalid_permit_request":       {"The tool's connection is not set up for execution permits. Ask the account owner to check the connection.", "none"},
	ReasonUnitMismatch:             {"The call is in another currency or unit than the agent's Card counts. Do not retry it; tell the account owner.", "none"},
	ReasonUnitUnverified:           {"KIFF could not check the call's currency or unit. Do not retry it; tell the account owner.", "none"},
	"idempotency_key_reuse":        {"This " + OperationArg + " was already used for a different call. Call again with a new " + OperationArg + ".", "new_call"},
	// The owner halted the agent, or its account:
	// nothing of it is sent until they resume it, and nothing asked before
	// the halt is sent after it.
	"halted":           {"The owner halted this agent, or the whole account. Nothing was sent, and nothing will be until they resume it. Don't retry: stop and say so.", "none"},
	"halt_unavailable": {"KIFF couldn't check whether this agent is halted, so nothing was sent. Call again with a new " + OperationArg + " shortly.", "new_call"},
}

// nextStep is the next-step sentence and retry hint for a refusal's
// reasons: the first reason that has one wins.
func nextStep(reasons []string) (string, string) {
	for _, r := range reasons {
		if s, ok := nextSteps[r]; ok {
			return s.text, s.retry
		}
	}
	return "", "new_call"
}

// refusalMessage is the agent-facing text of a refusal: what happened,
// the next step, then apps/api's own explanation. The codes stay in the
// text for support.
func refusalMessage(outcome string, reasons []string, apiMessage string) string {
	msg := "Refused by KIFF (" + outcome
	if len(reasons) > 0 {
		msg += ": " + strings.Join(reasons, ", ")
	}
	msg += "). Nothing was sent to the tool."
	if step, _ := nextStep(reasons); step != "" {
		msg += " Next: " + step
	}
	if apiMessage != "" {
		msg += " KIFF said: " + apiMessage
	}
	return msg
}

// utc is t in UTC. A time read back from Postgres comes in the session's
// time zone, so without it an agent's hold_expires_at would depend on the
// database's setting: the same instant as "…Z" or "…+02:00".
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// callOf is the Call for an invocation's recorded state.
func callOf(in invocation.Invocation) Call {
	c := Call{State: string(in.State), Invocation: in.Key, OperationID: in.OperationID,
		Reasons: in.Reasons, ExceptionID: in.ExceptionID, ReviewURL: in.ReviewURL, HoldExpiresAt: utc(in.HoldExpiresAt)}
	switch in.State {
	case invocation.StateHeld:
		c.Sent, c.Retry, c.RetryAfterS = "no", "same_call", retryHeld
	case invocation.StateDeciding:
		c.Sent, c.Retry, c.RetryAfterS = "no", "same_call", retryDeciding
	case invocation.StateForwarding:
		c.Sent, c.Retry, c.RetryAfterS = "unknown", "same_call", retryForwarding
	case invocation.StateForwarded:
		c.Sent, c.Retry = "yes", "none"
	case invocation.StateUnknown:
		// Never re-sent; the agent checks the tool before a new attempt.
		c.Sent, c.Retry = "unknown", "none"
	case invocation.StateRefused:
		_, c.Retry = nextStep(in.Reasons)
		c.Sent = "no"
	default: // failed
		c.Sent, c.Retry = "no", "new_call"
	}
	return c
}

// withResource names t's resource argument, and where it is, in the
// call state of a result that has one. A result without call state, or a
// tool without a resource argument, is returned unchanged.
func withResource(raw json.RawMessage, t Tool) json.RawMessage {
	if t.ResourceArg == "" {
		return raw
	}
	var m struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	var c Call
	if json.Unmarshal(raw, &m) != nil || m.Meta[MetaKey] == nil || json.Unmarshal(m.Meta[MetaKey], &c) != nil {
		return raw
	}
	c.ResourceArgument, c.ResourcePath = t.ResourceArg, t.resourcePath()
	return withCall(raw, c)
}

// withTrialDraft names, in the call's _meta, the draft whose trial made it
// (kiff/tafs#173). A result without the call's _meta is left as it is.
func withTrialDraft(raw json.RawMessage, draft string) json.RawMessage {
	if draft == "" {
		return raw
	}
	var m struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	var c Call
	if json.Unmarshal(raw, &m) != nil || m.Meta[MetaKey] == nil || json.Unmarshal(m.Meta[MetaKey], &c) != nil {
		return raw
	}
	c.TrialDraft = draft
	return withCall(raw, c)
}

// withCall adds c at _meta[MetaKey], keeping any other _meta keys.
//
// The result's other values stay as the tool wrote them: a forwarded
// tool's result passes through here, and decoding it into Go values would
// turn a large integer id into a float.
func withCall(raw json.RawMessage, c Call) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(m["_meta"], &meta) != nil || meta == nil {
		meta = map[string]json.RawMessage{}
	}
	cb, err := json.Marshal(c)
	if err != nil {
		return raw
	}
	meta[MetaKey] = cb
	mb, err := json.Marshal(meta)
	if err != nil {
		return raw
	}
	m["_meta"] = mb
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}

// withoutKIFFMeta removes every dev.kiff/ key from a forwarded tool's
// result _meta, keeping the tool's other keys. dev.kiff/ is the gateway's
// namespace: a client reading _meta[MetaKey] must be able to trust that the
// gateway wrote it, so a tool cannot forge a hold, a refusal or a review
// link there (kiff/kiff-plugins#10). A result with a _meta key is always
// re-encoded, so duplicate keys collapse to the one checked here and a
// client that keeps the first duplicate cannot see a forged copy. A result
// that is not a JSON object, or has no _meta, is returned unchanged.
func withoutKIFFMeta(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	if _, ok := m["_meta"]; !ok {
		return raw
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(m["_meta"], &meta) == nil && meta != nil {
		for k := range meta {
			if strings.HasPrefix(k, "dev.kiff/") {
				delete(meta, k)
			}
		}
		if len(meta) == 0 {
			delete(m, "_meta")
		} else {
			b, _ := json.Marshal(meta)
			m["_meta"] = b
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}

// kiffResult is a result the gateway writes itself: the text for the
// model and the Call for programs.
func kiffResult(text string, c Call) json.RawMessage {
	return withCall(mcp.TextResult(text, true), c)
}

// maxToolError bounds the tool's error message kept on the record.
const maxToolError = 300

// toolError is the tool's own error message when its result is an error
// (isError), for the record of how the call ended; "" when the
// tool reported success. It reads the result already redacted.
//
// Only an absent, null or false isError is success. Any other value,
// such as the string "true", is an error: a malformed flag is not
// evidence that the call was done.
func toolError(res json.RawMessage) string {
	var flag struct {
		IsError json.RawMessage `json:"isError"`
	}
	if json.Unmarshal(res, &flag) != nil {
		return ""
	}
	switch strings.TrimSpace(string(flag.IsError)) {
	case "", "null", "false":
		return ""
	}
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(res, &r) // malformed content leaves the generic message
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimSpace(c.Text))
		}
	}
	return toolErrorText(strings.Join(parts, " "))
}

// toolErrorText is msg as kept on the record: one line, at most
// maxToolError characters, never empty.
func toolErrorText(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if msg == "" {
		return "The tool returned an error."
	}
	if r := []rune(msg); len(r) > maxToolError {
		msg = string(r[:maxToolError-1]) + "…"
	}
	return msg
}

// toolOutcome is what the record says about a delivered call:
// "error" when the tool answered with one, "done" otherwise. Always set,
// so a recorded success is never confused with an outcome not recorded.
func toolOutcome(toolError string) string {
	if toolError != "" {
		return "error"
	}
	return "done"
}
