package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/reviewload"
	"github.com/kiff/cloud/api/internal/tenant"
)

func reviewRatioMux(t *testing.T, reader proposals.Reader, opts ...ReviewRatioOption) *http.ServeMux {
	t.Helper()
	opts = append(opts, WithReviewRatioClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	h := NewReviewRatioHandler(reader, opts...)
	if h == nil {
		t.Fatal("NewReviewRatioHandler returned nil with a reader wired")
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func getRatio(t *testing.T, mux *http.ServeMux, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "alice", Tenant: "t1", Roles: []string{"tenant_owner"},
	}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	return rr, body
}

// seedReader returns a reader whose counts are exactly what is asked
// for, so each test states its window in one line.
type countReader struct {
	counts map[string]int
	err    error
}

func (c countReader) ListDecisions(context.Context, string, int) ([]proposals.DecisionRow, error) {
	return nil, nil
}

func (c countReader) ListDecisionsInWindow(context.Context, string, proposals.DecisionQuery) ([]proposals.DecisionRow, error) {
	return nil, nil
}

func (c countReader) CountDecisionsByOutcome(context.Context, string, proposals.DecisionQuery) (map[string]int, int, error) {
	if c.err != nil {
		return nil, 0, c.err
	}
	total := 0
	for _, n := range c.counts {
		total += n
	}
	return c.counts, total, nil
}

func (c countReader) CountDecisionsOverTime(context.Context, string, proposals.DecisionQuery, time.Duration) ([]proposals.TimeBucket, error) {
	return nil, nil
}

func TestReviewRatioReportsTheShareThatNeededNobody(t *testing.T) {
	t.Parallel()
	mux := reviewRatioMux(t, countReader{counts: map[string]int{
		"allowed": 84, "approval_required": 12, "blocked": 4,
	}})

	rr, body := getRatio(t, mux, "/v1/me/review-ratio")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if got := body["percent"].(float64); got != 84 {
		t.Errorf("percent = %v, want 84", got)
	}
	if got := body["total"].(float64); got != 100 {
		t.Errorf("total = %v, want 100", got)
	}
	if got := body["would_be_refused"].(float64); got != 4 {
		t.Errorf("would_be_refused = %v, want 4 — reported, and not in the numerator", got)
	}
}

// The number exists to be checked against the customer's own records,
// so a window too thin to mean anything must decline rather than
// flatter. A client branching on percent instead of status would show
// a confident 0%, which is why percent is absent entirely.
func TestAThinWindowDeclinesInsteadOfFlattering(t *testing.T) {
	t.Parallel()
	mux := reviewRatioMux(t, countReader{counts: map[string]int{"allowed": 4}})

	rr, body := getRatio(t, mux, "/v1/me/review-ratio")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if body["status"] != "insufficient_sample" {
		t.Errorf("status = %v, want insufficient_sample", body["status"])
	}
	if _, present := body["percent"]; present {
		t.Error("percent was sent on an insufficient sample; a client reading it would show a confident figure off four decisions")
	}
	if got := body["minimum_sample"].(float64); int(got) != reviewload.MinimumSample {
		t.Errorf("minimum_sample = %v, want %d", got, reviewload.MinimumSample)
	}
	if body["note"] == "" {
		t.Error("no note explaining what a thin window means for the engagement")
	}
}

// Zero percent is a real answer — everything needed a person — and must
// arrive as status ok so it is not confused with declining to answer.
func TestEverythingNeedingAPersonIsAnAnswerNotASilence(t *testing.T) {
	t.Parallel()
	mux := reviewRatioMux(t, countReader{counts: map[string]int{"approval_required": 60}})

	_, body := getRatio(t, mux, "/v1/me/review-ratio")
	if body["status"] != "ok" {
		t.Fatalf("status = %v, want ok: a full window of approvals is a real finding", body["status"])
	}
	if p, present := body["percent"]; present && p.(float64) != 0 {
		t.Errorf("percent = %v, want 0", p)
	}
	if got := body["would_reach_a_person"].(float64); got != 60 {
		t.Errorf("would_reach_a_person = %v, want 60", got)
	}
}

// In observe mode nothing was withheld, so the figure is what would
// have happened. Claiming an action was actually stopped when it was
// not is the error a customer catches, so the tense fails to observe.
func TestTenseDefaultsToWouldHave(t *testing.T) {
	t.Parallel()
	mux := reviewRatioMux(t, countReader{counts: map[string]int{"allowed": 40}})

	_, body := getRatio(t, mux, "/v1/me/review-ratio")
	if body["enforcing"].(bool) {
		t.Error("enforcing reported true with no tenant store wired; the conservative reading is observe")
	}
	if note, _ := body["note"].(string); note == "" || !containsAll(note, "would have run") {
		t.Errorf("note does not use the counterfactual tense: %q", note)
	}
}

func TestEnforcingTenantGetsThePastTense(t *testing.T) {
	t.Parallel()
	const tid = "01m29jn9j6t1zx3t7e5xsbspqv"
	store := tenant.NewMemoryStore()
	if err := store.Create(context.Background(), tenant.Tenant{ID: tid, Slug: "t1"}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	mux := reviewRatioMux(t, countReader{counts: map[string]int{"allowed": 40}},
		WithReviewRatioTenants(store))

	req := httptest.NewRequest(http.MethodGet, "/v1/me/review-ratio", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "alice", Tenant: tid, Roles: []string{"tenant_owner"},
	}))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	if !body["enforcing"].(bool) {
		t.Fatal("a tenant with no explicit mode defaults to enforce, so these decisions were binding")
	}
	if note, _ := body["note"].(string); !containsAll(note, "ran without reaching a person") {
		t.Errorf("note is not in the past tense: %q", note)
	}
}

// The one output this endpoint must never produce is a plausible
// number built from nothing.
func TestAnUnreadableCountIsNotZero(t *testing.T) {
	t.Parallel()
	mux := reviewRatioMux(t, countReader{err: errors.New("db down")})

	rr, body := getRatio(t, mux, "/v1/me/review-ratio")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: an unreadable count must not render as a ratio", rr.Code)
	}
	if _, present := body["percent"]; present {
		t.Error("a percent was sent when the decisions could not be counted")
	}
}

// Without a reader there is nothing to answer from, so the route is
// absent rather than serving a figure computed over no source.
func TestNoReaderMeansNoRoute(t *testing.T) {
	t.Parallel()
	if NewReviewRatioHandler(nil) != nil {
		t.Fatal("a handler was built with no decisions reader")
	}
	mux := http.NewServeMux()
	NewReviewRatioHandler(nil).Register(mux)
	rr, _ := getRatio(t, mux, "/v1/me/review-ratio")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestWindowDefaultsToTheEngagementWeek(t *testing.T) {
	t.Parallel()
	mux := reviewRatioMux(t, countReader{counts: map[string]int{"allowed": 40}})

	_, body := getRatio(t, mux, "/v1/me/review-ratio")
	from, _ := time.Parse(time.RFC3339, body["from"].(string))
	to, _ := time.Parse(time.RFC3339, body["to"].(string))
	if d := to.Sub(from); d != 7*24*time.Hour {
		t.Errorf("default window = %v, want 7 days — the week the engagement is sold as", d)
	}
}

func TestABackwardsWindowIsRefused(t *testing.T) {
	t.Parallel()
	mux := reviewRatioMux(t, countReader{counts: map[string]int{"allowed": 40}})

	rr, _ := getRatio(t, mux,
		"/v1/me/review-ratio?from=2026-09-12T00:00:00Z&to=2026-09-01T00:00:00Z")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// The ratio sits above rows and tiles that can be filtered to one
// agent. A domain-wide figure there is a number that does not reconcile
// with what is under it — the same failure the control scope exists to
// avoid one level up.
func TestReviewRatioScopesToOneActor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	reader := proposals.NewMemoryReader()
	for i := 0; i < 20; i++ {
		reader.Append("t1", proposals.DecisionRow{
			ProposalID: "held-" + strconv.Itoa(i), ActorID: "refund-agent-eu",
			Outcome: "approval_required", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
		reader.Append("t1", proposals.DecisionRow{
			ProposalID: "ran-" + strconv.Itoa(i), ActorID: "some-other-agent",
			Outcome: "allowed", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	mux := reviewRatioMux(t, reader)
	// Every one of this agent's actions reached a person, even though
	// the domain as a whole is half automatic.
	rr, body := getRatio(t, mux, "/v1/me/review-ratio?actor=refund-agent-eu")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := body["total"]; got != float64(20) {
		t.Errorf("total = %v, want only the holder's 20 decisions", got)
	}
	if got := body["would_reach_a_person"]; got != float64(20) {
		t.Errorf("would_reach_a_person = %v, want 20", got)
	}
	if body["status"] == "ok" && body["percent"] != float64(0) {
		t.Errorf("percent = %v, want 0 for an agent nothing ran for", body["percent"])
	}
}
