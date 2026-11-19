package handlers_test

import (
	"strings"
	"testing"
	"time"
)

// dashboard_proposals_test.go: the standalone Proposals page was retired
// in the authenticated-surface design — its decision ledger folded into
// the domain's Activity view (the Control Room decisions table), and the
// /dashboard/proposals route now redirects there. The domain-scoped
// decisions rendering is covered by the domain evidence + controls
// tests; what remains here is the domain-home activity glance's
// connection signal, which the proposals data still feeds.

// TestDomainHome_ConnectionStatusConnected verifies the domain-home
// activity glance shows a live last-seen signal when LastReceivedAt is
// set (the connection signal folded off the retired Overview).
func TestDomainHome_ConnectionStatusConnected(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	v := freshDomainHomeView()
	v.LastReceivedAt = &now

	body := renderDomainHome(t, v)

	if !strings.Contains(body, "last seen") {
		t.Errorf("last-seen status missing when LastReceivedAt is set")
	}
	if strings.Contains(body, "waiting for traffic") {
		t.Errorf("not-connected copy should not render when connected")
	}
}

// TestDomainHome_FirstValueWhenNeverExercised verifies that a governed
// domain that has never produced a decision leads with the value event
// (the first-decision design, as amended for the card-first journey) —
// issue a card and test it — instead of an empty 0/0/0 glance with a
// "waiting for traffic" status. The retired Examples gallery is no longer
// the on-ramp.
func TestDomainHome_FirstValueWhenNeverExercised(t *testing.T) {
	t.Parallel()
	v := freshDomainHomeView()

	body := renderDomainHome(t, v)

	for _, want := range []string{
		"Give an agent a Card, then test it",
		`href="/cards/new"`,
		`href="/cards"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("first-value module missing %q for a never-exercised domain", want)
		}
	}
	if strings.Contains(body, `href="/dashboard/examples"`) {
		t.Errorf("first-value module still links the retired Examples gallery")
	}
	// The empty activity glance must not lead here.
	if strings.Contains(body, "waiting for traffic") {
		t.Errorf("never-exercised domain should lead with the value event, not the empty glance")
	}
}

// TestDomainHome_StatusIdleWhenStale verifies the glance status word is
// honest: a domain whose last decision is well in the past reads "idle",
// not a hardcoded "live" (the first-decision design follow-on to the
// value reframe).
func TestDomainHome_StatusIdleWhenStale(t *testing.T) {
	t.Parallel()
	stale := time.Now().UTC().Add(-23 * 24 * time.Hour)
	v := freshDomainHomeView()
	v.LastReceivedAt = &stale

	body := renderDomainHome(t, v)

	if !strings.Contains(body, "idle") {
		t.Errorf("stale domain status should read 'idle', not a hardcoded 'live'")
	}
	if !strings.Contains(body, "last seen") {
		t.Errorf("exercised domain should still show the last-seen sub-label")
	}
}
