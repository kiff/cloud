package kiffapi

import (
	"context"
	"net/http"
	"net/url"
)

// The request carries no result claim. apps/api reads the gateway's persisted
// evidence and verifies the operation against its own authorization ledger.
func (c *Client) ReconcileEffects(ctx context.Context, key, proposal string) error {
	status, raw, err := c.do(ctx, key, http.MethodPost, "/v1/me/card-effects/"+url.PathEscape(proposal)+"/reconcile", map[string]any{})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return apiError(status, raw)
	}
	return nil
}

// SurchargeEffects asks apps/api to count what a vendor used beyond the
// decided amount, from the gateway's own verify read of the call; the
// request carries no amount.
func (c *Client) SurchargeEffects(ctx context.Context, key, proposal string) error {
	status, raw, err := c.do(ctx, key, http.MethodPost, "/v1/me/card-effects/"+url.PathEscape(proposal)+"/surcharge", map[string]any{})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return apiError(status, raw)
	}
	return nil
}
