package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// A held call can run as an MCP task. When the client asks for
// a task, the gateway answers a held call with a working task instead of a
// "retry later" result. The client polls tasks/get; each poll asks KIFF for
// the owner's answer exactly as a retry of the same call would, and once
// the call is forwarded, refused or expired the task completes and
// tasks/result returns the call's result. The agent's conversation simply
// waits for it: nobody has to tell the agent to retry.
//
// The task id is the invocation key. Only the key that made the call can
// read its task; any other caller is told the task does not exist.

const (
	taskPollInterval = 5 * time.Second
	// taskTTL is how long a task is kept: well past the longest hold
	// (7 days), so a client can always collect the outcome.
	taskTTL = 8 * 24 * time.Hour
)

// answerAsTask answers the call with its task when the client asked for
// one: working while the call is held or in flight, completed when it has
// an outcome (collected with tasks/result).
func (a *agent) answerAsTask(ctx context.Context, key string) {
	if key == "" || !mcp.CallerFrom(ctx).TaskRequested {
		return
	}
	in, err := a.g.Store.Get(ctx, key)
	if err != nil || in.CredentialID != a.credentialID {
		return
	}
	mcp.RespondWithTask(ctx, taskOf(in))
}

func taskOf(in invocation.Invocation) mcp.Task {
	t := mcp.Task{ID: in.Key, Status: mcp.TaskCompleted, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
		TTL: taskTTL, PollInterval: taskPollInterval}
	switch in.State {
	case invocation.StateHeld:
		t.Status, t.StatusMessage = mcp.TaskWorking, "Waiting for the owner's approval. Nothing has been sent to the tool."
	case invocation.StateDeciding:
		t.Status, t.StatusMessage = mcp.TaskWorking, "KIFF is deciding."
	case invocation.StateForwarding:
		t.Status, t.StatusMessage = mcp.TaskWorking, "Sent to the tool; waiting for its result."
	}
	return t
}

// owned is the invocation behind task id, if this agent's key made it.
func (a *agent) owned(ctx context.Context, id string) (invocation.Invocation, error) {
	in, err := a.g.Store.Get(ctx, id)
	if err != nil || in.CredentialID != a.credentialID {
		return invocation.Invocation{}, mcp.ErrUnknownTask
	}
	return in, nil
}

// GetTask re-reads a task. While the call waits for the owner, it asks
// KIFF again exactly as a retry of the same call would, so the owner's
// answer moves the task on.
func (a *agent) GetTask(ctx context.Context, id string) (mcp.Task, error) {
	in, err := a.owned(ctx, id)
	if err != nil {
		return mcp.Task{}, err
	}
	if in.State == invocation.StateHeld || in.State == invocation.StateDeciding || in.State == invocation.StateForwarding {
		t, err := a.tool(ctx, in.Tool)
		if err != nil {
			return mcp.Task{}, err
		}
		dec := json.NewDecoder(bytes.NewReader(in.Arguments))
		dec.UseNumber()
		args := map[string]any{}
		_ = dec.Decode(&args)
		params, err := t.params(args)
		if err != nil {
			return mcp.Task{}, err
		}
		if _, err := a.retry(context.WithoutCancel(ctx), t, in, params); err != nil {
			return mcp.Task{}, err
		}
		if in, err = a.g.Store.Get(ctx, id); err != nil {
			return mcp.Task{}, err
		}
	}
	return taskOf(in), nil
}

// TaskResult is the call's result once the task has completed.
func (a *agent) TaskResult(ctx context.Context, id string) (json.RawMessage, error) {
	in, err := a.owned(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskOf(in).Terminal() {
		return nil, mcp.ErrTaskWorking
	}
	return a.replay(ctx, id)
}
