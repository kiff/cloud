package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The integration proofs: mirrors of apps/api internal/proofs.

// OpsProofCheck is one named check of a proof run.
type OpsProofCheck struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Why  string `json:"why,omitempty"`
}

// OpsProofRun is one run's summary as the runner sent it, plus KIFF's
// counted and received_at.
type OpsProofRun struct {
	Schema        int             `json:"schema"`
	RunID         string          `json:"run_id"`
	App           string          `json:"app"`
	Job           string          `json:"job"`
	Tenant        string          `json:"tenant"`
	Trigger       string          `json:"trigger"`
	Thread        string          `json:"thread"`
	StartedAt     time.Time       `json:"started_at"`
	EndedAt       time.Time       `json:"ended_at"`
	Outcome       string          `json:"outcome"`
	Checks        []OpsProofCheck `json:"checks"`
	LimitsHeld    bool            `json:"limits_held"`
	P50Ms         int             `json:"p50_ms"`
	P95Ms         int             `json:"p95_ms"`
	RunnerVersion string          `json:"runner_version"`
	EventsKey     string          `json:"events_key"`
	Entry         string          `json:"entry"`
	Rules         json.RawMessage `json:"rules"`
	Counted       bool            `json:"counted"`
	ReceivedAt    time.Time       `json:"received_at"`
	// The GitHub run that sent it, from its signed token.
	GitHubRunID      string `json:"github_run_id"`
	GitHubRunAttempt string `json:"github_run_attempt"`
}

// OpsProofState is an app's state with any override in force.
type OpsProofState struct {
	App      string    `json:"app"`
	Entry    string    `json:"entry"` // the app's current catalog entry revision
	State    string    `json:"state"` // green | red | unknown
	Since    time.Time `json:"since"`
	Reason   string    `json:"reason,omitempty"`
	Override string    `json:"override,omitempty"` // hold | release
	Held     bool      `json:"held"`
	Stale    bool      `json:"stale"`
}

// OpsProofDay is one cell of an app's history strip.
type OpsProofDay struct {
	Date   string `json:"date"`
	Status string `json:"status"` // green | amber | red | grey
}

// OpsProofRow is one app on the monitoring page.
type OpsProofRow struct {
	App         string        `json:"app"`
	Passed      int           `json:"passed"`
	Counted     int           `json:"counted"`
	Errors      int           `json:"errors"`
	Uncounted   int           `json:"uncounted"`
	LastRunID   string        `json:"last_run_id"`
	LastAt      time.Time     `json:"last_at"`
	LastOutcome string        `json:"last_outcome"`
	P50Ms       int           `json:"p50_ms"`
	Strip       []OpsProofDay `json:"strip"`
	State       OpsProofState `json:"state"`
}

// OpsProofRows is the monitoring page.
type OpsProofRows struct {
	Apps []OpsProofRow `json:"apps"`
	// ReleaseAnyway: the "release anyway" override is enabled at all.
	ReleaseAnyway bool `json:"release_anyway"`
}

// OpsProofReplay is one run: its summary and, when the API could read
// them, its events (already redacted by the API).
type OpsProofReplay struct {
	Run               OpsProofRun       `json:"run"`
	Events            []json.RawMessage `json:"events"`
	EventsUnavailable bool              `json:"events_unavailable"`
	Redacted          int               `json:"redacted"`
}

// OpsProofsBearer reads the monitoring page.
func (c *Client) OpsProofsBearer(ctx context.Context, bearer string) (*OpsProofRows, error) {
	raw, err := c.do(ctx, bearer, http.MethodGet, "/v1/ops/proofs", nil)
	if err != nil {
		return nil, err
	}
	var out OpsProofRows
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops proofs: %w", err)
	}
	return &out, nil
}

// OpsProofRunBearer reads one run for the replay.
func (c *Client) OpsProofRunBearer(ctx context.Context, bearer, runID string) (*OpsProofReplay, error) {
	raw, err := c.do(ctx, bearer, http.MethodGet, "/v1/ops/proofs/"+url.PathEscape(runID), nil)
	if err != nil {
		return nil, err
	}
	var out OpsProofReplay
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decode ops proof run: %w", err)
	}
	return &out, nil
}

// OpsProofOverride is one override request (action = hold | release |
// clear). Until is RFC 3339; OwnerApproval is required for a release.
type OpsProofOverride struct {
	Reason        string `json:"reason"`
	Until         string `json:"until,omitempty"`
	OwnerApproval string `json:"owner_approval,omitempty"`
}

// OpsProofOverrideBearer records an override of an app's write hold.
func (c *Client) OpsProofOverrideBearer(ctx context.Context, bearer, app, action string, o OpsProofOverride) error {
	body, _ := json.Marshal(o)
	path := "/v1/ops/proofs/apps/" + url.PathEscape(app) + "/" + url.PathEscape(action)
	_, err := c.do(ctx, bearer, http.MethodPost, path, strings.NewReader(string(body)))
	return err
}
