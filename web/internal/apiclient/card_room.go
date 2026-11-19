package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

type RoomGrant struct {
	ID                            string
	Amount                        int64
	Duration                      string
	At, ConfirmedAt, ExpiresAt    time.Time
	By, Measure, Window, Location string
}

type EffectRelease struct {
	TenantID, MandateID, ProposalID, QuantityKey string
	Amount                                       int64
	At                                           time.Time
	Source, EvidenceID, Reason                   string
}

type ExtraRoomRequest struct {
	ID           string    `json:"revision_id"`
	Quantity     string    `json:"quantity"`
	Window       string    `json:"window"`
	Measure      string    `json:"measure"`
	Amount       int64     `json:"amount"`
	Duration     string    `json:"duration"`
	Until        time.Time `json:"until"`
	WindowEnd    time.Time `json:"window_end"`
	ExpectedBase *int64    `json:"expected_base_limit"`
}

func (c *Client) GrantExtraRoomBearer(ctx context.Context, bearer, id string, req ExtraRoomRequest) (*Mandate, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, bearer, http.MethodPost, "/v1/me/mandates/"+url.PathEscape(id)+"/extra-room", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var m Mandate
	err = json.Unmarshal(raw, &m)
	return &m, err
}
