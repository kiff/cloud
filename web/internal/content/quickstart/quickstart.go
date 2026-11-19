// Package quickstart holds the data for the landing page's "start
// immediately" spine: the agent-framework picker (put the guard in front
// of one action) and the coding-agent picker (install the domain skill).
//
// The snippets are grounded in the real kiff-guard public API: one
// ToolMap.bind + Guard(mode="enforce") + connect(adapter=...), then the
// framework's verified attach seam. The decide contract is identical on
// every stack — only the adapter import and attach line change. Data,
// not markup, so the templ layer just ranges over it and the whole set
// is reviewable in one place.
package quickstart

import (
	"fmt"
	"strings"
)

// Framework is one entry in the "put it in front of the action" picker.
// Snippet is a complete, copy-paste-able integration for that stack.
type Framework struct {
	Slug    string // URL selector + element id (?stack=<slug>)
	Label   string // chip text
	Lang    string // code fence hint (py | ts | bash)
	Snippet string // the full integration snippet
	Icon    string // self-hosted monochrome logo slug ("" = label only)
}

// pySetup is the shared enforce-mode setup every Python SDK adapter uses:
// map the tool to a domain action, construct the guard, register the
// runtime. Only the adapter import and the attach line differ per stack.
const pySetup = `from kiff_guard import Guard, HTTPClient, ToolMap
from kiff_guard.adapters.%s import %s

tm = ToolMap().bind("refund_order", action="REFUND_ORDER",
                    entity_type="Order", entity_arg="order_id")
guard = Guard(client=HTTPClient(api_key=KEY, tool_map=tm),
              tenant="acme", agent="refunds", mode="enforce")
guard.connect(adapter="%s")

`

// Frameworks is the ordered picker. The first entry is the default when
// no (or an unknown) ?stack= is supplied.
func Frameworks() []Framework {
	return []Framework{
		{
			Slug: "agno", Label: "Agno", Lang: "py",
			Snippet: fmt.Sprintf(pySetup, "agno", "agno_hook", "agno") +
				`agent = Agent(model=..., tools=[refund_order],
              tool_hooks=[agno_hook(guard)])   # decides before the tool runs`,
		},
		{
			Slug: "langgraph", Label: "LangGraph", Lang: "py", Icon: "langgraph",
			Snippet: fmt.Sprintf(pySetup, "langgraph", "kiff_wrap_tool_call", "langgraph") +
				`agent = create_agent(model=..., tools=[refund_order],
                     middleware=[kiff_wrap_tool_call(guard)])`,
		},
		{
			Slug: "openai-agents", Label: "OpenAI Agents SDK", Lang: "py",
			Snippet: fmt.Sprintf(pySetup, "openai_agents", "kiff_tool_input_guardrail", "openai-agents") +
				`@function_tool(tool_input_guardrails=[kiff_tool_input_guardrail(guard)])
def refund_order(order_id: str, amount: int, reason: str): ...`,
		},
		{
			Slug: "google-adk", Label: "Google ADK", Lang: "py", Icon: "google",
			Snippet: fmt.Sprintf(pySetup, "google_adk", "kiff_before_tool_callback", "google-adk") +
				`agent = Agent(tools=[refund_order],
              before_tool_callback=kiff_before_tool_callback(guard))`,
		},
		{
			Slug: "pydantic-ai", Label: "Pydantic AI", Lang: "py", Icon: "pydantic",
			Snippet: fmt.Sprintf(pySetup, "pydantic_ai", "kiff_before_tool_execute", "pydantic-ai") +
				`agent = Agent(model=...,
              before_tool_execute=kiff_before_tool_execute(guard))`,
		},
		{
			Slug: "strands", Label: "Strands", Lang: "py",
			Snippet: fmt.Sprintf(pySetup, "strands", "kiff_hook_provider", "strands") +
				`agent = Agent(model=..., tools=[refund_order],
              hooks=[kiff_hook_provider(guard)])`,
		},
		{
			Slug: "ms-agent-framework", Label: "MS Agent Framework", Lang: "py",
			Snippet: fmt.Sprintf(pySetup, "microsoft_agent_framework", "kiff_guard_middleware", "ms-agent-framework") +
				`agent = Agent(tools=[refund_order],
              middleware=[kiff_guard_middleware(guard)])`,
		},
		{
			Slug: "hermes", Label: "Hermes", Lang: "py",
			Snippet: fmt.Sprintf(pySetup, "hermes", "register_kiff_guard", "hermes") +
				`register_kiff_guard(ctx, guard)   # in your Hermes plugin's register()`,
		},
		{
			Slug: "openclaw", Label: "OpenClaw", Lang: "ts",
			Snippet: `import { Guard, HTTPClient, ToolMap } from "@kiff/kiff-guard";
import { registerKiffGuard } from "@kiff/kiff-guard/adapters/openclaw";

const tm = new ToolMap().bind("refund_order", {
  action: "REFUND_ORDER", entityType: "Order", entityArg: "order_id" });
const client = new HTTPClient({ apiKey: KEY, toolMap: tm });
const guard = new Guard({ client, tenant: "acme", agent: "refunds", mode: "enforce" });

registerKiffGuard(ctx, guard);   // in your OpenClaw plugin`,
		},
		{
			Slug: "custom", Label: "Custom code", Lang: "py",
			Snippet: `# No adapter needed. Wrap the one function that moves money.
def issue_refund(order, amount):
    d = kiff.decide("REFUND_ORDER", entity=order, amount=amount)
    if not d.allowed:
        return d                         # blocked or held — never execute
    payments.refund(order, amount)       # your code, unchanged`,
		},
		{
			Slug: "http", Label: "Plain HTTP", Lang: "bash", Icon: "curl",
			Snippet: `# No SDK. Any language. POST the proposed action; act only on "allowed".
curl -s https://api.kiff.dev/v1/proposals/decide \
  -H "Authorization: Bearer $KIFF_KEY" -H "Content-Type: application/json" \
  -d '{"id":"rd-4471","entity_id":"order-4471","entity_type":"Order",
       "action_name":"REFUND_ORDER","actor_id":"refunds",
       "parameters":{"amount":8400,"reason":"damaged"}}'
# -> {"outcome":"allowed"}   then POST .../execute for a signed receipt`,
		},
	}
}

// Assistant is one entry in the "install the domain skill" picker. The
// install is a single curl of the public SKILL.md into the agent's own
// skills/rules location — the same distribution shape as other agent-skill
// catalogs, so it works today with no extra CLI.
type Assistant struct {
	Slug    string // ?agent=<slug>
	Label   string // chip text
	Command string // the install command (may be multi-line for display)
	Icon    string // self-hosted monochrome logo slug ("" = no logo, label only)
	Prompt  string // natural-language setup instruction to paste into the agent
}

// setupPrompt builds the paste-into-your-agent instruction: install the
// skill with the (single-line) command, read it, then author + connect a
// domain. Grounded in the real Cloud routes so the agent can act on it.
func setupPrompt(cmd string) string {
	oneLine := strings.ReplaceAll(cmd, " \\\n  ", " ")
	return "Set up the KIFF domain skill in this project: run `" + oneLine +
		"` to add the skill, read it, then help me author a kiff.yaml domain for my " +
		"riskiest action (states, actions, approvals, permissions, executors) and connect it " +
		"to KIFF Cloud — validate with POST /v1/me/domain/validate, then PUT /v1/me/domain."
}

// skillURL is the public, single-fetch source of the domain skill.
const skillURL = "https://kiff.dev/skills/kiff-domains.md"

// ConnectPrompt is the paste-into-your-assistant instruction for wiring
// an existing agent through the KIFF proposal gate — the connect-side
// analog of setupPrompt (which authors the domain). It is grounded in
// the real decide route and the outcome contract so a coding assistant
// (Cursor, Claude Code, Codex, …) can implement the integration
// directly, with the API endpoint filled in. apiBase falls back to the
// public cloud when empty.
func ConnectPrompt(apiBase string) string {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = "https://api.kiff.dev"
	}
	return "Wire this agent's consequential action (the one that moves money or " +
		"changes state) through KIFF before it runs. Immediately before the side " +
		"effect executes, POST to " + base + "/v1/proposals/decide with header " +
		"`Authorization: Bearer $KIFF_API_KEY` and a JSON body of {id, entity_type, " +
		"entity_id, action_name, actor_id, parameters}. Then branch on the response " +
		"field `outcome`: run the action only when it is `allowed`; on " +
		"`approval_required` queue it for a human and stop; on `blocked` or `invalid` " +
		"do not run it and surface `message`. Keep the KIFF call in front of the side " +
		"effect, never after, and read the KIFF_API_KEY from the environment. See " +
		"https://kiff.dev/llms-full.txt for the decision contract and outcome semantics."
}

// Assistants is the ordered picker; first entry is the default.
func Assistants() []Assistant {
	as := []Assistant{
		{Slug: "cursor", Label: "Cursor", Icon: "cursor", Command: "curl -fsSL " + skillURL + " \\\n  -o .cursor/rules/kiff-domains.mdc"},
		{Slug: "kiro", Label: "Kiro", Command: "mkdir -p .kiro/skills/kiff-domains && curl -fsSL " + skillURL + " \\\n  -o .kiro/skills/kiff-domains/SKILL.md"},
		{Slug: "codex", Label: "Codex", Command: "curl -fsSL " + skillURL + " >> AGENTS.md"},
		{Slug: "claude", Label: "Claude Code", Icon: "claude", Command: "mkdir -p ~/.claude/skills/kiff-domains && curl -fsSL " + skillURL + " \\\n  -o ~/.claude/skills/kiff-domains/SKILL.md"},
		{Slug: "copilot", Label: "Copilot", Icon: "githubcopilot", Command: "mkdir -p ~/.copilot/skills/kiff-domains && curl -fsSL " + skillURL + " \\\n  -o ~/.copilot/skills/kiff-domains/SKILL.md"},
		{Slug: "gemini", Label: "Gemini CLI", Icon: "googlegemini", Command: "curl -fsSL " + skillURL + " >> GEMINI.md"},
		{Slug: "aider", Label: "Aider", Command: "curl -fsSL " + skillURL + " >> AGENTS.md"},
		{Slug: "amp", Label: "Amp", Command: "curl -fsSL " + skillURL + " >> AGENTS.md"},
		{Slug: "opencode", Label: "OpenCode", Command: "curl -fsSL " + skillURL + " >> AGENTS.md"},
		{Slug: "windsurf", Label: "Windsurf", Icon: "windsurf", Command: "mkdir -p .windsurf/rules && curl -fsSL " + skillURL + " \\\n  -o .windsurf/rules/kiff-domains.md"},
	}
	for i := range as {
		as[i].Prompt = setupPrompt(as[i].Command)
	}
	return as
}

// ResolveFramework returns the selected framework for a ?stack= value,
// falling back to the first (default) when empty or unknown.
func ResolveFramework(slug string) Framework {
	fw := Frameworks()
	for _, f := range fw {
		if f.Slug == slug {
			return f
		}
	}
	return fw[0]
}

// ResolveAssistant returns the selected assistant for a ?agent= value,
// falling back to the first (default) when empty or unknown.
func ResolveAssistant(slug string) Assistant {
	as := Assistants()
	for _, a := range as {
		if a.Slug == slug {
			return a
		}
	}
	return as[0]
}
