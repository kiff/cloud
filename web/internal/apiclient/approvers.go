package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

type ApproverGrant struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Principal        string     `json:"principal"`
	CardID           string     `json:"card_id"`
	Worker           string     `json:"worker"`
	Action           string     `json:"action"`
	Parameter        string     `json:"parameter"`
	Max              int64      `json:"max"`
	ExpiresAt        time.Time  `json:"expires_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	LastSeenAt       *time.Time `json:"last_seen_at,omitempty"`
	ConnectionStatus string     `json:"connection_status,omitempty"`
}
type CreateApproverRequest struct {
	Name      string    `json:"name"`
	CardID    string    `json:"card_id"`
	Worker    string    `json:"worker"`
	Action    string    `json:"action"`
	Parameter string    `json:"parameter"`
	Max       int64     `json:"max"`
	ExpiresAt time.Time `json:"expires_at"`
}
type CreatedApprover struct {
	Grant      ApproverGrant `json:"grant"`
	Credential string        `json:"credential"`
}

func (c *Client) ListApproversBearer(ctx context.Context, bearer, card string) ([]ApproverGrant, error) {
	b, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/approver-grants?card_id="+url.QueryEscape(card), nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Grants []ApproverGrant `json:"grants"`
	}
	err = json.Unmarshal(b, &out)
	return out.Grants, err
}
func (c *Client) CreateApproverBearer(ctx context.Context, bearer string, req CreateApproverRequest) (CreatedApprover, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return CreatedApprover{}, err
	}
	b, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/approver-grants", bytes.NewReader(data))
	if err != nil {
		return CreatedApprover{}, err
	}
	var out CreatedApprover
	err = json.Unmarshal(b, &out)
	return out, err
}
func (c *Client) RevokeApproverBearer(ctx context.Context, bearer, id string) error {
	_, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/approver-grants/"+url.PathEscape(id)+"/revoke", nil)
	return err
}

type ExternalAnswer struct {
	GrantID    string    `json:"grant_id"`
	Principal  string    `json:"principal"`
	Name       string    `json:"name,omitempty"`
	Decision   string    `json:"decision"`
	ReasonCode string    `json:"reason_code"`
	Reason     string    `json:"reason,omitempty"`
	Model      string    `json:"model,omitempty"`
	At         time.Time `json:"at"`
}
