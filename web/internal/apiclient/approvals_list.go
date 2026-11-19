package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// approvals_list.go — apiclient method for the cloud's
// tenant-wide list-approvals endpoint. The dashboard's
// approval queue UX calls this on every render of the queue
// tab.

// PendingApproval is the apps/web view of one row from
// GET /v1/approvals. Fields mirror the framework's
// approval.Approval, surfaced flat so the templ partials can
// render them without further unwrapping.
//
// Status is always one of "pending" / "granted" / "denied" — the
// api side parses the query parameter against the framework's
// constants, so the wire shape is stable.
type PendingApproval struct {
	ID          string    `json:"id"`
	EntityID    string    `json:"entity_id"`
	EntityType  string    `json:"entity_type"`
	ActionName  string    `json:"action_name"`
	Status      string    `json:"status"`
	RequestedBy string    `json:"requested_by"`
	Reason      string    `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ReviewedAt  time.Time `json:"reviewed_at,omitempty"`
	ReviewedBy  string    `json:"reviewed_by,omitempty"`
}

// ApprovalsList is the response envelope from GET /v1/approvals.
// Approvals is the empty slice (never nil) when no rows match;
// LastReceivedAt is nil-zero when the slice is empty.
type ApprovalsList struct {
	Approvals      []PendingApproval `json:"approvals"`
	LastReceivedAt *time.Time        `json:"last_received_at,omitempty"`
}

// ApprovalsListOptions configures the list call. Both fields are
// optional and default to the api's documented defaults
// (status=pending, limit=50). The handler renders a "Pending"
// label by default; future tabs for "Granted" / "Denied" will
// pass the corresponding Status.
type ApprovalsListOptions struct {
	Status string // "pending" (default) | "granted" | "denied"
	Limit  int    // clamped server-side; 0 = use the server default
}

// ListApprovalsBearer fetches the tenant's approvals from
// GET /v1/approvals, returning the list + the last-received
// timestamp the dashboard's connection-status header reads.
func (c *Client) ListApprovalsBearer(ctx context.Context, bearer string, opts ApprovalsListOptions) (*ApprovalsList, error) {
	path := "/v1/approvals"
	q := ""
	if opts.Status != "" {
		q = "status=" + opts.Status
	}
	if opts.Limit > 0 {
		if q != "" {
			q += "&"
		}
		q += "limit=" + strconv.Itoa(opts.Limit)
	}
	if q != "" {
		path += "?" + q
	}

	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var out ApprovalsList
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode approvals list: %w", err)
	}
	if out.Approvals == nil {
		out.Approvals = []PendingApproval{}
	}
	return &out, nil
}
