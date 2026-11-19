package main

import (
	"net/http"
	"testing"
)

// kiff/agents#91: the key a no-tools draft mints (an agent-bound key with no
// role) reaches what the setup agent needs, /v1/me and submitting a taf's
// draft, as any runtime key does. A gateway key reaches only the gateway's
// routes, so it couldn't submit one.
func TestANoToolsAgentKeySubmitsDrafts(t *testing.T) {
	h, keys := boundaryRouter(t)
	for _, r := range [][2]string{{"GET", "/v1/me"}, {"POST", "/v1/me/agent-drafts"}} {
		if code, byBoundary := callAs(t, h, keys["no-tools-agent"], r[0], r[1]); byBoundary || code == http.StatusUnauthorized {
			t.Errorf("no-tools agent key %s %s: %d (refused by a boundary: %v)", r[0], r[1], code, byBoundary)
		}
	}
	if _, byBoundary := callAs(t, h, keys["gateway"], "POST", "/v1/me/agent-drafts"); !byBoundary {
		t.Error("a gateway key reached the draft route")
	}
	for _, r := range [][2]string{{"POST", "/v1/me/mandates"}, {"POST", "/v1/keys"}, {"GET", "/v1/me/stream"}} {
		if _, byBoundary := callAs(t, h, keys["no-tools-agent"], r[0], r[1]); !byBoundary {
			t.Errorf("no-tools agent key reached %s %s", r[0], r[1])
		}
	}
}
