package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/kiff/cloud/gateway/internal/invocation"
)

const TerminalNoEffectV1 = "terminal_no_effect_v1"

// This contract is an explicit owner choice for a trusted business executor.
// Its structured response guarantees that the exact operation had no effect
// and cannot execute later. Generic MCP errors and free text are not evidence.
func terminalNoEffect(t Tool, in invocation.Invocation, res json.RawMessage) json.RawMessage {
	return noEffectEvidence(t, in, res, true)
}

func noEffectEvidence(t Tool, in invocation.Invocation, res json.RawMessage, direct bool) json.RawMessage {
	if t.OutcomeContract != TerminalNoEffectV1 || t.IdempotencyArg == "" {
		return nil
	}
	var result struct {
		IsError    bool `json:"isError"`
		Structured struct {
			Outcome struct {
				Operation string `json:"operation_id"`
				Effect    string `json:"effect"`
				Terminal  bool   `json:"terminal"`
				Reason    string `json:"reason_code"`
			} `json:"kiff_outcome"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(res, &result) != nil || (direct && !result.IsError) || (!direct && result.IsError) {
		return nil
	}
	o := result.Structured.Outcome
	if o.Operation != in.Key || o.Effect != "none" || !o.Terminal || strings.TrimSpace(o.Reason) == "" || len(o.Reason) > 200 {
		return nil
	}
	e, _ := json.Marshal(map[string]any{"contract": TerminalNoEffectV1, "operation_id": in.Key, "agent_id": in.AgentID, "action": in.Tool, "reason": o.Reason})
	return e
}

type effectReconciler interface {
	ReconcileEffects(context.Context, string, string) error
}

// Only after the terminal result/evidence commits can the API append release
// records. A failed reconciliation keeps budget charged and is retried when
// this result is collected again; it never causes another tool send.
func (a *agent) reconcileEffects(ctx context.Context, in invocation.Invocation) {
	if len(in.NoEffectEvidence) == 0 {
		return
	}
	r, ok := a.g.API.(effectReconciler)
	if !ok {
		return
	}
	if err := r.ReconcileEffects(ctx, a.key, in.Key); err != nil && a.g.Logger != nil {
		a.g.Logger.WarnContext(ctx, "gateway: effect reconciliation pending", slog.String("invocation", in.Key), slog.String("err", err.Error()))
	}
}
