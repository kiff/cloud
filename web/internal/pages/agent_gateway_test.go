package pages

import (
	"strings"
	"testing"
)

// The config matches the runbook's shape, in the order people read it.
func TestGatewayClaudeConfig(t *testing.T) {
	want := `{
  "mcpServers": {
    "kiff": {
      "type": "http",
      "url": "https://mcp.kiff.dev/mcp",
      "headers": {
        "Authorization": "Bearer kiff_k"
      }
    }
  }
}`
	if got := GatewayClaudeConfig("https://mcp.kiff.dev/mcp", "kiff_k"); got != want {
		t.Fatalf("got\n%s", got)
	}
}

// Codex reads the key from the environment: its config names the variable
// and never holds the key; the env line and curl hold it once.
func TestGatewayFormatsHoldTheKeyOnce(t *testing.T) {
	const url, key = "https://mcp.kiff.dev/mcp", "kiff_k_secret"
	codex := GatewayCodexConfig(url)
	if strings.Contains(codex, key) || !strings.Contains(codex, `bearer_token_env_var = "KIFF_GATEWAY_KEY"`) {
		t.Fatalf("codex config:\n%s", codex)
	}
	for name, got := range map[string]string{
		"claude": GatewayClaudeConfig(url, key),
		"env":    GatewayEnvLine(key),
		"curl":   GatewayCurl(url, key),
	} {
		if n := strings.Count(got, key); n != 1 {
			t.Errorf("%s holds the key %d times:\n%s", name, n, got)
		}
	}
	if GatewayEnvLine(key) != "export KIFF_GATEWAY_KEY="+key+"\n" {
		t.Errorf("env line = %q", GatewayEnvLine(key))
	}
	if c := GatewayCurl(url, key); !strings.Contains(c, `"method":"initialize"`) || !strings.HasPrefix(c, "curl -sS "+url) {
		t.Errorf("curl:\n%s", c)
	}
}
