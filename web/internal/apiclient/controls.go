package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Control mirrors the apps/api controlResponse wire shape from
// GET /v1/me/controls.
type Control struct {
	ID          string    `json:"id"`
	Domain      string    `json:"domain"`
	Entity      string    `json:"entity"`
	Actions     []string  `json:"actions"`
	Environment string    `json:"environment"`
	Workflow    string    `json:"workflow"`
	ActivatedAt time.Time `json:"activated_at"`
	ActivatedBy string    `json:"activated_by"`
	Status      string    `json:"status"` // "active" or "superseded"
}

// controlsListResponse mirrors the GET /v1/me/controls envelope.
type controlsListResponse struct {
	Controls []Control `json:"controls"`
}

// ListControlsBearer fetches all control records for the caller's
// tenant, newest first.
func (c *Client) ListControlsBearer(ctx context.Context, bearer string) ([]Control, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/controls", nil)
	if err != nil {
		return nil, err
	}
	var resp controlsListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode controls: %w", err)
	}
	if resp.Controls == nil {
		resp.Controls = []Control{}
	}
	return resp.Controls, nil
}
