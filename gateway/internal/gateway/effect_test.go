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
		// planque's API-key tools (amount lookups): a vendor's final 4xx
		// comes as its words and the structured outcome, keyed by planque_op,
		// which KIFF fills with the operation. The status alone decides; the
		// vendor's text never counts.
		{"planque_4xx", TerminalNoEffectV1, "planque_op", `{"isError":true,"content":[{"type":"text","text":"api.vendor.test refused it (402): {\"error\":\"card declined\"}"}],"structuredContent":{"kiff_outcome":{"operation_id":"operation-1","effect":"none","terminal":true,"reason_code":"vendor_refused_402"}}}`, true},
		{"planque_5xx", TerminalNoEffectV1, "planque_op", `{"isError":true,"content":[{"type":"text","text":"Sent to api.vendor.test, which answered 503: it may or may not have done it."}]}`, false},
		{"planque_408", TerminalNoEffectV1, "planque_op", `{"isError":true,"content":[{"type":"text","text":"api.vendor.test refused it (408): timeout"}]}`, false},
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
