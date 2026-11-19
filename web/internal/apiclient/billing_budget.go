package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// GetBudgetBearer fetches the tenant's monthly governed-operation budget
// from GET /v1/me/billing-budget. The value is expressed in governed
// operations (not dollars); 0 means no budget is set. Used by the
// account-level consumption view to show the tenant's self-set budget
// alongside used-vs-allowance.
func (c *Client) GetBudgetBearer(ctx context.Context, bearer string) (int, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/billing-budget", nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		BudgetOps int `json:"budget_ops"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("apiclient: decode billing budget: %w", err)
	}
	if resp.BudgetOps < 0 {
		return 0, nil
	}
	return resp.BudgetOps, nil
}
