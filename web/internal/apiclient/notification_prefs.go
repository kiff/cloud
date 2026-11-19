package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// notificationPrefsBody mirrors the API's GET/PUT
// /v1/me/notification-prefs envelope: a rule-key → enabled map.
type notificationPrefsBody struct {
	Prefs map[string]bool `json:"prefs"`
}

// GetNotificationPrefsBearer fetches the tenant's per-rule notification
// preferences (rule key → enabled). Absent rules default to enabled on
// the API side, so the returned map is complete.
func (c *Client) GetNotificationPrefsBearer(ctx context.Context, bearer string) (map[string]bool, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/notification-prefs", nil)
	if err != nil {
		return nil, err
	}
	var resp notificationPrefsBody
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode notification prefs: %w", err)
	}
	if resp.Prefs == nil {
		resp.Prefs = map[string]bool{}
	}
	return resp.Prefs, nil
}

// PutNotificationPrefsBearer persists the tenant's notification
// preferences and returns the stored (seeded) map.
func (c *Client) PutNotificationPrefsBearer(ctx context.Context, bearer string, prefs map[string]bool) (map[string]bool, error) {
	payload, err := json.Marshal(notificationPrefsBody{Prefs: prefs})
	if err != nil {
		return nil, fmt.Errorf("apiclient: encode notification prefs: %w", err)
	}
	body, err := c.do(ctx, bearer, http.MethodPut, "/v1/me/notification-prefs", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	var resp notificationPrefsBody
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode notification prefs: %w", err)
	}
	if resp.Prefs == nil {
		resp.Prefs = map[string]bool{}
	}
	return resp.Prefs, nil
}
