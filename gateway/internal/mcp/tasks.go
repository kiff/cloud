package mcp

import (
	"context"
	"encoding/json"
	"time"
)

// Task statuses (MCP tasks, revision 2025-11-25 and later).
const (
	TaskWorking   = "working"
	TaskCompleted = "completed"
	TaskFailed    = "failed"
)

// Task is an MCP task: a tools/call the client asked to run as a task, whose
// result it collects later with tasks/get and tasks/result instead of in
// the response. A client that does not ask for a task gets the ordinary
// result, so nothing changes for it.
type Task struct {
	ID            string
	Status        string
	StatusMessage string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// TTL is how long the task is kept after creation; PollInterval how
	// often the client should ask.
	TTL, PollInterval time.Duration
}

func (t Task) wire() map[string]any {
	m := map[string]any{
		"taskId":        t.ID,
		"status":        t.Status,
		"createdAt":     t.CreatedAt.UTC().Format(time.RFC3339),
		"lastUpdatedAt": t.UpdatedAt.UTC().Format(time.RFC3339),
		"ttl":           t.TTL.Milliseconds(),
	}
	if t.StatusMessage != "" {
		m["statusMessage"] = t.StatusMessage
	}
	if t.PollInterval > 0 {
		m["pollInterval"] = t.PollInterval.Milliseconds()
	}
	return m
}

// Terminal reports whether the task's result can be collected.
func (t Task) Terminal() bool { return t.Status != TaskWorking }

// TaskHandler is implemented by a Handler that runs tools/call as tasks.
// GetTask re-reads a task (and may advance it); TaskResult returns the
// tools/call result of a terminal task. Both refuse a task the caller does
// not own as unknown.
type TaskHandler interface {
	GetTask(ctx context.Context, id string) (Task, error)
	TaskResult(ctx context.Context, id string) (json.RawMessage, error)
}

// ErrUnknownTask answers tasks/get and tasks/result for a task that does
// not exist or is not the caller's.
var ErrUnknownTask = &RPCError{Code: codeInvalidParams, Message: "unknown task"}

// ErrTaskWorking answers tasks/result for a task that is still working.
var ErrTaskWorking = &RPCError{Code: codeInvalidParams, Message: "the task is still working; call tasks/get until it completes"}

// RespondWithTask asks that this tools/call be answered with t instead of
// its result. Honored only when the caller asked for a task; reports
// whether it will be.
func RespondWithTask(ctx context.Context, t Task) bool {
	st, ok := ctx.Value(callerKey{}).(*callerState)
	if !ok || !st.c.TaskRequested || t.ID == "" {
		return false
	}
	st.task = &t
	return true
}

// tasksCapability is declared when the handler can run tasks.
func tasksCapability() map[string]any {
	return map[string]any{"requests": map[string]any{"tools": map[string]any{"call": map[string]any{}}}}
}

// taskRequest dispatches tasks/get and tasks/result.
func taskRequest(ctx context.Context, h Handler, req request) (json.RawMessage, *RPCError) {
	th, ok := h.(TaskHandler)
	if !ok {
		return nil, &RPCError{Code: codeMethodNotFound, Message: "Method not found: " + req.Method}
	}
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.TaskID == "" {
		return nil, &RPCError{Code: codeInvalidParams, Message: req.Method + " needs a taskId"}
	}
	asRPC := func(err error) *RPCError {
		if rpc, ok := err.(*RPCError); ok {
			return rpc
		}
		return &RPCError{Code: codeInternal, Message: "the task could not be read"}
	}
	if req.Method == "tasks/get" {
		t, err := th.GetTask(ctx, p.TaskID)
		if err != nil {
			return nil, asRPC(err)
		}
		b, _ := json.Marshal(t.wire())
		return b, nil
	}
	res, err := th.TaskResult(ctx, p.TaskID)
	if err != nil {
		return nil, asRPC(err)
	}
	return res, nil
}
