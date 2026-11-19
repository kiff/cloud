package pages

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// gatewayKeyEnv is the variable the Codex config reads the key from, so
// the key stays out of a file that may be checked in.
const gatewayKeyEnv = "KIFF_GATEWAY_KEY"

// GatewayClaudeConfig is the Claude Code MCP server entry for the KIFF
// gateway, with the agent's gateway key.
func GatewayClaudeConfig(gatewayURL, key string) string {
	type server struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	cfg := map[string]map[string]server{"mcpServers": {"kiff": {
		Type: "http", URL: gatewayURL, Headers: map[string]string{"Authorization": "Bearer " + key},
	}}}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return string(b)
}

// GatewayCodexConfig is the Codex config.toml entry for the gateway. It
// names the variable the key is read from (GatewayEnvLine), so the key
// stays out of a file that may be checked in.
func GatewayCodexConfig(gatewayURL string) string {
	return "# ~/.codex/config.toml\n" +
		"[mcp_servers.kiff]\n" +
		"url = " + strconv.Quote(gatewayURL) + "\n" +
		"bearer_token_env_var = " + strconv.Quote(gatewayKeyEnv) + "\n"
}

// GatewayEnvLine puts the key in the agent's environment, where Codex and
// other clients read it.
func GatewayEnvLine(key string) string {
	return "export " + gatewayKeyEnv + "=" + key + "\n"
}

// GatewayCurl is an MCP initialize call to the gateway with the key: a
// 200 with the server's name says the key works.
func GatewayCurl(gatewayURL, key string) string {
	return "curl -sS " + gatewayURL + " \\\n" +
		"  -H 'Authorization: Bearer " + key + "' \\\n" +
		"  -H 'Content-Type: application/json' \\\n" +
		"  -H 'Accept: application/json, text/event-stream' \\\n" +
		`  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'` + "\n"
}

// gatewayFormat is one "Use it with…" choice on the key reveal.
type gatewayFormat struct{ ID, Label string }

// gatewayFormats are the reveal's formats, in the order people pick them.
var gatewayFormats = []gatewayFormat{
	{"claude", "Claude Code"},
	{"codex", "Codex"},
	{"env", "Environment variable"},
	{"curl", "curl"},
}

// gatewayKeyFlash is the gateway key to show once, from the flash.
func gatewayKeyFlash(f *DashboardFlash) string {
	if f != nil && f.Kind == "gateway_key_revealed" {
		return f.KeyPlaintext
	}
	return ""
}

// hasGatewayKey reports whether one of the agent's keys holds
// gateway_agent.
func hasGatewayKey(a AgentSummary) bool {
	for _, k := range a.Keys {
		for _, r := range k.Roles {
			if r == "gateway_agent" {
				return true
			}
		}
	}
	return false
}

// gatewayKeyCount is how many of the agent's keys reach the gateway.
func gatewayKeyCount(a AgentSummary) int {
	n := 0
	for _, k := range a.Keys {
		if keyIsGateway(k) {
			n++
		}
	}
	return n
}

func keyCountLabel(n int) string {
	if n == 1 {
		return "1 key"
	}
	return strconv.Itoa(n) + " keys"
}

// activeLinkCount is how many of the agent's connect links still work.
func activeLinkCount(links []apiclient.ConnectLink) int {
	n := 0
	for _, l := range links {
		if l.RevokedAt == nil {
			n++
		}
	}
	return n
}

func linkCountLabel(n int) string {
	if n == 1 {
		return "1 link"
	}
	return strconv.Itoa(n) + " links"
}

// toolCoverage is the agent's Card in force that covers the tool, if any.
// A tool no Card of the agent's covers has its calls refused (the gateway
// says so in its tool list).
func toolCoverage(a AgentSummary, tool string) (apiclient.Mandate, bool) {
	now := time.Now()
	for _, m := range a.Cards {
		if !cardInForce(m, now) {
			continue
		}
		for _, g := range m.Grants {
			if g.Action == tool {
				return m, true
			}
		}
	}
	return apiclient.Mandate{}, false
}
