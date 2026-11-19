package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ActivitySeriesResponse mirrors GET
// /v1/me/domains/{domain}/activity/series: the resolved window, the
// bucket width the server picked for that span, and the non-empty
// per-outcome buckets in ascending time order. The web fills gaps and
// draws the chart natively (the web-stack design, no JS lib).
type ActivitySeriesResponse struct {
	Domain        string           `json:"domain"`
	ControlID     string           `json:"control_id"`
	Actor         string           `json:"actor,omitempty"`
	From          time.Time        `json:"from"`
	To            time.Time        `json:"to"`
	BucketSeconds int              `json:"bucket_seconds"`
	Buckets       []ActivityBucket `json:"buckets"`
}

// ActivityBucket is one interval of the requests-over-time series.
type ActivityBucket struct {
	Start  time.Time      `json:"start"`
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
}

// ActivitySeriesOptions scopes a series read. Actor optionally narrows
// to one proposing identity (the per-runtime slice).
type ActivitySeriesOptions struct {
	From  time.Time
	To    time.Time
	Actor string
}

// GetActivitySeriesBearer fetches the domain's requests-over-time
// series. A 404 (unknown domain / no active control) surfaces as
// ErrNoDomain via do(); callers render the honest empty state.
func (c *Client) GetActivitySeriesBearer(ctx context.Context, bearer, domain string, opts ActivitySeriesOptions) (*ActivitySeriesResponse, error) {
	if domain == "" {
		return nil, fmt.Errorf("apiclient: empty domain")
	}
	path := "/v1/me/domains/" + url.PathEscape(domain) + "/activity/series"
	q := url.Values{}
	if !opts.From.IsZero() {
		q.Set("from", opts.From.UTC().Format(time.RFC3339))
	}
	if !opts.To.IsZero() {
		q.Set("to", opts.To.UTC().Format(time.RFC3339))
	}
	if opts.Actor != "" {
		q.Set("actor", opts.Actor)
	}
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		// The shared do() collapses any 404 to ErrReceiptNotFound; for
		// this surface a 404 means "no active control for this domain".
		if errors.Is(err, ErrReceiptNotFound) {
			return nil, ErrNoDomain
		}
		return nil, err
	}
	var resp ActivitySeriesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("apiclient: decode activity series: %w", err)
	}
	if resp.Buckets == nil {
		resp.Buckets = []ActivityBucket{}
	}
	return &resp, nil
}
