package apiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// ActivityCall is one call an agent made: its latest decision, whether
// any of its decisions held it for a person, and how many decisions it
// had.
type ActivityCall struct {
	ProposalDecision
	Held      bool `json:"held"`
	Decisions int  `json:"decisions"`
}

// ActivityCounts are how many calls each filter shows.
type ActivityCounts struct {
	All     int `json:"all"`
	Allowed int `json:"allowed"`
	Refused int `json:"refused"`
	Held    int `json:"held"`
}

// ActivityPage is GET /v1/me/activity: a page of calls, newest first;
// Shown is how many the filter selects in all.
type ActivityPage struct {
	Calls  []ActivityCall `json:"calls"`
	Counts ActivityCounts `json:"counts"`
	Shown  int            `json:"shown"`
	// Agents are every agent with a call in the account, whatever the
	// filter.
	Agents []string `json:"agents"`
}

// ActivityQuery narrows the calls: one agent, one call by proposal id,
// allowed / refused / held, and a page.
type ActivityQuery struct {
	Actor, Proposal, Show string
	Limit, Offset         int
}

// ActivityBearer reads GET /v1/me/activity.
func (c *Client) ActivityBearer(ctx context.Context, bearer string, q ActivityQuery) (ActivityPage, error) {
	v := url.Values{}
	if q.Actor != "" {
		v.Set("actor", q.Actor)
	}
	if q.Proposal != "" {
		v.Set("proposal", q.Proposal)
	}
	if q.Show != "" {
		v.Set("show", q.Show)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	path := "/v1/me/activity"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var page ActivityPage
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return page, err
	}
	err = json.Unmarshal(body, &page)
	return page, err
}
