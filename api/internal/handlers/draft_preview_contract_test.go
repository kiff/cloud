package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// replyYAML is the gateway contract a batch connecting post_reply beside
// refund would apply: what the account's contract doesn't hold yet.
var replyYAML = strings.Replace(refundCentsYAML, "permissions:\n", `  - name: post_reply
    allowed_states: [requested]
    required_parameters: [ticket_id]
    parameters:
      - name: ticket_id
        type: string
        min_length: 1
        max_length: 200
    required_permissions: [tools.post_reply]
    risk: medium
    approval: never
    executor: cloud.proxy
    requires_authority: true
permissions:
`, 1)

func replyCard() map[string]any {
	return map[string]any{"id": "coach-reply", "subject": "setup", "domain": "gateway",
		"grants":     []map[string]any{{"action": "post_reply"}},
		"aggregates": []map[string]any{{"quantity": "count", "limit": 20, "window": "calendar_day"}}}
}

// A first start signs a batch of tools and Coach's draft together, so its
// Cards are worded before the tools are connected: against the contract the
// batch would apply (the gateway's batch preview sends it), with the batch's
// tool titles. The words are KIFF's, nothing is stored, and the draft is
// taken only while the contract it was worded over is in force.
func TestAPreviewWordsCardsOverTheContractABatchWouldApply(t *testing.T) {
	r := newDraftRig(t, WithMandatesToolTitles(fakeTitles{"refund": "Refund a payment"}))
	preview := func(body map[string]any) (int, map[string]any) {
		rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/agent-drafts/preview", body)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	draft := map[string]any{"agent_id": "setup", "label": "Coach", "cards": []any{replyCard()}}

	// Over the stored contract, a Card over a tool not connected is refused.
	if code, out := preview(draft); code != http.StatusBadRequest {
		t.Fatalf("without the batch's contract: %d %v", code, out)
	}
	_, stored := preview(map[string]any{"agent_id": "rae", "cards": []any{card("rae-refund", "rae")}})
	storedDigest, _ := stored["contract_digest"].(string)
	if storedDigest == "" {
		t.Fatalf("a preview answers the contract's digest: %v", stored)
	}

	// Over the batch's, worded, with its tool's title.
	with := map[string]any{"agent_id": "setup", "label": "Coach", "cards": []any{replyCard()}, "contract": replyYAML, "titles": map[string]string{"post_reply": "Post a reply"}}
	code, out := preview(with)
	if code != http.StatusOK {
		t.Fatalf("with the batch's contract: %d %v", code, out)
	}
	if lines := mustJSON(t, out["cards"]); !strings.Contains(lines, "Post a reply") {
		t.Errorf("the batch's tool title isn't in the words: %s", lines)
	}
	digest, _ := out["contract_digest"].(string)
	if digest == "" || digest == storedDigest {
		t.Fatalf("the batch's contract digest: %q (stored %q)", digest, storedDigest)
	}
	for _, k := range []string{"id", "claim_secret", "status"} {
		if _, ok := out[k]; ok {
			t.Errorf("a preview has no %s", k)
		}
	}
	if list := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts", nil); strings.Contains(list.Body.String(), "drf_") {
		t.Errorf("a preview stored a draft: %s", list.Body.String())
	}

	// The draft is taken only while the contract it was worded over is in
	// force: worded over the batch's, while the stored one is still in
	// force, it's refused; over the stored one, it's taken.
	stale := map[string]any{"agent_id": "rae", "cards": []any{card("rae-refund", "rae")}, "contract_digest": digest}
	if code, out := r.submit(admin(), stale); code != http.StatusConflict || out["error"] != "contract_changed" {
		t.Fatalf("a draft worded over another contract: %d %v", code, out)
	}
	fresh := map[string]any{"agent_id": "rae", "cards": []any{card("rae-refund", "rae")}, "contract_digest": storedDigest}
	if code, out := r.submit(admin(), fresh); code != http.StatusCreated {
		t.Fatalf("a draft worded over the contract in force: %d %v", code, out)
	}

	// Refused: from a key that may not change the gateway's domain, a
	// contract of another domain, one that doesn't parse, titles alone, a
	// title on two lines.
	if rr := do(t, r.mux, agentKey("setup-agent", ""), http.MethodPost, "/v1/me/agent-drafts/preview", with); rr.Code != http.StatusForbidden {
		t.Errorf("an agent's key: %d %s", rr.Code, rr.Body.String())
	}
	for name, body := range map[string]map[string]any{
		"another domain":       {"agent_id": "setup", "cards": []any{replyCard()}, "contract": strings.Replace(replyYAML, "domain: gateway", "domain: billing", 1)},
		"not a contract":       {"agent_id": "setup", "cards": []any{replyCard()}, "contract": "domain: ["},
		"titles alone":         {"agent_id": "setup", "cards": []any{replyCard()}, "titles": map[string]string{"post_reply": "Post a reply"}},
		"a title on two lines": {"agent_id": "setup", "cards": []any{replyCard()}, "contract": replyYAML, "titles": map[string]string{"post_reply": "Post\na reply"}},
	} {
		if code, out := preview(body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
