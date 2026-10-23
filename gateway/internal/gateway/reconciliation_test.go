package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

type reconciliationAPI struct {
	*fakeAPI
	mu       sync.Mutex
	fail     bool
	releases []string
}

func (a *reconciliationAPI) ReconcileEffects(_ context.Context, _, operation string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return errors.New("local API unavailable")
	}
	a.releases = append(a.releases, operation)
	return nil
}

type statusExecutor struct {
	mu           sync.Mutex
	sends, reads int
	result       string
	lookupError  bool
}

func (e *statusExecutor) CallTool(_ context.Context, _, _, tool string, args json.RawMessage) (json.RawMessage, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tool != "refund_status" {
		e.sends++
		return nil, &mcp.CallError{Sent: true, Err: errors.New("response lost")}
	}
	e.reads++
	if e.lookupError {
		return nil, errors.New("status unavailable")
	}
	var a map[string]string
	_ = json.Unmarshal(args, &a)
	if len(a) != 1 || a["idempotency_key"] == "" {
		return nil, errors.New("lookup must contain only the operation identity")
	}
	return json.RawMessage(strings.ReplaceAll(e.result, "OPERATION", a["idempotency_key"])), nil
}

const finalStatus = `{"isError":false,"structuredContent":{"kiff_outcome":{"operation_id":"OPERATION","effect":"none","terminal":true,"reason_code":"final_refusal"}}}`

func TestExecutorBindingDoesNotRenewForwardingLease(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	in, created, err := r.store.Begin(ctx, invocation.Call{TenantID: "t1", CredentialID: "credential", AgentID: "coding-agent", Tool: "issue_refund", OperationID: "binding-lease", Arguments: json.RawMessage(`{}`)})
	if err != nil || !created {
		t.Fatal(created, err)
	}
	if won, err := r.store.Transition(ctx, in.Key, []invocation.State{invocation.StateDeciding}, invocation.StateForwarding, invocation.Update{}); err != nil || !won {
		t.Fatal(won, err)
	}
	before, err := r.store.Get(ctx, in.Key)
	if err != nil {
		t.Fatal(err)
	}
	*r.now = r.now.Add(time.Minute)
	for i := 0; i < 2; i++ {
		if won, err := r.store.BindExecution(ctx, in.Key, json.RawMessage(`{"executor":"original"}`)); err != nil || !won {
			t.Fatal(won, err)
		}
	}
	if won, err := r.store.BindExecution(ctx, in.Key, json.RawMessage(`{"executor":"changed"}`)); err != nil || won {
		t.Fatal("binding changed", won, err)
	}
	after, err := r.store.Get(ctx, in.Key)
	if err != nil || after.State != invocation.StateForwarding || !before.UpdatedAt.Equal(after.UpdatedAt) {
		t.Fatal("binding renewed forwarding lease", after, err)
	}
	if won, err := r.store.Transition(ctx, in.Key, []invocation.State{invocation.StateForwarding}, invocation.StateUnknown, invocation.Update{}); err != nil || !won {
		t.Fatal(won, err)
	}
	if won, err := r.store.BindExecution(ctx, in.Key, json.RawMessage(`{"executor":"original"}`)); err != nil || won {
		t.Fatal("unknown operation rebound", won, err)
	}
}

func reconciliationRig(t *testing.T) (*rig, *reconciliationAPI, *statusExecutor, string) {
	t.Helper()
	r := newRig(t)
	a := &reconciliationAPI{fakeAPI: r.api}
	e := &statusExecutor{result: finalStatus}
	target := refundTool
	target.OutcomeContract, target.OutcomeLookupTool = gateway.TerminalNoEffectV1, "refund_status"
	r.gw.API, r.gw.Forward, r.gw.Catalog = a, e, catalog{target}
	call := r.call(t, "agent-key", amount(100, "unknown-operation"))
	key, _ := call.call["invocation"].(string)
	if !call.isError || key == "" {
		t.Fatalf("expected unknown operation: %+v", call)
	}
	in, err := r.store.Get(context.Background(), key)
	if err != nil || in.State != invocation.StateUnknown || len(in.ExecutionBinding) == 0 {
		t.Fatalf("unknown binding: %+v %v", in, err)
	}
	return r, a, e, key
}

func reconcile(t *testing.T, r *rig, credential, key string) int {
	t.Helper()
	mux := http.NewServeMux()
	r.gw.RegisterReconciliation(mux)
	req := httptest.NewRequest(http.MethodPost, "/v1/operations/"+key+"/reconcile", strings.NewReader(`{"effect":"none"}`))
	req.Header.Set("Authorization", "Bearer "+credential)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Code
}

func TestUnknownReconciliationNeverResendsAndRetriesDurableRelease(t *testing.T) {
	r, api, executor, key := reconciliationRig(t)
	api.fail = true
	if status := reconcile(t, r, "agent-key", key); status != http.StatusServiceUnavailable {
		t.Fatalf("failed release: %d", status)
	}
	in, err := r.store.Get(context.Background(), key)
	if err != nil || in.State != invocation.StateForwarded || len(in.NoEffectEvidence) == 0 {
		t.Fatalf("trusted result must survive API failure: %+v %v", in, err)
	}
	var fact map[string]any
	_ = json.Unmarshal(in.NoEffectEvidence, &fact)
	if fact["source"] != "read_only_lookup" || fact["previous_state"] != "unknown" {
		t.Fatalf("missing provenance: %s", in.NoEffectEvidence)
	}
	api.fail = false
	for i := 0; i < 3; i++ {
		if status := reconcile(t, r, "agent-key", key); status != http.StatusOK {
			t.Fatalf("reconciliation: %d", status)
		}
	}
	if replay := r.call(t, "agent-key", amount(100, "unknown-operation")); !replay.isError {
		t.Fatalf("replay: %+v", replay)
	}
	if executor.sends != 1 || executor.reads != 1 {
		t.Fatalf("action/status sends: %d/%d", executor.sends, executor.reads)
	}
	if len(api.releases) != 4 {
		t.Fatalf("reconciliation retries and replay: %v", api.releases)
	}
	if amount, draws := api.totalDrawn(); amount != 100 || draws != 1 {
		t.Fatalf("operation reopened: %d/%d", amount, draws)
	}
}

func TestUnknownReconciliationConcurrentReadsDoNotReopenOperation(t *testing.T) {
	r, api, executor, key := reconciliationRig(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := reconcile(t, r, "agent-key", key)
			if status != 200 && status != 409 {
				t.Errorf("concurrent reconciliation: %d", status)
			}
		}()
	}
	wg.Wait()
	in, err := r.store.Get(context.Background(), key)
	if err != nil || in.State != invocation.StateForwarded || len(in.NoEffectEvidence) == 0 || executor.sends != 1 || executor.reads < 1 {
		t.Fatalf("reconciliation state: %+v %v sends=%d reads=%d", in, err, executor.sends, executor.reads)
	}
	if amount, draws := api.totalDrawn(); amount != 100 || draws != 1 {
		t.Fatalf("authorization reopened: %d/%d", amount, draws)
	}
	// Neither the executor binding nor the first terminal evidence may be
	// replaced by a later internal transition.
	_, err = r.store.Transition(context.Background(), key, []invocation.State{invocation.StateForwarded}, invocation.StateForwarded, invocation.Update{ExecutionBinding: json.RawMessage(`{"changed":true}`), NoEffectEvidence: json.RawMessage(`{"changed":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	after, err := r.store.Get(context.Background(), key)
	if err != nil || string(after.ExecutionBinding) != string(in.ExecutionBinding) || string(after.NoEffectEvidence) != string(in.NoEffectEvidence) {
		t.Fatal("immutable provenance replaced", after, err)
	}
}

func TestUnknownReconciliationFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, credential, result                  string
		changeBinding, removeBinding, lookupError bool
		status                                    int
	}{
		{name: "other_agent", credential: "chat-key", result: finalStatus, status: 404},
		{name: "other_credential", credential: "agent-key-2", result: finalStatus, status: 404},
		{name: "unbound", credential: "unbound-key", result: finalStatus, status: 403},
		{name: "changed_executor", credential: "agent-key", result: finalStatus, changeBinding: true, status: 409},
		{name: "legacy_unknown", credential: "agent-key", result: finalStatus, removeBinding: true, status: 409},
		{name: "read_failed", credential: "agent-key", result: finalStatus, lookupError: true, status: 503},
		{name: "absence", credential: "agent-key", result: `{"isError":false,"content":[]}`, status: 409},
		{name: "nonterminal", credential: "agent-key", result: `{"structuredContent":{"kiff_outcome":{"operation_id":"OPERATION","effect":"none","terminal":false,"reason_code":"pending"}}}`, status: 409},
		{name: "wrong_operation", credential: "agent-key", result: strings.ReplaceAll(finalStatus, "OPERATION", "other"), status: 409},
		{name: "partial", credential: "agent-key", result: strings.ReplaceAll(finalStatus, `"none"`, `"partial"`), status: 409},
		{name: "read_error_is_not_refusal", credential: "agent-key", result: strings.ReplaceAll(finalStatus, `"isError":false`, `"isError":true`), status: 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, api, executor, key := reconciliationRig(t)
			executor.result, executor.lookupError = tc.result, tc.lookupError
			if tc.changeBinding {
				target := r.gw.Catalog.(catalog)[0]
				target.UpstreamURL = "http://different-executor.test"
				r.gw.Catalog = catalog{target}
			}
			if tc.removeBinding {
				if _, err := r.pool.Exec(context.Background(), `UPDATE gateway.invocations SET execution_binding=NULL WHERE key=$1`, key); err != nil {
					t.Fatal(err)
				}
			}
			if status := reconcile(t, r, tc.credential, key); status != tc.status {
				t.Fatalf("status %d, want %d", status, tc.status)
			}
			in, err := r.store.Get(context.Background(), key)
			if err != nil || in.State != invocation.StateUnknown || len(in.NoEffectEvidence) != 0 || len(api.releases) != 0 || executor.sends != 1 {
				t.Fatalf("unsafe settlement: %+v %v releases=%v sends=%d", in, err, api.releases, executor.sends)
			}
		})
	}
}
