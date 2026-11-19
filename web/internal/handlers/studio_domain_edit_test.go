package handlers_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// prodGuardianYAML is a valid, complete contract for a non-default
// domain, used to exercise the domain-aware Build: entering Build scoped
// to a selected domain edits THAT domain in place.
const prodGuardianYAML = `domain: prod-guardian
entity: Service
states: [HEALTHY, DEGRADED, INCIDENT]
events: [INCIDENT_DECLARED]
transitions:
  - on: RESTART_SERVICE
    from: INCIDENT
    to: HEALTHY
actions:
  - name: RESTART_SERVICE
    allowed_states: [DEGRADED, INCIDENT]
    required_parameters: [service]
    risk: high
    executor: cloud.proxy
`

// Build entered with ?domain=<slug> loads THAT domain's contract, scopes
// the sidebar to it, and wires the save form to target it in place —
// instead of the singular default draft.
func TestBuild_LoadsSelectedDomain(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{domainYAML: map[string]string{"prod-guardian": prodGuardianYAML}}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	code, body := studioGet(t, gated, cookie, "/dashboard/build?domain=prod-guardian")
	if code != http.StatusOK {
		t.Fatalf("status %d; body=%s", code, body)
	}
	for _, want := range []string{
		`name="domain" value="prod-guardian"`,           // save targets this domain in place
		`href="/dashboard/govern?domain=prod-guardian"`, // sidebar scoped to it
		"RESTART_SERVICE",                               // the loaded contract, not the default draft
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Build scoped to prod-guardian missing %q\n---\n%s", want, body)
		}
	}
	// It must not have fallen back to the singular default draft.
	if strings.Contains(body, "ISSUE_REFUND") {
		t.Fatalf("Build should edit prod-guardian, not the default refund draft\n---\n%s", body)
	}
}

// Saving from a domain-scoped Build updates that domain in place (PUT
// /v1/me/domains/<slug>) and lands on the domain's home — not the
// singular default activate.
func TestBuild_SavesToSelectedDomain(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{domainYAML: map[string]string{"prod-guardian": prodGuardianYAML}}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	edited := strings.Replace(prodGuardianYAML, "risk: high", "risk: medium", 1)
	rec := studioPost(t, gated, cookie, "/dashboard/build/activate-yaml", url.Values{
		"yaml":   {edited},
		"domain": {"prod-guardian"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: status %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/domains/prod-guardian" {
		t.Errorf("save redirect = %q, want /dashboard/domains/prod-guardian", loc)
	}
	// The per-domain update endpoint was hit (not the singular PUT /v1/me/domain).
	api.mu.Lock()
	defer api.mu.Unlock()
	sawPerDomainPut := false
	for _, o := range api.observed {
		if o == "PUT /v1/me/domains/prod-guardian" {
			sawPerDomainPut = true
		}
		if o == "PUT /v1/me/domain" {
			t.Errorf("domain-scoped save must not hit the singular default activate: %s", o)
		}
	}
	if !sawPerDomainPut {
		t.Errorf("expected PUT /v1/me/domains/prod-guardian; observed=%v", api.observed)
	}
	if got := api.domainYAML["prod-guardian"]; !strings.Contains(got, "risk: medium") {
		t.Errorf("prod-guardian was not updated in place; got:\n%s", got)
	}
}
