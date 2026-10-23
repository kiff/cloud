package gateway

import (
	"encoding/json"
	"testing"

	"github.com/kiff/cloud/gateway/internal/invocation"
)

func TestTerminalNoEffectRequiresTrustedContractAndExactOperation(t *testing.T) {
	target := Tool{OutcomeContract: TerminalNoEffectV1, IdempotencyArg: "idempotency_key"}
	in := invocation.Invocation{Key: "operation-1", AgentID: "agent-1", Tool: "refund"}
	for _, tc := range []struct {
		name           string
		contract, idem string
		result         string
		want           bool
	}{
		{"trusted", TerminalNoEffectV1, "idempotency_key", `{"isError":true,"structuredContent":{"kiff_outcome":{"operation_id":"operation-1","effect":"none","terminal":true,"reason_code":"order_not_paid"}}}`, true},
		{"not_opted_in", "", "idempotency_key", `{"isError":true,"structuredContent":{"kiff_outcome":{"operation_id":"operation-1","effect":"none","terminal":true,"reason_code":"order_not_paid"}}}`, false},
		{"generic_error", TerminalNoEffectV1, "idempotency_key", `{"isError":true,"content":[{"type":"text","text":"nothing happened"}]}`, false},
		{"wrong_operation", TerminalNoEffectV1, "idempotency_key", `{"isError":true,"structuredContent":{"kiff_outcome":{"operation_id":"operation-2","effect":"none","terminal":true,"reason_code":"order_not_paid"}}}`, false},
		{"may_finish_later", TerminalNoEffectV1, "idempotency_key", `{"isError":true,"structuredContent":{"kiff_outcome":{"operation_id":"operation-1","effect":"none","terminal":false,"reason_code":"pending"}}}`, false},
		{"partial", TerminalNoEffectV1, "idempotency_key", `{"isError":true,"structuredContent":{"kiff_outcome":{"operation_id":"operation-1","effect":"partial","terminal":true,"reason_code":"partial"}}}`, false},
		{"success", TerminalNoEffectV1, "idempotency_key", `{"isError":false,"structuredContent":{"kiff_outcome":{"operation_id":"operation-1","effect":"none","terminal":true,"reason_code":"order_not_paid"}}}`, false},
		{"no_binding", TerminalNoEffectV1, "", `{"isError":true,"structuredContent":{"kiff_outcome":{"operation_id":"operation-1","effect":"none","terminal":true,"reason_code":"order_not_paid"}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target.OutcomeContract, target.IdempotencyArg = tc.contract, tc.idem
			r := terminalNoEffect(target, in, json.RawMessage(tc.result))
			if (len(r) > 0) != tc.want {
				t.Fatalf("classification %s", r)
			}
		})
	}
}
