// Package gatewayclient is apps/web's client for the KIFF MCP gateway's
// connection routes (the cards-for-any-agent design, /v1/connections).
// The dashboard calls them with the signed-in owner's own session
// bearer; the gateway checks it through apps/api, so this package holds
// no key of its own.
//
// Tool credentials pass through on the way to the gateway and are never
// read back: the gateway does not return them.
package gatewayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kiff/cloud/web/internal/apiclient"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Error is a refusal from the gateway. Message is the gateway's own
// sentence, written for the account owner, with any credential redacted.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrUnavailable: the gateway could not be reached or answered with
// something other than its documented JSON.
var ErrUnavailable = errors.New("the KIFF gateway is not reachable right now; nothing was changed")

// Client calls the gateway. Concurrent-safe.
type Client struct {
	base string
	hc   *http.Client
}

// New returns a client for the gateway at base (http or https), or an
// error when base is not an absolute URL.
func New(base string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("gatewayclient: base URL must be an absolute http(s) URL, got %q", base)
	}
	// Connecting a tool lists the remote server's tools first, which can
	// take a while on a slow server.
	return &Client{base: u.String(), hc: &http.Client{Timeout: 45 * time.Second}}, nil
}

// Connection is a connected tool, as the gateway lists it.
type Connection struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	URL            string `json:"url"`
	Tool           string `json:"tool"`
	AmountArg      string `json:"amount_argument"`
	AmountUnit     string `json:"amount_unit,omitempty"`
	AmountScale    int    `json:"amount_scale,omitempty"`
	IdempotencyArg string `json:"idempotency_argument"`
	// ResourceArg is the string argument that says what a call is
	// on; a Card can name which values it covers.
	ResourceArg       string `json:"resource_argument,omitempty"`
	OutcomeContract   string `json:"outcome_contract,omitempty"`
	OutcomeLookupTool string `json:"outcome_lookup_tool,omitempty"`
	// AmountPath, ResourcePath and FixedArgs: one operation of a generic
	// tool. The paths say where the amount and the resource sit; the
	// fixed arguments are sent on every call.
	AmountPath    string          `json:"amount_path,omitempty"`
	ResourcePath  string          `json:"resource_path,omitempty"`
	FixedArgs     json.RawMessage `json:"fixed_arguments,omitempty"`
	ReadOnly      bool            `json:"read_only"`
	HasCredential bool            `json:"has_credential"`
	CreatedAt     time.Time       `json:"created_at"`
	// Mode is who holds the tool's credential: sealed, verify or relay.
	// Audience is what the tool or relay expects in KIFF's execution
	// permit.
	Mode     string `json:"mode"`
	Audience string `json:"audience"`
	// InputSchema is the tool's JSON schema, as the server listed it.
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ResourceChoices are the arguments that can say what a call is on: the
// schema's string properties, other than the idempotency argument and
// KIFF's own operation argument. The gateway checks the choice again.
func (c Connection) ResourceChoices() []string {
	var s struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
	}
	if json.Unmarshal(c.InputSchema, &s) != nil {
		return nil
	}
	var out []string
	for name, p := range s.Properties {
		if name == c.IdempotencyArg || name == c.AmountArg || name == "kiff_operation_id" || !hasType(p.Type, "string") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func hasType(t any, want string) bool {
	switch v := t.(type) {
	case string:
		return v == want
	case []any:
		for _, x := range v {
			if x == want {
				return true
			}
		}
	}
	return false
}

// Host is the tool server's host, for display.
func (c Connection) Host() string {
	if u, err := url.Parse(c.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return c.URL
}

// DiscoveredTool is a tool a server lists, with the arguments a Card can
// sum (integers) and the ones KIFF can fill with an idempotency key
// (strings).
type DiscoveredTool struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	IntegerArgs []string `json:"integer_arguments"`
	StringArgs  []string `json:"string_arguments"`
}

// NewConnection is what the owner chose to connect.
type NewConnection struct {
	Name              string          `json:"name"`
	URL               string          `json:"url"`
	Tool              string          `json:"tool"`
	AmountArg         string          `json:"amount_argument,omitempty"`
	AmountUnit        string          `json:"amount_unit,omitempty"`
	AmountScale       *int            `json:"amount_scale,omitempty"`
	IdempotencyArg    string          `json:"idempotency_argument,omitempty"`
	ResourceArg       string          `json:"resource_argument,omitempty"`
	OutcomeContract   string          `json:"outcome_contract,omitempty"`
	OutcomeLookupTool string          `json:"outcome_lookup_tool,omitempty"`
	AmountPath        string          `json:"amount_path,omitempty"`
	ResourcePath      string          `json:"resource_path,omitempty"`
	FixedArgs         json.RawMessage `json:"fixed_arguments,omitempty"`
	Credential        string          `json:"credential,omitempty"`
	ReadOnly          bool            `json:"read_only"`
}

// List returns the account's connected tools.
func (c *Client) List(ctx context.Context, bearer string) ([]Connection, error) {
	var out struct {
		Connections []Connection `json:"connections"`
	}
	if err := c.do(ctx, bearer, http.MethodGet, "/v1/connections", nil, &out); err != nil {
		return nil, err
	}
	return out.Connections, nil
}

// Discover lists the tools at serverURL with credential. The gateway
// stores nothing. It returns the URL as the gateway normalised it.
func (c *Client) Discover(ctx context.Context, bearer, serverURL, credential string) (string, []DiscoveredTool, error) {
	var out struct {
		URL   string           `json:"url"`
		Tools []DiscoveredTool `json:"tools"`
	}
	err := c.do(ctx, bearer, http.MethodPost, "/v1/connections/discover", map[string]string{"url": serverURL, "credential": credential}, &out)
	return out.URL, out.Tools, err
}

// Create connects a tool.
func (c *Client) Create(ctx context.Context, bearer string, n NewConnection) (Connection, error) {
	var out Connection
	err := c.do(ctx, bearer, http.MethodPost, "/v1/connections", n, &out)
	return out, err
}

// Test reaches the tool with its stored credential. ok false with a
// message is a failed test, not an error.
func (c *Client) Test(ctx context.Context, bearer, name string) (ok bool, message string, err error) {
	var out struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := c.do(ctx, bearer, http.MethodPost, "/v1/connections/"+url.PathEscape(name)+"/test", nil, &out); err != nil {
		return false, "", err
	}
	if out.OK {
		return true, out.Message, nil
	}
	return false, out.Error, nil
}

// SetCredential replaces a tool's credential; the gateway keeps the old
// one unless the new one works.
func (c *Client) SetCredential(ctx context.Context, bearer, name, credential string) error {
	return c.do(ctx, bearer, http.MethodPut, "/v1/connections/"+url.PathEscape(name)+"/credential", map[string]string{"credential": credential}, nil)
}

// ModeChange moves a tool to another custody mode.
type ModeChange struct {
	Mode       string `json:"mode"`
	Audience   string `json:"audience,omitempty"`
	URL        string `json:"url,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// SetMode changes who holds a tool's credential. The gateway reaches the
// tool the new way before anything changes.
func (c *Client) SetMode(ctx context.Context, bearer, name string, m ModeChange) error {
	return c.do(ctx, bearer, http.MethodPut, "/v1/connections/"+url.PathEscape(name)+"/mode", m, nil)
}

// SettingsChange is what KIFF checks on a connected tool. A nil field
// keeps its value.
type SettingsChange struct {
	ResourceArg     *string `json:"resource_argument,omitempty"`
	OutcomeContract *string `json:"outcome_contract,omitempty"`
}

// SetSettings changes a connected tool's settings without reconnecting it.
func (c *Client) SetSettings(ctx context.Context, bearer, name string, s SettingsChange) error {
	return c.do(ctx, bearer, http.MethodPut, "/v1/connections/"+url.PathEscape(name)+"/settings", s, nil)
}

// Remove disconnects a tool.
func (c *Client) Remove(ctx context.Context, bearer, name string) error {
	return c.do(ctx, bearer, http.MethodDelete, "/v1/connections/"+url.PathEscape(name), nil, nil)
}

func (c *Client) do(ctx context.Context, bearer, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// The account the dashboard has selected; the gateway forwards
	// it to apps/api, which checks the membership.
	if t := apiclient.TenantFrom(ctx); t != "" {
		req.Header.Set(apiclient.TenantHeader, t)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return ErrUnavailable
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) != nil || strings.TrimSpace(e.Error) == "" {
			return ErrUnavailable
		}
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return ErrUnavailable
	}
	return nil
}
