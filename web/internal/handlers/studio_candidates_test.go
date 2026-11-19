package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// oneCandidateJSON is a single runtime with one grounded candidate, in the
// /v1/guard/candidates wire shape.
const oneCandidateJSON = `{
  "source": "kiff-guard runtime",
  "runtimes": [
    {
      "runtime": {"agent_id":"ap-agent","adapter":"openclaw","project":"cookbook","environment":"aws","workflow":"duplicate-payment","mode":"enforce"},
      "candidates": [
        {
          "tool":"pay_invoice","action":"PAY_INVOICE","entity_type":"Invoice","entity_arg":"invoice_id",
          "fields":[{"name":"invoice_id","type":"string","required":false},{"name":"amount_cents","type":"integer","required":true}],
          "risk":"high","risk_reasons":["matched signal: pay"],
          "evidence":{"source":"kiff-guard runtime","tool":"pay_invoice","adapter":"openclaw","project":"cookbook","environment":"aws","workflow":"duplicate-payment","mode":"enforce"},
          "observed_call_count":4,
          "policy_thresholds":null,"states":null
        }
      ],
      "grounding": {"ok": true, "checks": []}
    }
  ]
}`

func studioGet(t *testing.T, gated http.Handler, cookie *http.Cookie, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func studioPost(t *testing.T, gated http.Handler, cookie *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	return rec
}

// First-run with runtime candidates leads with the action-first chooser and
// renders a candidate-control card — not the cold authoring ladder.
func TestStudio_FirstRunRendersCandidateCards(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{candidatesJSON: oneCandidateJSON}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	code, body := studioGet(t, gated, cookie, "/dashboard/build")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	for _, want := range []string{
		"KIFF found actions your agent can take. Choose one to protect.",
		"PAY_INVOICE",
		"pay_invoice",
		"Invoice",
		"Protect this action",
		"Policy template",
		"KIFF did not infer any",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("candidate render missing %q", want)
		}
	}
	// The cold-authoring ladder must NOT be the default front door.
	if strings.Contains(body, "Hand your domain to your AI coding agent.") {
		t.Error("action-first first run should not render the bridge/ladder header")
	}
}

// First-run with no candidates renders the action-first empty state with
// connect guidance + fallbacks — never the ladder.
func TestStudio_FirstRunNoCandidatesEmptyState(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{} // empty candidates default
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	code, body := studioGet(t, gated, cookie, "/dashboard/build")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	for _, want := range []string{
		"KIFF found actions your agent can take. Choose one to protect.",
		"Connect your agent",
		"Paste one tool call",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("empty state missing %q", want)
		}
	}
}

// The candidate card must not surface invented thresholds or states.
func TestStudio_CandidateCardNoInventedPolicy(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{candidatesJSON: oneCandidateJSON}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	_, body := studioGet(t, gated, cookie, "/dashboard/build")
	if !strings.Contains(body, "You set the bands") {
		t.Error("policy template must state the bands are user-set")
	}
	// No fabricated lifecycle: the card text promises no inference, and the
	// observation carried no states.
	if !strings.Contains(body, "KIFF did not infer any") {
		t.Error("card must carry the no-invention promise")
	}
}

// Confirming a candidate creates the domain draft and routes back to the
// existing Studio draft/edit path (now non-first-run → ladder).
func TestStudio_ConfirmCandidateCreatesDraft(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{candidatesJSON: oneCandidateJSON}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	rec := studioPost(t, gated, cookie, "/dashboard/build/candidate/confirm", url.Values{
		"project":     {"cookbook"},
		"environment": {"aws"},
		"agent_id":    {"ap-agent"},
		"workflow":    {"duplicate-payment"},
		"tool":        {"pay_invoice"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}

	// Draft now exists and carries the confirmed action surface, with no
	// fabricated thresholds.
	if !strings.Contains(api.draftYAML, "PAY_INVOICE") {
		t.Errorf("draft missing confirmed action; yaml=%q", api.draftYAML)
	}
	if !strings.Contains(api.draftYAML, "Invoice") {
		t.Errorf("draft missing observed entity; yaml=%q", api.draftYAML)
	}

	// A subsequent visit is no longer first-run: the ladder renders.
	code, body := studioGet(t, gated, cookie, "/dashboard/build")
	if code != http.StatusOK {
		t.Fatalf("post-confirm status = %d, want 200", code)
	}
	if !strings.Contains(body, "Hand your domain to your AI coding agent.") {
		t.Error("post-confirm visit should render the draft/edit view, not action-first")
	}
}

// The paste/replay fallback derives a control from one tool-call JSON.
func TestStudio_ReplayDerivesCandidate(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	rec := studioPost(t, gated, cookie, "/dashboard/build/candidate/replay", url.Values{
		"tool_call": {`{"tool":"issue_refund","action":"ISSUE_REFUND","entity_type":"refund","fields":{"amount":4900,"reason":"x"}}`},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("replay status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(api.draftYAML, "ISSUE_REFUND") {
		t.Errorf("replay draft missing derived action; yaml=%q", api.draftYAML)
	}
	if !strings.Contains(api.draftYAML, "refund") {
		t.Errorf("replay draft missing entity; yaml=%q", api.draftYAML)
	}
}

// A bad replay payload is rejected without creating a draft.
func TestStudio_ReplayRejectsBadJSON(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	rec := studioPost(t, gated, cookie, "/dashboard/build/candidate/replay", url.Values{
		"tool_call": {`not json`},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("replay status = %d, want 303 (flash+redirect)", rec.Code)
	}
	if api.draftYAML != "" {
		t.Errorf("bad replay should not create a draft; yaml=%q", api.draftYAML)
	}
}
