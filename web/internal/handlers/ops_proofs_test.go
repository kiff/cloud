package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// opsProofsFakeAPI serves the proofs routes the /ops/proofs console reads,
// and records the overrides it forwards.
type opsProofsFakeAPI struct {
	mu        sync.Mutex
	overrides []map[string]string // app, action, and the body's fields
	refuse    bool
}

func (f *opsProofsFakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ops/proofs", func(w http.ResponseWriter, r *http.Request) {
		strip := []map[string]string{{"date": "2026-11-16", "status": "red"}, {"date": "2026-11-17", "status": "amber"}}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"release_anyway": true,
			"apps": []map[string]any{{
				"app": "gmail", "passed": 1, "counted": 4, "errors": 2, "uncounted": 1,
				"last_run_id": "run_gmail_9", "last_at": now.Format(time.RFC3339), "last_outcome": "fail",
				"p50_ms": 420, "strip": strip,
				"state": map[string]any{"app": "gmail", "entry": "gmail@4", "state": "red", "reason": "limits_failed", "held": true},
			}, {
				"app": "stripe", "passed": 30, "counted": 30, "last_run_id": "run_stripe_1",
				"last_at": now.Format(time.RFC3339), "last_outcome": "pass", "p50_ms": 300, "strip": strip,
				"state": map[string]any{"app": "stripe", "state": "green", "reason": "two_passed"},
			}},
		})
	})
	mux.HandleFunc("GET /v1/ops/proofs/{run_id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("run_id") != "run_gmail_9" {
			writeJSONForTest(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"run": map[string]any{
				"run_id": "run_gmail_9", "app": "gmail", "job": "replies-in-time", "tenant": "tnt_proof",
				"trigger": "schedule", "outcome": "fail", "counted": true, "runner_version": strings.Repeat("a", 40),
				"started_at": now.Add(-time.Minute).Format(time.RFC3339), "ended_at": now.Format(time.RFC3339),
				"events_key": "proofs/run_gmail_9/events.jsonl", "entry": "gmail@4",
				"github_run_id": "42146041790", "github_run_attempt": "2",
				"rules": map[string]string{"judge_limits": "sha256:cd34", "coach_words": "sha256:ab12"},
				"checks": []map[string]any{
					{"name": "connects", "ok": true},
					{"name": "card_limit_held", "ok": false, "why": "a send over the limit went without asking"},
				},
			},
			"events": []json.RawMessage{
				json.RawMessage(`{"seq":1,"kind":"user_said","at":"2026-11-17T11:59:01Z","text":"Reply to the refund email"}`),
				json.RawMessage(`{"seq":2,"kind":"tool_call","args":{"api_key":"[redacted]"}}`),
			},
			"redacted": 1,
		})
	})
	mux.HandleFunc("POST /v1/ops/proofs/apps/{app}/{action}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["app"], body["action"] = r.PathValue("app"), r.PathValue("action")
		f.mu.Lock()
		f.overrides = append(f.overrides, body)
		f.mu.Unlock()
		if f.refuse {
			writeJSONForTest(w, http.StatusForbidden, map[string]string{"error": "owner_approval_required"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]string{"id": "pov_1"})
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpsProofs_MonitoringPageRenders(t *testing.T) {
	t.Parallel()
	srv := (&opsProofsFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := opsGet(t, newOpsSupportMux(t, srv, mgr), mgr, "/ops/proofs")
	for _, want := range []string{
		"Proofs.", "gmail", "gmail@4", "stripe", "red · writes held", "the card limit didn&#39;t hold", "1 / 4", "1 uncounted",
		"/ops/proofs/run_gmail_9", "ops-strip-red", "ops-strip-amber",
		"/ops/proofs/apps/gmail/hold", "/ops/proofs/apps/gmail/release", "Owner&#39;s yes",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("monitoring page missing %q\n---\n%s", want, body)
		}
	}
	// A green app offers no release.
	if strings.Contains(body, "/ops/proofs/apps/stripe/release") {
		t.Fatal("release offered for a green app")
	}
}

func TestOpsProofs_ReplayRenders(t *testing.T) {
	t.Parallel()
	srv := (&opsProofsFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := opsGet(t, newOpsSupportMux(t, srv, mgr), mgr, "/ops/proofs/run_gmail_9")
	for _, want := range []string{
		"replies-in-time", "gmail@4", "42146041790 · attempt 2", "coach_words=sha256:ab12 · judge_limits=sha256:cd34", "Card limit held", "a send over the limit went without asking", "ops-check-card",
		"user_said", "Reply to the refund email", "[redacted]", "1 line(s) had secret-looking values", "aaaaaaaaaaaa",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("replay missing %q\n---\n%s", want, body)
		}
	}
}

func TestOpsProofs_UnknownRun404(t *testing.T) {
	t.Parallel()
	srv := (&opsProofsFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	req := httptest.NewRequest(http.MethodGet, "/ops/proofs/nope", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	newOpsSupportMux(t, srv, mgr).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestOpsProofs_OverridesReachTheAPI(t *testing.T) {
	t.Parallel()
	fake := &opsProofsFakeAPI{}
	srv := fake.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newOpsSupportMux(t, srv, mgr)

	rec := opsPost(t, gated, mgr, "/ops/proofs/apps/gmail/release",
		url.Values{"reason": {"false alarm"}, "owner_approval": {"owner's message"}, "for": {"24h"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ops/proofs" {
		t.Fatalf("release: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = opsPost(t, gated, mgr, "/ops/proofs/apps/gmail/clear", url.Values{"reason": {"fixed"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear: %d", rec.Code)
	}
	// A length that isn't offered never reaches the API.
	rec = opsPost(t, gated, mgr, "/ops/proofs/apps/gmail/hold", url.Values{"reason": {"x"}, "for": {"365d"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "refused") {
		t.Fatalf("odd length: %s", loc)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.overrides) != 2 {
		t.Fatalf("forwarded %d overrides: %+v", len(fake.overrides), fake.overrides)
	}
	rel := fake.overrides[0]
	if rel["action"] != "release" || rel["owner_approval"] != "owner's message" || rel["reason"] != "false alarm" {
		t.Fatalf("release forwarded as %+v", rel)
	}
	until, err := time.Parse(time.RFC3339, rel["until"])
	if err != nil || until.Sub(time.Now()) > 25*time.Hour || until.Sub(time.Now()) < 23*time.Hour {
		t.Fatalf("until %q", rel["until"])
	}
	if c := fake.overrides[1]; c["action"] != "clear" || c["until"] != "" {
		t.Fatalf("clear forwarded as %+v", c)
	}
}

func TestOpsProofs_ARefusedOverrideSaysSo(t *testing.T) {
	t.Parallel()
	fake := &opsProofsFakeAPI{refuse: true}
	srv := fake.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newOpsSupportMux(t, srv, mgr)
	rec := opsPost(t, gated, mgr, "/ops/proofs/apps/gmail/release", url.Values{"reason": {"x"}, "for": {"24h"}})
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "refused") {
		t.Fatalf("location %q", loc)
	}
	if body := opsGet(t, gated, mgr, loc); !strings.Contains(body, "The override was refused") {
		t.Fatal("no refusal notice")
	}
}

func TestOpsProofs_NonOperatorIsTurnedAway(t *testing.T) {
	t.Parallel()
	srv := (&opsProofsFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	// No session cookie: the gate never reaches the handler.
	for _, path := range []string{"/ops/proofs", "/ops/proofs/run_gmail_9"} {
		rec := httptest.NewRecorder()
		newOpsSupportMux(t, srv, mgr).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusOK {
			t.Fatalf("%s served without a session", path)
		}
	}
}
