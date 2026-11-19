package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ReviewRatio is the share of an agent's proposed actions that needed
// nobody (GET /v1/me/review-ratio).
//
// The arithmetic lives in the api and is not repeated here. The rules
// that make the figure honest — a refusal is not review removed, our
// own billing limit is not their operation, a thin window declines to
// answer — are decisions rather than arithmetic, and a second
// implementation of them is a second place for them to drift.
type ReviewRatio struct {
	// Status is "ok" or "insufficient_sample". Branch on this, never
	// on Percent: zero percent means every action needed a person,
	// which is a real answer, and an insufficient window means we
	// decline to give one.
	Status  string `json:"status"`
	Percent int    `json:"percent"`

	WouldRun          int `json:"would_run"`
	WouldReachAPerson int `json:"would_reach_a_person"`
	WouldBeRefused    int `json:"would_be_refused"`
	Total             int `json:"total"`
	Metered           int `json:"metered"`

	// Enforcing says whether these decisions were acted on. False is
	// observe mode, where every figure is what would have happened.
	Enforcing bool `json:"enforcing"`

	MinimumSample int       `json:"minimum_sample"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	Note          string    `json:"note"`
}

// Answerable reports whether the window held enough decisions.
func (r ReviewRatio) Answerable() bool { return r.Status == "ok" }

// Tense is "ran" or "would have run", so a template never has to infer
// it from Enforcing and get it backwards.
func (r ReviewRatio) Tense() string {
	if r.Enforcing {
		return "ran without reaching a person"
	}
	return "would have run without reaching a person"
}

// ReviewRatioBearer fetches the ratio for a window, optionally scoped
// to one control.
// ReviewRatioBearer reads the share of proposed actions that needed
// nobody, over a window and a control — and, when actor is set, for one
// proposing identity, so the figure reconciles with a timeline filtered
// to a card's holder rather than contradicting it.
func (c *Client) ReviewRatioBearer(ctx context.Context, bearer, controlID, actor string, from, to time.Time) (*ReviewRatio, error) {
	q := url.Values{}
	if !from.IsZero() {
		q.Set("from", from.UTC().Format(time.RFC3339))
	}
	if !to.IsZero() {
		q.Set("to", to.UTC().Format(time.RFC3339))
	}
	if controlID != "" {
		q.Set("control", controlID)
	}
	if actor != "" {
		q.Set("actor", actor)
	}
	path := "/v1/me/review-ratio"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	body, err := c.do(ctx, bearer, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var rr ReviewRatio
	if err := json.Unmarshal(body, &rr); err != nil {
		return nil, fmt.Errorf("apiclient: decode review ratio: %w", err)
	}
	return &rr, nil
}
