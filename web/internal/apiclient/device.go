package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrDeviceCodeUnknown is returned by ApproveDeviceBearer when the
// API reports the user_code is unknown or expired (404). The device
// page surfaces it as a "check the code / it may have expired" hint
// rather than a generic failure.
var ErrDeviceCodeUnknown = errors.New("apiclient: device code unknown or expired")

// ErrReauthRequired is returned by ApproveDeviceBearer when approving a
// device needs a recent sign-in (403 reauth_required): the page asks the
// person to sign in again and brings them back to the same code.
var ErrReauthRequired = errors.New("apiclient: approving a device needs a recent sign-in")

// IsReauthRequired reports whether err is the API's 403 reauth_required:
// the change needs a recent sign-in. It reads ErrReauthRequired, and a
// refusal from the generic request path, which keeps the API's body in
// the ErrForbidden it returns.
func IsReauthRequired(err error) bool {
	if errors.Is(err, ErrReauthRequired) {
		return true
	}
	return errors.Is(err, ErrForbidden) && strings.Contains(err.Error(), `"error":"reauth_required"`)
}

// ErrDeviceCodeApproved is returned by DevicePendingBearer when the code
// was already approved (409): the device signs in by itself, and the page
// says so rather than calling the code expired.
var ErrDeviceCodeApproved = errors.New("apiclient: device code already approved")

// ApproveDeviceBearer approves a pending device-authorization flow
// on the signed-in user's behalf: POST /v1/auth/device/approve with
// the user_code, authenticated by the caller's session bearer. On
// success the API binds the caller's subject+tenant to the flow and
// mints the developer session the CLI then polls for.
//
// Errors:
//   - empty bearer or user_code: an error without a request.
//   - 401 → ErrUnauthorized (bounce to /sign-in).
//   - 404 → ErrDeviceCodeUnknown (unknown or expired code).
//   - 403 reauth_required → ErrReauthRequired (sign in again first).
//   - other non-2xx → a wrapping error with the body snippet.
func (c *Client) ApproveDeviceBearer(ctx context.Context, bearer, userCode, label string) error {
	if c == nil {
		return errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return errors.New("apiclient: empty bearer")
	}
	if strings.TrimSpace(userCode) == "" {
		return errors.New("apiclient: empty user_code")
	}
	payload, err := json.Marshal(map[string]string{"user_code": userCode, "label": label})
	if err != nil {
		return fmt.Errorf("apiclient: marshal approve body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/auth/device/approve", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("apiclient: build device approve request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: device approve: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return ErrDeviceCodeUnknown
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode == http.StatusForbidden {
			var refusal struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(body, &refusal) == nil && refusal.Error == "reauth_required" {
				return ErrReauthRequired
			}
		}
		return fmt.Errorf("apiclient: device approve unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// DevicePending is what a device sign-in will bind, shown before the
// person approves it: the device's name and its key's fingerprint.
type DevicePending struct {
	DeviceName     string `json:"device_name"`
	HasKey         bool   `json:"has_key"`
	KeyFingerprint string `json:"key_fingerprint"`
	// Relink: a phone already signed in asks to approve again with a new
	// approval key (its faces or fingerprints changed), on its own account.
	Relink                 bool   `json:"relink"`
	ApprovalKeyFingerprint string `json:"approval_key_fingerprint"`
	Tenant                 string `json:"tenant"`
	// RecentSignIn: approving now would pass the API's recent sign-in
	// check. Nil from an API that doesn't say: approve finds out.
	RecentSignIn *bool `json:"recent_sign_in"`
}

// DevicePendingBearer looks up a pending device sign-in by its user code.
// ErrDeviceCodeUnknown when the code is unknown or expired, and
// ErrDeviceCodeApproved when it was already approved.
func (c *Client) DevicePendingBearer(ctx context.Context, bearer, userCode string) (DevicePending, error) {
	if c == nil {
		return DevicePending{}, errors.New("apiclient: nil client")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/auth/device/pending?user_code="+url.QueryEscape(userCode), nil)
	if err != nil {
		return DevicePending{}, fmt.Errorf("apiclient: build device pending request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return DevicePending{}, fmt.Errorf("apiclient: device pending: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		var p DevicePending
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&p); err != nil {
			return DevicePending{}, fmt.Errorf("apiclient: decode device pending: %w", err)
		}
		return p, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return DevicePending{}, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return DevicePending{}, ErrDeviceCodeUnknown
	case resp.StatusCode == http.StatusConflict:
		return DevicePending{}, ErrDeviceCodeApproved
	default:
		return DevicePending{}, fmt.Errorf("apiclient: device pending unexpected status %d", resp.StatusCode)
	}
}
