package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DeleteAccountBearer requests account erasure via
// POST /v1/me/delete (admin role required). Returns the parsed
// delete response on success. 401 maps to ErrUnauthorized; 403
// means the caller lacks admin role; 410 means the tenant has
// already been hard-deleted.
func (c *Client) DeleteAccountBearer(ctx context.Context, bearer string) (*DeleteAccountResponse, error) {
	body, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/delete", nil)
	if err != nil {
		return nil, err
	}
	var resp DeleteAccountResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode delete response: %w", err)
	}
	return &resp, nil
}

// DeleteAccountResponse is the parsed body of POST /v1/me/delete.
type DeleteAccountResponse struct {
	Status          string `json:"status"`
	DeletedAt       string `json:"deleted_at"`
	GraceWindowEnds string `json:"grace_window_ends"`
	Message         string `json:"message"`
}

// GET /v1/me/export and returns the raw JSON bytes so the web
// handler can stream them to the browser as a download.
//
// Unlike the shared do() helper, this reads the entire body with
// no 1MB cap — an export can legitimately be larger than the
// dashboard's other read responses. 401 maps to ErrUnauthorized
// so the handler can bounce to sign-in, matching every other
// bearer call's contract.
func (c *Client) ExportBearer(ctx context.Context, bearer string) ([]byte, error) {
	resp, err := c.doRaw(ctx, bearer, http.MethodGet, "/v1/me/export", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("apiclient: read export body: %w", err)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return body, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	default:
		return nil, fmt.Errorf("apiclient: GET /v1/me/export unexpected status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
}
