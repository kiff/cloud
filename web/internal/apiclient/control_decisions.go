package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ControlDecisionsResponse mirrors GET
// /v1/me/controls/{control_id}/decisions.
type ControlDecisionsResponse struct {
	Control   ControlDecisionsControl `json:"control"`
	Window    ControlDecisionsWindow  `json:"window"`
	Decisions ControlDecisions        `json:"decisions"`
	// ByAction is the per-action, window-complete decision breakdown for a
	// control that governs several actions. Empty for single-action or
	// action-scoped reads (omitted on the wire).
	ByAction []ActionCountsView `json:"by_action,omitempty"`
	// Additive evidence sections (wire shape). Each carries a status
	// (complete | empty | unavailable) so the web renders honest empty
	// / "not yet recorded" states from the wire.
	Receipts  ControlDecisionsReceipts `json:"receipts"`
	Alerts    ControlDecisionsAlerts   `json:"alerts"`
	Approvals ControlDecisionsSection  `json:"approvals"`
}

// ActionCountsView is one governed action's window-complete decision
// tally: outcome counts + total.
type ActionCountsView struct {
	Action string         `json:"action"`
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

// ControlDecisionsSection is the minimal honest-status envelope a
// section carries when it has no payload yet (e.g. approvals).
type ControlDecisionsSection struct {
	Status string `json:"status"`
}

// ControlDecisionsReceipts is the control-scoped receipts section:
// aggregate tally + a trimmed, newest-first sample, plus a scope-aware
// verifiability claim.
type ControlDecisionsReceipts struct {
	Status        string                        `json:"status"`
	Count         int                           `json:"count"`
	SignedCount   int                           `json:"signed_count"`
	AnchoredCount int                           `json:"anchored_count"`
	Truncated     bool                          `json:"truncated"`
	Verifiability ControlDecisionsVerifiability `json:"verifiability"`
	Samples       []ReceiptSampleView           `json:"samples"`
}

// ReceiptSampleView is the glanceable per-receipt view.
type ReceiptSampleView struct {
	TraceID    string    `json:"trace_id"`
	ProposalID string    `json:"proposal_id,omitempty"`
	EntityID   string    `json:"entity_id"`
	ActionName string    `json:"action_name"`
	IssuedAt   time.Time `json:"issued_at"`
	Signed     bool      `json:"signed"`
	Anchored   bool      `json:"anchored"`
	// Kind is "decision" (pre-execution decision receipt) or
	// "execution". Empty on older API responses; treated as "execution".
	Kind string `json:"kind,omitempty"`
}

// ControlDecisionsVerifiability carries the computed claim plus its
// scope: "sample" means the counts are over the shown sample, not
// window-complete, so an "all" claim is never misread.
type ControlDecisionsVerifiability struct {
	SignedAlgorithm string `json:"signed_algorithm"`
	SignedCount     int    `json:"signed_count"`
	AnchoredCount   int    `json:"anchored_count"`
	Anchored        string `json:"anchored"` // none | partial | all
	Note            string `json:"note"`
	Scope           string `json:"scope,omitempty"` // sample | window
}

// ControlDecisionsAlerts is the alert-deliveries section (G4/#406 Q4):
// an aggregate list, each delivery carrying proposal_id so the web can
// cross-link back to its triggering decision row.
type ControlDecisionsAlerts struct {
	Status     string              `json:"status"`
	Deliveries []AlertDeliveryView `json:"deliveries"`
}

// AlertDeliveryView is the wire view of one alert delivery attempt.
// Status is one of delivered | failed | blocked.
type AlertDeliveryView struct {
	ProposalID  string    `json:"proposal_id,omitempty"`
	TargetID    string    `json:"target_id"`
	TargetType  string    `json:"target_type"`
	Status      string    `json:"status"`
	AttemptedAt time.Time `json:"attempted_at"`
}

// ControlDecisionsControl is the control header for one evidence window.
// Action is the authoritative action scoped by the endpoint; Actions is
// tolerated for compatibility with the control-list shape.
type ControlDecisionsControl struct {
	ID          string    `json:"id"`
	Domain      string    `json:"domain"`
	Entity      string    `json:"entity"`
	Action      string    `json:"action"`
	Actions     []string  `json:"actions,omitempty"`
	Environment string    `json:"environment"`
	Workflow    string    `json:"workflow"`
	Status      string    `json:"status"`
	Mode        string    `json:"mode,omitempty"`
	ActivatedAt time.Time `json:"activated_at"`
	ActivatedBy string    `json:"activated_by,omitempty"`
	// Posture is the additive posture-change history block. Absent
	// when the API has no posture reader wired or no change has been
	// recorded — render the honest empty-state in that case.
	Posture *ControlDecisionsPosture `json:"posture,omitempty"`
}

// ControlDecisionsPosture mirrors the control header's posture
// block: current observe/enforce mode plus, when recorded, the
// observe→enforce flip moment and the most recent change.
type ControlDecisionsPosture struct {
	Current         string     `json:"current"`
	BecameEnforceAt *time.Time `json:"became_enforce_at,omitempty"`
	BecameEnforceBy string     `json:"became_enforce_by,omitempty"`
	LastChangedAt   *time.Time `json:"last_changed_at,omitempty"`
}

type ControlDecisionsWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type ControlDecisions struct {
	Total     int                `json:"total"`
	Counts    map[string]int     `json:"counts"`
	Truncated bool               `json:"truncated"`
	Rows      []ProposalDecision `json:"rows"`
}

type ControlDecisionsOptions struct {
	From     time.Time
	To       time.Time
	Outcomes []string
	// Actor narrows the timeline to one proposing identity — the holder
	// of a card, when the page was reached from one. Empty means every
	// actor.
	Actor string
	Limit int
	// Offset paginates the returned rows (skip N, DESC created_at). The
	// per-outcome counts stay window-complete, so the page count is
	// derived from Counts + the active Outcomes filter.
	Offset int
}

// GetControlDecisionsBearer fetches the control-scoped decision timeline.
func (c *Client) GetControlDecisionsBearer(ctx context.Context, bearer, controlID string, opts ControlDecisionsOptions) (*ControlDecisionsResponse, error) {
	if controlID == "" {
		return nil, fmt.Errorf("apiclient: empty control id")
	}
	path := "/v1/me/controls/" + url.PathEscape(controlID) + "/decisions"
	q := url.Values{}
	if !opts.From.IsZero() {
		q.Set("from", opts.From.UTC().Format(time.RFC3339))
	}
	if !opts.To.IsZero() {
		q.Set("to", opts.To.UTC().Format(time.RFC3339))
	}
	for _, outcome := range opts.Outcomes {
		q.Add("outcome", outcome)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var resp ControlDecisionsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode control decisions: %w", err)
	}
	if resp.Decisions.Counts == nil {
		resp.Decisions.Counts = map[string]int{}
	}
	if resp.Decisions.Rows == nil {
		resp.Decisions.Rows = []ProposalDecision{}
	}
	return &resp, nil
}

// GetProofPackBearer fetches the raw proof-pack JSON for a control action.
func (c *Client) GetProofPackBearer(ctx context.Context, bearer, action, environment string) ([]byte, error) {
	if action == "" {
		return nil, fmt.Errorf("apiclient: empty proof-pack action")
	}
	path := "/v1/me/proof-pack/" + url.PathEscape(action)
	if environment != "" {
		q := url.Values{}
		q.Set("environment", environment)
		path += "?" + q.Encode()
	}
	resp, err := c.doRaw(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("apiclient: read proof pack: %w", err)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return body, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrReceiptNotFound
	default:
		return nil, fmt.Errorf("apiclient: GET %s unexpected status %d: %s",
			path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}
