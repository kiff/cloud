package mcp

import (
	"context"
	"encoding/json"
)

// Caller is what a request says about the client that sent it. On
// revision 2026-07-28 every request carries it in _meta; a legacy client
// declares it once at initialize, and this endpoint keeps no sessions, so
// a legacy Caller only has what the request itself carries.
//
// Everything here is the client's own claim. It may decide how a person
// is told about something, never who may do anything.
type Caller struct {
	Name, Version string
	// URLElicitation: the client declares elicitation.url.
	URLElicitation bool
	// Responses are the client's answers to this server's earlier
	// input requests on the same call (inputResponses), by request id:
	// "accept", "decline" or "cancel". In URL mode "accept" only means
	// the person agreed to open the link.
	Responses map[string]string
	// Modern: the request is on revision 2026-07-28.
	Modern bool
	// TaskRequested: the tools/call carries a task parameter, asking to
	// run as an MCP task.
	TaskRequested bool
}

// URLElicitation asks the client to open URL for a person to act there.
type URLElicitation struct {
	ID, URL, Message string
}

type callerKey struct{}

type callerState struct {
	c      Caller
	elicit *URLElicitation
	task   *Task
}

// CallerFrom is the request's Caller; the zero Caller outside a request.
func CallerFrom(ctx context.Context) Caller {
	if st, ok := ctx.Value(callerKey{}).(*callerState); ok {
		return st.c
	}
	return Caller{}
}

// RequestURLElicitation asks that this tools/call be answered with a
// URL elicitation instead of its result. The server honors it only for
// a modern caller that declares URL mode and has not already answered
// a request with this id on this call; it reports whether it will.
func RequestURLElicitation(ctx context.Context, e URLElicitation) bool {
	st, ok := ctx.Value(callerKey{}).(*callerState)
	if !ok || !st.c.Modern || !st.c.URLElicitation || e.ID == "" || e.URL == "" {
		return false
	}
	if _, answered := st.c.Responses[e.ID]; answered {
		return false
	}
	st.elicit = &e
	return true
}

func withCaller(ctx context.Context, c Caller) (context.Context, *callerState) {
	st := &callerState{c: c}
	return context.WithValue(ctx, callerKey{}, st), st
}

// callerOf reads a request's Caller from its params.
func callerOf(raw json.RawMessage, modern bool) Caller {
	var p struct {
		Meta struct {
			Info struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"io.modelcontextprotocol/clientInfo"`
			Caps struct {
				Elicitation map[string]json.RawMessage `json:"elicitation"`
			} `json:"io.modelcontextprotocol/clientCapabilities"`
		} `json:"_meta"`
		InputResponses map[string]struct {
			Action string `json:"action"`
		} `json:"inputResponses"`
		Task json.RawMessage `json:"task"`
	}
	_ = json.Unmarshal(raw, &p)
	c := Caller{Name: p.Meta.Info.Name, Version: p.Meta.Info.Version, Modern: modern,
		TaskRequested: len(p.Task) > 0 && string(p.Task) != "null"}
	_, c.URLElicitation = p.Meta.Caps.Elicitation["url"]
	if len(p.InputResponses) > 0 {
		c.Responses = make(map[string]string, len(p.InputResponses))
		for id, r := range p.InputResponses {
			c.Responses[id] = r.Action
		}
	}
	return c
}

// inputRequired is the 2026-07-28 result asking the client to open e's
// URL and then send the same call again with its answer.
func inputRequired(e URLElicitation) map[string]any {
	return map[string]any{
		"resultType": "input_required",
		"inputRequests": map[string]any{
			e.ID: map[string]any{
				"method": "elicitation/create",
				"params": map[string]any{"mode": "url", "message": e.Message, "url": e.URL},
			},
		},
		"requestState": e.ID,
	}
}
