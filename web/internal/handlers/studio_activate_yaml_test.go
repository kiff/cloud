package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// studio_activate_yaml_test.go covers the block-free completion path: a
// derived draft lands at StageScratch (an observed action, no state
// machine) and cannot be activated as-is. Today the only in-app way to
// complete it is the legacy block editor. This path lets the developer
// finish the contract in their codebase (scaffold -> fill -> kiff
// verify) and bring the completed kiff.yaml back to validate + activate
// in one shot — no block editor. That is the prerequisite that has to
// ship and be proven before the editor is retired.

// scratchDraftYAML is the shape a freshly derived candidate persists:
// an observed action, no state machine. StageScratch — not activatable.
const scratchDraftYAML = `domain: refund-flow
entity: Order
states: []
actions:
  - name: ISSUE_REFUND
    allowed_states: []
    required_parameters: [amount_cents]
    risk: high
`

// completedYAML is what the developer brings back after finishing the
// domain in their repo and running kiff verify — a real state machine.
const completedYAML = `domain: refund-flow
entity: Order
states: [REQUESTED, REFUNDED]
events: [ORDER_REQUESTED]
transitions:
  - on: ISSUE_REFUND
    from: REQUESTED
    to: REFUNDED
actions:
  - name: ISSUE_REFUND
    allowed_states: [REQUESTED]
    required_parameters: [amount_cents]
    required_permissions: [refund-flow.issue_refund]
    risk: high
    approval: required
    executor: cloud.proxy
roles:
  tenant_owner: [refund-flow.issue_refund, refund-flow.issue_refund.approve]
`

func postActivateYAML(t *testing.T, gated http.Handler, cookie *http.Cookie, yaml string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"yaml": {yaml}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/build/activate-yaml", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	return rec
}

// TestStudioActivateYAML_CompletesDerivedDraftBlockFree is the
// end-to-end acceptance: an *incomplete* (StageScratch) derived draft
// exists, and the developer activates a completed kiff.yaml without
// touching any block-form route. The completed contract validates and
// becomes the active domain (PUT /v1/me/domain), and we land on the
// domain home.
func TestStudioActivateYAML_CompletesDerivedDraftBlockFree(t *testing.T) {
	t.Parallel()
	// Seed an incomplete derived draft — the case that actually
	// exercises the stranding risk.
	api := &studioFakeAPI{draftYAML: scratchDraftYAML, draftUpdatedAt: 1}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	rec := postActivateYAML(t, gated, cookie, completedYAML)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("activate-yaml: status %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/domains" {
		t.Errorf("activate-yaml redirect = %q, want /dashboard/domains", loc)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	var sawPutDomain bool
	for _, o := range api.observed {
		if o == "PUT /v1/me/domain" {
			sawPutDomain = true
		}
		if strings.HasPrefix(o, "POST /dashboard/build/blocks") {
			t.Errorf("block-form route used in the block-free path: %s", o)
		}
	}
	if !sawPutDomain {
		t.Errorf("completion did not PUT /v1/me/domain; observed=%v", api.observed)
	}
	if !strings.Contains(api.activeYAML, "REQUESTED") {
		t.Errorf("completed contract did not become active; activeYAML=%q", api.activeYAML)
	}
}

// TestStudioActivateYAML_IncompleteFlashesWhatsLeft confirms the honest
// loop: importing a still-incomplete contract is not activated; the api's
// 422 surfaces what's left to finish, and nothing is promoted to active.
func TestStudioActivateYAML_IncompleteFlashesWhatsLeft(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{draftYAML: scratchDraftYAML, draftUpdatedAt: 1}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	// The scratch YAML still has states: [] — incomplete.
	rec := postActivateYAML(t, gated, cookie, scratchDraftYAML)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("activate-yaml incomplete: status %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/build" {
		t.Errorf("incomplete activate redirect = %q, want /dashboard/build", loc)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if api.activeYAML != "" {
		t.Errorf("incomplete contract was activated; activeYAML=%q", api.activeYAML)
	}
}

// TestStudioActivateYAML_EmptyPaste is a no-op with a helpful message,
// never a PUT.
func TestStudioActivateYAML_EmptyPaste(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{draftYAML: scratchDraftYAML, draftUpdatedAt: 1}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	rec := postActivateYAML(t, gated, cookie, "   ")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("empty activate-yaml: status %d, want 303", rec.Code)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	for _, o := range api.observed {
		if o == "PUT /v1/me/domain" {
			t.Errorf("empty paste should not PUT /v1/me/domain; observed=%v", api.observed)
		}
	}
}

// TestStudioActivateYAML_AffordanceReachableWithoutManual confirms the
// block-free handoff (the paste-to-activate form) renders in the default
// Build view for a derived draft — i.e. without ?manual=1, the editor's
// entry. This is what makes the path reachable once the editor is gone.
func TestStudioActivateYAML_AffordanceReachableWithoutManual(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{draftYAML: scratchDraftYAML, draftUpdatedAt: 1}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/build", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET studio: status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`action="/dashboard/build/activate-yaml"`,
		"Finish with your AI coding agent",
		"kiff verify",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("default Build view missing block-free handoff marker %q", want)
		}
	}
}
