package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
	"github.com/kiff/cloud/gateway/internal/secret"
)

func outcomeBinding(t Tool, credential string) json.RawMessage {
	hash := sha256.Sum256([]byte(credential))
	b, _ := json.Marshal(struct {
		URL, Tool, Idempotency, Contract, Lookup, Mode, Audience, CredentialHash string
		ConnectionCreatedAt                                                      time.Time
	}{t.UpstreamURL, t.UpstreamTool, t.IdempotencyArg, t.OutcomeContract, t.OutcomeLookupTool, t.Mode, t.Audience, hex.EncodeToString(hash[:]), t.ConnectionCreatedAt.UTC()})
	return b
}

// RegisterReconciliation exposes a read-only executor lookup. It never calls
// the action tool, issues a permit, reopens an operation or widens a Card.
func (g *Gateway) RegisterReconciliation(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/operations/{key}/reconcile", g.reconcileOperation)
	mux.HandleFunc("POST /a/{id}/operations/{key}/reconcile", g.reconcileOperation)
}

func (g *Gateway) reconcileOperation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	reply := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": message, "operation_reopened": false})
	}
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(key) == "" {
		reply(http.StatusUnauthorized, "a KIFF credential is required")
		return
	}
	key = strings.TrimSpace(key)
	r = r.WithContext(kiffapi.WithTenant(r.Context(), strings.TrimSpace(r.Header.Get(kiffapi.TenantHeader))))
	me, err := g.API.Me(r.Context(), key)
	if err != nil || me.TenantID == "" {
		reply(http.StatusUnauthorized, "credential not accepted")
		return
	}
	resource := g.Resource
	if link := r.PathValue("id"); link != "" {
		if !ValidLinkID(link) {
			reply(http.StatusNotFound, "no such operation")
			return
		}
		resource = g.LinkResource(link)
	}
	if strings.HasPrefix(key, OAuthTokenPrefix) && (me.Resource != resource || me.CredentialID == "") {
		reply(http.StatusForbidden, "token issued for another gateway resource")
		return
	}
	if me.AgentID == "" && !me.CanManage() {
		reply(http.StatusForbidden, "management authority or an agent credential is required")
		return
	}
	in, err := g.Store.Get(r.Context(), r.PathValue("key"))
	if err != nil || in.TenantID != me.TenantID || (me.AgentID != "" && (in.AgentID != me.AgentID || in.CredentialID != credentialID(key, me))) {
		reply(http.StatusNotFound, "no such operation")
		return
	}
	a := &agent{g: g, key: key, credentialID: credentialID(key, me), me: me}
	if in.State == invocation.StateForwarded && len(in.NoEffectEvidence) > 0 {
		if !a.releaseReconciled(r.Context(), in) {
			reply(http.StatusServiceUnavailable, "trusted outcome retained; budget reconciliation is pending")
			return
		}
		reply(http.StatusOK, "final no-effect outcome reconciled; action remains spent")
		return
	}
	if in.State != invocation.StateUnknown || len(in.ExecutionBinding) == 0 {
		reply(http.StatusConflict, "no trusted status lookup for this operation; budget remains charged")
		return
	}
	t, err := a.tool(r.Context(), in.Tool)
	if err != nil || t.OutcomeContract != TerminalNoEffectV1 || t.OutcomeLookupTool == "" || t.IdempotencyArg == "" || t.OutcomeLookupTool == t.UpstreamTool {
		reply(http.StatusConflict, "original executor status lookup unavailable; budget remains charged")
		return
	}
	credential := ""
	if g.Credentials != nil && t.Mode != ModeVerify {
		credential, err = g.Credentials.Credential(r.Context(), in.TenantID, in.Tool)
		if err != nil {
			reply(http.StatusServiceUnavailable, "executor credential unavailable; budget remains charged")
			return
		}
	}
	if !bytes.Equal(in.ExecutionBinding, outcomeBinding(t, credential)) {
		// jsonb normalizes object whitespace/order. Compare canonical bytes.
		stored, se := invocation.CanonicalJSON(in.ExecutionBinding)
		current, ce := invocation.CanonicalJSON(outcomeBinding(t, credential))
		if se != nil || ce != nil || !bytes.Equal(stored, current) {
			reply(http.StatusConflict, "executor binding changed; budget remains charged")
			return
		}
	}
	body, _ := json.Marshal(map[string]string{t.IdempotencyArg: in.Key})
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	result, err := g.Forward.CallTool(ctx, t.UpstreamURL, credential, t.OutcomeLookupTool, body)
	cancel()
	if err != nil {
		reply(http.StatusServiceUnavailable, "status lookup did not settle the outcome; budget remains charged")
		return
	}
	result = withoutKIFFMeta(secret.RedactJSON(result, credential))
	evidence := noEffectEvidence(t, in, result, false)
	if len(evidence) == 0 {
		reply(http.StatusConflict, "status lookup did not prove final no effect; budget remains charged")
		return
	}
	// Retain the original unknown outcome and the read-only reconciliation
	// source in evidence. Only this unknown -> terminal transition is allowed.
	var fact map[string]any
	_ = json.Unmarshal(evidence, &fact)
	fact["source"], fact["previous_state"] = "read_only_lookup", string(in.State)
	evidence, _ = json.Marshal(fact)
	final := mcp.TextResult("The trusted executor confirmed a final refusal with no effect. This operation will not be sent again.", true)
	won, err := g.Store.Transition(r.Context(), in.Key, []invocation.State{invocation.StateUnknown}, invocation.StateForwarded, invocation.Update{Result: final, ToolOutcome: "error", ToolError: "Final no-effect refusal confirmed by the executor's status lookup.", NoEffectEvidence: evidence})
	if err != nil {
		reply(http.StatusServiceUnavailable, "trusted reconciliation could not be recorded; budget remains charged")
		return
	}
	if !won {
		reply(http.StatusConflict, "operation changed; read its current outcome before reconciling")
		return
	}
	in, err = g.Store.Get(r.Context(), in.Key)
	if err != nil || !a.releaseReconciled(r.Context(), in) {
		reply(http.StatusServiceUnavailable, "trusted outcome retained; budget reconciliation is pending")
		return
	}
	reply(http.StatusOK, "final no-effect outcome reconciled; action remains spent")
}

func (a *agent) releaseReconciled(ctx context.Context, in invocation.Invocation) bool {
	r, ok := a.g.API.(effectReconciler)
	return ok && r.ReconcileEffects(ctx, a.key, in.Key) == nil
}
