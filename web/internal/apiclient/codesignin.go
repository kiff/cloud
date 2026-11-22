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
	"time"
)

// Signing in with a code sent by email (apps/api's codesignin). The web's
// two calls carry no credential: no one is signed in yet, and the API takes
// them only from the box.
var (
	// ErrCodeWrong: the code doesn't sign anyone in. The same for an
	// unknown address, codes off, a lapsed, used or wrong code.
	ErrCodeWrong = errors.New("apiclient: the code is wrong, used or lapsed")
	// ErrCodeSlowDown: too many tries from this address or IP address.
	ErrCodeSlowDown = errors.New("apiclient: too many code tries")
	// ErrCodeOff: KIFF has code sign-in off (no key), or the call didn't
	// come from the box.
	ErrCodeOff = errors.New("apiclient: code sign-in is off")
	// ErrCodeSecondFactor: the person signs in with a second factor, which
	// a code never stands in for.
	ErrCodeSecondFactor = errors.New("apiclient: the person signs in with a second factor")
)

// CodeAsk is a browser asking for a code: its flow (the web's sealed
// cookie), the client address the web trusts, and what the email says of
// where it was asked from.
type CodeAsk struct {
	Email, Flow, IP, Lang, Browser, Country string
}

// SendSignInCode asks KIFF to send a code: POST /v1/auth/code/send. The
// answer is the same whatever the address.
func (c *Client) SendSignInCode(ctx context.Context, ask CodeAsk) error {
	_, err := c.codeCall(ctx, "/v1/auth/code/send", map[string]string{"email": ask.Email, "flow": ask.Flow, "ip": ask.IP,
		"lang": ask.Lang, "browser": ask.Browser, "country": ask.Country})
	return err
}

// VerifySignInCode checks a code and answers the sign-in provider's
// one-time sign-in token for its person: POST /v1/auth/code/verify.
func (c *Client) VerifySignInCode(ctx context.Context, email, flow, code, ip string) (string, error) {
	body, err := c.codeCall(ctx, "/v1/auth/code/verify", map[string]string{"email": email, "flow": flow, "code": code, "ip": ip})
	if err != nil {
		return "", err
	}
	var out struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Ticket == "" {
		return "", errors.New("apiclient: verify: no ticket in the answer")
	}
	return out.Ticket, nil
}

func (c *Client) codeCall(ctx context.Context, path string, in map[string]string) ([]byte, error) {
	if c == nil {
		return nil, errors.New("apiclient: nil client")
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("apiclient: build code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// Never the request: it carries the address and maybe the code.
		return nil, fmt.Errorf("apiclient: %s: unreachable", path)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return body, nil
	case http.StatusTooManyRequests:
		return nil, ErrCodeSlowDown
	case http.StatusForbidden:
		return nil, ErrCodeSecondFactor
	case http.StatusNotFound:
		return nil, ErrCodeOff
	case http.StatusBadRequest:
		if strings.Contains(string(body), "code_wrong") {
			return nil, ErrCodeWrong
		}
		return nil, fmt.Errorf("apiclient: %s: refused (400)", path)
	}
	return nil, fmt.Errorf("apiclient: %s: status %d", path, resp.StatusCode)
}

// SignInSetting is the person's: whether they sign in with a code sent by
// email. Available is false when KIFF can't send codes.
type SignInSetting struct {
	EmailCode bool       `json:"email_code"`
	ChangedAt *time.Time `json:"changed_at,omitempty"`
	Available bool       `json:"available"`
}

// SignInSetting reads GET /v1/me/sign-in.
func (c *Client) SignInSetting(ctx context.Context, bearer string) (SignInSetting, error) {
	var out SignInSetting
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/sign-in", nil)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(body, &out)
}

// SetSignInCode turns the person's codes on or off: PUT /v1/me/sign-in.
// On needs a recent sign-in: ErrReauthRequired.
func (c *Client) SetSignInCode(ctx context.Context, bearer string, on bool) (SignInSetting, error) {
	var out SignInSetting
	payload, _ := json.Marshal(map[string]bool{"email_code": on})
	resp, err := c.doRaw(ctx, bearer, http.MethodPut, "/v1/me/sign-in", bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusOK:
		return out, json.Unmarshal(body, &out)
	case resp.StatusCode == http.StatusUnauthorized:
		return out, ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "reauth_required"):
		return out, ErrReauthRequired
	case resp.StatusCode == http.StatusServiceUnavailable:
		return out, ErrCodeOff
	case resp.StatusCode == http.StatusConflict && strings.Contains(string(body), "second_factor"):
		return out, ErrCodeSecondFactor
	}
	return out, fmt.Errorf("apiclient: PUT /v1/me/sign-in: status %d", resp.StatusCode)
}
