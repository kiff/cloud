package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type WarningSettings struct {
	Runway            bool `json:"runway"`
	Spent             bool `json:"spent"`
	Unusual           bool `json:"unusual"`
	Burst             bool `json:"burst"`
	RunwayPercent     int  `json:"runway_percent"`
	UnusualMultiplier int  `json:"unusual_multiplier"`
	BurstPercent      int  `json:"burst_percent"`
}
type WarningConfig struct {
	Revision int64           `json:"revision"`
	At       time.Time       `json:"at"`
	By       string          `json:"by"`
	Settings WarningSettings `json:"settings"`
}
type CardForecast struct {
	State              string     `json:"state"`
	ObservedAt         time.Time  `json:"observed_at"`
	ObservationSeconds int64      `json:"observation_seconds"`
	RunsOutAt          *time.Time `json:"runs_out_at"`
	ResetsAt           *time.Time `json:"resets_at"`
}
type CardWarning struct {
	ResolvedAt *time.Time `json:"resolved_at"`
	ID         string     `json:"id"`
	CardID     string     `json:"card_id"`
	Kind       string     `json:"kind"`
	Quantity   string     `json:"quantity"`
	At         time.Time  `json:"at"`
	Reason     string     `json:"reason"`
}

func (c *Client) CardWarningSettingsBearer(ctx context.Context, bearer, id string) (*WarningConfig, error) {
	raw, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/mandates/"+url.PathEscape(id)+"/warnings", nil)
	if err != nil {
		return nil, err
	}
	var config WarningConfig
	err = json.Unmarshal(raw, &config)
	return &config, err
}
func (c *Client) SetCardWarningSettingsBearer(ctx context.Context, bearer, id string, config WarningConfig) error {
	raw, err := json.Marshal(map[string]any{"revision": config.Revision, "settings": config.Settings})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, bearer, http.MethodPut, "/v1/me/mandates/"+url.PathEscape(id)+"/warnings", bytes.NewReader(raw))
	return err
}

func (c *Client) CardWarningAttentionBearer(ctx context.Context, bearer string, page int) (AttentionFeed, error) {
	return c.AttentionKindBearer(ctx, bearer, "card_warning", page)
}

// AttentionKindBearer reads one kind of open work, a page of 100:
// card_warning, approval or blocked.
func (c *Client) AttentionKindBearer(ctx context.Context, bearer, kind string, page int) (AttentionFeed, error) {
	raw, err := c.do(ctx, bearer, http.MethodGet, "/v1/me/attention?kind="+url.QueryEscape(kind)+"&page="+strconv.Itoa(page), nil)
	if err != nil {
		return AttentionFeed{}, err
	}
	var feed AttentionFeed
	err = json.Unmarshal(raw, &feed)
	return feed, err
}
