package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrAppSignInGone: the app's sign-in request is unknown, used or expired
// (410), so the person starts again from the app.
var ErrAppSignInGone = errors.New("apiclient: this app sign-in has expired")

// ErrAppSignInOff: signing in inside the tafs apps is off (404).
var ErrAppSignInOff = errors.New("apiclient: app sign-in is off")

// ApproveAppSignIn answers the tafs app's sign-in request as the signed-in
// person: POST /v1/auth/app/approve with {request}. It returns where the
// sheet goes next, the app's redirect with a code and its state. The API
// takes the request once.
//
// Errors: 401 → ErrUnauthorized; 403 reauth_required → ErrReauthRequired
// (the request is kept: sign in again and come back); 410 →
// ErrAppSignInGone; 404 → ErrAppSignInOff.
func (c *Client) ApproveAppSignIn(ctx context.Context, bearer, request string) (string, error) {
	return c.appSignIn(ctx, bearer, request, "/v1/auth/app/approve")
}

// DenyAppSignIn closes the request as the person's cancel: the sheet goes to
// the app's redirect with access_denied. Errors as ApproveAppSignIn.
func (c *Client) DenyAppSignIn(ctx context.Context, bearer, request string) (string, error) {
	return c.appSignIn(ctx, bearer, request, "/v1/auth/app/deny")
}

func (c *Client) appSignIn(ctx context.Context, bearer, request, path string) (string, error) {
	if c == nil {
		return "", errors.New("apiclient: nil client")
	}
	if strings.TrimSpace(bearer) == "" {
		return "", errors.New("apiclient: empty bearer")
	}
	if strings.TrimSpace(request) == "" {
		return "", errors.New("apiclient: empty request")
	}
	payload, err := json.Marshal(map[string]string{"request": request})
	if err != nil {
		return "", fmt.Errorf("apiclient: marshal app sign-in body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("apiclient: build app sign-in request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("apiclient: app sign-in: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			RedirectTo string `json:"redirect_to"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.RedirectTo == "" {
			return "", fmt.Errorf("apiclient: app sign-in: no redirect_to in %q", strings.TrimSpace(string(body)))
		}
		return out.RedirectTo, nil
	case http.StatusUnauthorized:
		return "", ErrUnauthorized
	case http.StatusGone:
		return "", ErrAppSignInGone
	case http.StatusNotFound:
		return "", ErrAppSignInOff
	case http.StatusForbidden:
		var refusal struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &refusal) == nil && refusal.Error == "reauth_required" {
			return "", ErrReauthRequired
		}
	}
	return "", fmt.Errorf("apiclient: app sign-in unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
