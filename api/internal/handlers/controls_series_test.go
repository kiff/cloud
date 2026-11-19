package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/proposals"
)

type activitySeriesResp struct {
	Domain        string    `json:"domain"`
	ControlID     string    `json:"control_id"`
	Actor         string    `json:"actor"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	BucketSeconds int       `json:"bucket_seconds"`
	Buckets       []struct {
		Start  time.Time      `json:"start"`
		Counts map[string]int `json:"counts"`
		Total  int            `json:"total"`
	} `json:"buckets"`
}

// The series endpoint resolves the domain's active control, buckets the
// control-scoped decisions across the window, and returns per-outcome
// counts in ascending time order — the substrate for the Activity chart.
func TestDomainActivitySeries_ReturnsBuckets(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := controlDecisionsRig(t)
	now := time.Now().UTC()
	// 2 in the last 10 min, 1 an hour ago — with a 1h window the bucket is
	// 1m, so these land in distinct buckets.
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Minute)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p2", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "blocked", CreatedAt: now.Add(-2 * time.Minute)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p3", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-40 * time.Minute)})
	// A decision for a DIFFERENT control — must not appear.
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p4", ActionName: "REFUND_ORDER", ControlID: "ctrl-other", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Minute)})

	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)
	rec := getJSON(t, mux, "/v1/me/domains/refund/activity/series?from="+from+"&to="+to)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp activitySeriesResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ControlID != "ctrl-1" {
		t.Errorf("control_id = %q, want ctrl-1", resp.ControlID)
	}
	// A 1h window picks a 1-minute bucket.
	if resp.BucketSeconds != 60 {
		t.Errorf("bucket_seconds = %d, want 60 (1m for a 1h window)", resp.BucketSeconds)
	}
	// Two non-empty buckets (ctrl-1 only; ctrl-other excluded).
	if len(resp.Buckets) != 2 {
		t.Fatalf("want 2 non-empty buckets, got %d: %+v", len(resp.Buckets), resp.Buckets)
	}
	var total int
	for _, b := range resp.Buckets {
		total += b.Total
	}
	if total != 3 {
		t.Errorf("total across buckets = %d, want 3 (ctrl-1 only)", total)
	}
}

// ?actor= narrows the series to one proposing identity (the per-runtime
// "view this runtime's activity" slice).
func TestDomainActivitySeries_ActorFilter(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := controlDecisionsRig(t)
	now := time.Now().UTC()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", ActorID: "agent:refund-bot", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Minute)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p2", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", ActorID: "agent:other", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Minute)})

	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)
	rec := getJSON(t, mux, "/v1/me/domains/refund/activity/series?from="+from+"&to="+to+"&actor=agent:refund-bot")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp activitySeriesResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Actor != "agent:refund-bot" {
		t.Errorf("actor echo = %q, want agent:refund-bot", resp.Actor)
	}
	var total int
	for _, b := range resp.Buckets {
		total += b.Total
	}
	if total != 1 {
		t.Errorf("actor-scoped total = %d, want 1", total)
	}
}

// An unknown domain 404s (never leaks another tenant's control).
func TestDomainActivitySeries_UnknownDomain404(t *testing.T) {
	t.Parallel()
	mux, _, _ := controlDecisionsRig(t)
	rec := getJSON(t, mux, "/v1/me/domains/does-not-exist/activity/series")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}
