package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// OAuthRequest is a connection request waiting for consent: what the
// consent page shows.
type OAuthRequest struct {
	RequestID       string `json:"request_id"`
	ClientName      string `json:"client_name"`
	RedirectHost    string `json:"redirect_host"`
	Verified        bool   `json:"verified"`
	PreviousAgentID string `json:"previous_agent_id"`
	CanConsent      bool   `json:"can_consent"`
	// AgentID is set when the request came through a connect link: the
	// agent is fixed, and the page only asks whether to allow it.
	AgentID string `json:"agent_id"`
	// WrongAccount: the link belongs to another account.
	WrongAccount bool `json:"wrong_account"`
}

// OAuthRequestBearer reads a pending connection request.
func (c *Client) OAuthRequestBearer(ctx context.Context, bearer, requestID string) (*OAuthRequest, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/oauth/requests/"+url.PathEscape(requestID), nil)
	if err != nil {
		return nil, err
	}
	var out OAuthRequest
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode oauth request: %w", err)
	}
	return &out, nil
}

// OAuthConsentBearer allows the requesting client and returns where to
// send the browser (the client, with a code). Through a connect link the
// agent is the link's and both names are ignored; otherwise agentID is an
// existing agent, or newAgent names one the API creates.
func (c *Client) OAuthConsentBearer(ctx context.Context, bearer, requestID, agentID, newAgent string) (string, error) {
	return c.oauthAnswer(ctx, bearer, "/v1/oauth/consent",
		map[string]string{"request_id": requestID, "agent_id": agentID, "new_agent": newAgent})
}

// OAuthDenyBearer declines the request and returns where to send the
// browser (the client, with access_denied).
func (c *Client) OAuthDenyBearer(ctx context.Context, bearer, requestID string) (string, error) {
	return c.oauthAnswer(ctx, bearer, "/v1/oauth/deny", map[string]string{"request_id": requestID})
}

func (c *Client) oauthAnswer(ctx context.Context, bearer, path string, payload map[string]string) (string, error) {
	b, _ := json.Marshal(payload)
	body, err := c.do(ctx, bearer, http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.RedirectTo == "" {
		return "", fmt.Errorf("apiclient: decode oauth answer: %v", err)
	}
	return out.RedirectTo, nil
}

// ConnectLink is an agent's connect link: a gateway URL that names the
// agent, so an app connected with it acts as that agent after an admin or
// editor approves.
type ConnectLink struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agent_id"`
	URL       string     `json:"url"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// ConnectLinksBearer lists an agent's connect links, newest first.
func (c *Client) ConnectLinksBearer(ctx context.Context, bearer, agentID string) ([]ConnectLink, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/agents/"+url.PathEscape(agentID)+"/connect-links", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Links []ConnectLink `json:"links"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode connect links: %w", err)
	}
	return out.Links, nil
}

// CreateConnectLinkBearer creates a connect link for agentID.
func (c *Client) CreateConnectLinkBearer(ctx context.Context, bearer, agentID string) (*ConnectLink, error) {
	body, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/agents/"+url.PathEscape(agentID)+"/connect-links", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	var out ConnectLink
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode connect link: %w", err)
	}
	return &out, nil
}

// RevokeConnectLinkBearer revokes a link; its connections end.
func (c *Client) RevokeConnectLinkBearer(ctx context.Context, bearer, linkID string) error {
	_, err := c.do(ctx, bearer, http.MethodPost, "/v1/oauth/connect-links/"+url.PathEscape(linkID)+"/revoke", bytes.NewReader([]byte("{}")))
	return err
}

// DeviceInfo is a device code waiting for a person.
type DeviceInfo struct {
	UserCode        string `json:"user_code"`
	ClientName      string `json:"client_name"`
	CanConsent      bool   `json:"can_consent"`
	AgentID         string `json:"agent_id"`
	WrongAccount    bool   `json:"wrong_account"`
	PreviousAgentID string `json:"previous_agent_id"`
}

// DeviceInfoBearer reads a pending device code.
func (c *Client) DeviceInfoBearer(ctx context.Context, bearer, userCode string) (*DeviceInfo, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/oauth/device/"+url.PathEscape(userCode), nil)
	if err != nil {
		return nil, err
	}
	var out DeviceInfo
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode device info: %w", err)
	}
	return &out, nil
}

// DeviceApproveBearer allows a device code, as agentID or a new agent.
func (c *Client) DeviceApproveBearer(ctx context.Context, bearer, userCode, agentID, newAgent string) error {
	b, _ := json.Marshal(map[string]string{"user_code": userCode, "agent_id": agentID, "new_agent": newAgent})
	_, err := c.do(ctx, bearer, http.MethodPost, "/v1/oauth/device/approve", bytes.NewReader(b))
	return err
}

// DeviceDenyBearer declines a device code.
func (c *Client) DeviceDenyBearer(ctx context.Context, bearer, userCode string) error {
	b, _ := json.Marshal(map[string]string{"user_code": userCode})
	_, err := c.do(ctx, bearer, http.MethodPost, "/v1/oauth/device/deny", bytes.NewReader(b))
	return err
}

// Connection is one OAuth connection.
type Connection struct {
	ID          string     `json:"id"`
	AgentID     string     `json:"agent_id"`
	ClientName  string     `json:"client_name"`
	Method      string     `json:"method"`
	LinkID      string     `json:"link_id"`
	KeyID       string     `json:"key_id"`
	GrantedBy   string     `json:"granted_by"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  time.Time  `json:"last_used_at"`
	EndedAt     *time.Time `json:"ended_at"`
	EndedReason string     `json:"ended_reason"`
	Status      string     `json:"status"`
}

// ConnectionsBearer lists the account's OAuth connections.
func (c *Client) ConnectionsBearer(ctx context.Context, bearer string) ([]Connection, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/oauth/connections", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Connections []Connection `json:"connections"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode connections: %w", err)
	}
	return out.Connections, nil
}

// RevokeGrantBearer ends one connection; its key is revoked.
func (c *Client) RevokeGrantBearer(ctx context.Context, bearer, grantID string) error {
	_, err := c.do(ctx, bearer, http.MethodPost, "/v1/oauth/grants/"+url.PathEscape(grantID)+"/revoke", bytes.NewReader([]byte("{}")))
	return err
}
