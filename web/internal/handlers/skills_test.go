package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSkillDomains asserts /skills/kiff-domains.md serves the public,
// agent-installable domain-authoring playbook as markdown, carries the
// real grammar it teaches, and leaks no private surface.
func TestSkillDomains(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	SkillDomains().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/skills/kiff-domains.md", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("content-type: got %q, want text/markdown", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"name: kiff-domains",
		"kiff.yaml",
		"allowed_states",
		"approval: required",
		"Multi-domain",
		"executor",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("skill missing %q", want)
		}
	}

	for _, banned := range []string{
		"kiff/kiff-cloud",
		"internal/domain",
		"/ops",
		"/dashboard",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("skill leaks a private surface: %q", banned)
		}
	}
}

// TestSkillKiff asserts /skills/kiff.md serves the connect / Card / held
// call playbook, carries the rules that keep an agent on its side of the
// Card, and leaks no private surface.
func TestSkillKiff(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	SkillKiff().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/skills/kiff.md", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("content-type: got %q, want text/markdown", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"name: kiff\n",
		"https://mcp.kiff.dev/mcp",
		"Never route around the Card.",
		"Never approve your own call.",
		"Never ask for, store or print an owner or admin credential.",
		"same `kiff_operation_id`",
		"**Do not retry.**", // unknown: the call may have reached the tool
		"Propose a Card",
		"/plugin install kiff-cards@kiff", // the plugin's permanent ID
		"https://github.com/kiff/kiff-guard",
		"https://github.com/kiff/kiff-scan",
		"https://github.com/kiff/kiff",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("skill missing %q", want)
		}
	}
	for _, want := range []string{
		"| `forwarded` (with `repeat: true`)",
		"a new attempt with a **new** `kiff_operation_id`",
		"under `[mcp_servers.kiff]`",
		// A withdrawn hold is the agent's own run stopping, not the owner's
		// answer: the item is still to do.
		"`answer: withdrawn` means your own run withdrew the call when it stopped",
		"`POST /v1/me/agent-drafts`",
		"https://github.com/kiff/tafs",
		"https://github.com/kiff/agents",
		"https://github.com/kiff/planque",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("skill missing %q", want)
		}
	}
	// The skill never reads a credential from the user's machine: no
	// environment-variable key next to a KIFF URL. The Anthropic
	// directory holds a plugin that does, and the person pastes the
	// configuration KIFF Cloud shows them instead.
	for _, banned := range []string{"$KIFF_API_KEY", "$KIFF_GATEWAY_KEY", "bearer_token_env_var", "Authorization: Bearer $"} {
		if strings.Contains(body, banned) {
			t.Errorf("skill reads a credential from the environment: %q", banned)
		}
	}
	// The pages that describe the plugin must not send people back to an
	// environment variable either.
	llms := httptest.NewRecorder()
	LLMsText().ServeHTTP(llms, httptest.NewRequest(http.MethodGet, "/llms.txt", nil))
	if llms.Code != http.StatusOK || llms.Body.Len() == 0 {
		t.Fatalf("/llms.txt: status %d, %d bytes; want 200 with a body", llms.Code, llms.Body.Len())
	}
	if strings.Contains(llms.Body.String(), "KIFF_GATEWAY_KEY") {
		t.Error("/llms.txt still tells the plugin to read KIFF_GATEWAY_KEY")
	}
	if !strings.Contains(body, "You never read, store or type the key.") {
		t.Error("skill missing the rule that the agent never handles the gateway key")
	}
	for _, banned := range []string{"kiff/kiff-cloud", "/ops", "/dashboard", "api.kiff.dev/v1/ops", "tenant_owner", "agentgabo"} {
		if strings.Contains(body, banned) {
			t.Errorf("skill leaks a private surface: %q", banned)
		}
	}
}
