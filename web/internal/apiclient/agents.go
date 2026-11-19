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

// Agent is one agent from GET /v1/me/agents: a record the owner added,
// or a name the agent already has implicitly. Sources says where it was
// seen: "record", "card", "key", "runtime".
type Agent struct {
	ID         string     `json:"id"`
	Recorded   bool       `json:"recorded"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	Sources    []string   `json:"sources"`
}

// ErrAgentExists is returned by CreateAgentBearer when the tenant
// already has an agent by that name.
var ErrAgentExists = errors.New("apiclient: agent already exists")

// ListAgentsBearer returns the tenant's agents.
func (c *Client) ListAgentsBearer(ctx context.Context, bearer string) ([]Agent, error) {
	body, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/agents", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode agents: %w", err)
	}
	return resp.Agents, nil
}

// CreateAgentBearer adds an agent named id. ErrAgentExists on 409.
func (c *Client) CreateAgentBearer(ctx context.Context, bearer, id string) (*Agent, error) {
	payload, _ := json.Marshal(map[string]string{"id": id})
	resp, err := c.doRaw(ctx, bearer, http.MethodPost, "/v1/me/agents", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusConflict:
		return nil, ErrAgentExists
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := strings.TrimSpace(e.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return nil, fmt.Errorf("apiclient: add agent: status %d: %s", resp.StatusCode, msg)
	}
	var a Agent
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("apiclient: decode agent: %w", err)
	}
	return &a, nil
}
