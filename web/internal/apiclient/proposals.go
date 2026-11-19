package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ProposalDecision mirrors the apps/api proposals.DecisionRow
// wire shape returned by GET /v1/proposals. The JSON tags match
// the API contract.
type ProposalDecision struct {
	ProposalID  string    `json:"proposal_id"`
	EntityID    string    `json:"entity_id"`
	EntityType  string    `json:"entity_type"`
	ActionName  string    `json:"action_name"`
	ControlID   string    `json:"control_id,omitempty"`
	Environment string    `json:"environment,omitempty"`
	ActorID     string    `json:"actor_id,omitempty"`
	Outcome     string    `json:"outcome"`
	Reasons     []string  `json:"reasons,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// State-at-decision. StateAtDecision is the entity lifecycle state
	// KIFF reconstructed at decision time (not caller-supplied);
	// AllowedStates is the set the action is permitted from. Together
	// they make "decided against reconstructed state" legible on the
	// decision row. Empty for decisions recorded before or for
	// pre-state outcomes (invalid / unknown_action).
	StateAtDecision   string   `json:"state_at_decision,omitempty"`
	AllowedStates     []string `json:"allowed_states,omitempty"`
	PrincipalSubject  string   `json:"principal_subject,omitempty"`
	PrincipalAPIKeyID string   `json:"principal_api_key_id,omitempty"`
	// ActorVerified is true when a key bound to exactly ActorID made the
	// decision. Otherwise ActorID is the caller's claim, and must not be
	// shown as the agent's own activity or connection.
	ActorVerified  bool   `json:"actor_verified,omitempty"`
	ReceiptTraceID string `json:"receipt_trace_id,omitempty"`
	ReceiptStatus  string `json:"receipt_status"`
	AnchorTxHash   string `json:"anchor_tx_hash,omitempty"`
	// ToolOutcome is how an allowed gateway call ended at the tool;
	// nil for other decisions or without a gateway record.
	ToolOutcome *ToolOutcome `json:"tool_outcome,omitempty"`
}

// ProposalsListResponse is the envelope returned by
// GET /v1/proposals. LastReceivedAt is present when at least one
// proposal exists; absent otherwise.
type ProposalsListResponse struct {
	Proposals      []ProposalDecision `json:"proposals"`
	LastReceivedAt *time.Time         `json:"last_received_at,omitempty"`
}

// ListProposalsBearer fetches the most recent proposal decisions
// for the caller's tenant. limit <= 0 uses the server default (20).
func (c *Client) ListProposalsBearer(ctx context.Context, bearer string, limit int) (*ProposalsListResponse, error) {
	path := "/v1/proposals"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var resp ProposalsListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode proposals: %w", err)
	}
	if resp.Proposals == nil {
		resp.Proposals = []ProposalDecision{}
	}
	return &resp, nil
}
