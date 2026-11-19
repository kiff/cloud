# The KIFF skill for coding agents

A `SKILL.md` that teaches Claude Code, Codex and similar agents to work with KIFF. **Beta**: install steps may change.
For people who build or connect agents with a coding agent.
Install it, then ask your agent to connect a tool or draft a Card.

With it, an agent can connect a tool or agent to KIFF, draft a Card for
the owner to issue, and handle a held call without routing around it.
It is plain markdown at [kiff.dev/skills/kiff.md](https://kiff.dev/skills/kiff.md).
Read it before you install it.

**The skill is not the security boundary.** The
[MCP gateway](/docs/mcp-gateway) enforces each Card on KIFF's servers.
The skill alone enforces nothing.

## Install it

Claude Code, as a plugin with this skill, the
[domain skill](https://kiff.dev/skills/kiff-domains.md) and the gateway
as a server named `kiff`:

```text
/plugin marketplace add kiff/kiff-plugins
/plugin install kiff-cards@kiff
```

Claude Code asks for the agent's **gateway key** (from its **Tools**
tab, **Claude Code or Codex**) and keeps it in its secure storage.

### Optional: Card status, notices and recovery in Claude Code

The `kiff` plugin adds a status line with what is left on the Card, a
notice when a call is held, a notice when the owner answers, and the
`/kiff` pane. Install it after `kiff-cards`:

```text
/plugin install kiff@kiff
```

It reads `kiff_card` and `kiff_pending`, which send nothing and draw
nothing. It never approves a call. If Claude Code refuses those reads,
type `/kiff` and allow the tool, or add these rules in **`/permissions`
→ Allow**:

| Gateway connection | Card read | Pending-call read |
| --- | --- | --- |
| `kiff-cards` plugin | `mcp__plugin_kiff-cards_kiff__kiff_card` | `mcp__plugin_kiff-cards_kiff__kiff_pending` |
| Connect link added as `kiff` | `mcp__kiff__kiff_card` | `mcp__kiff__kiff_pending` |
| claude.ai KIFF connector | `mcp__claude_ai_KIFF__kiff_card` | `mcp__claude_ai_KIFF__kiff_pending` |

### Install only the skill in another agent

Codex:

```bash
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills/kiff" && curl -fsSL https://kiff.dev/skills/kiff.md \
  -o "${CODEX_HOME:-$HOME/.codex}/skills/kiff/SKILL.md"
```

For Claude Code, use `~/.claude/skills/kiff/SKILL.md` instead. Any other
agent: save the file as `KIFF.md` and add "Read KIFF.md before working
with KIFF." to its instructions file.

## What to ask

```text
Connect my refund tool to KIFF and give this agent a Card.
KIFF held my last call. What do I do?
```

## What it will not do

- Issue or change a Card. It drafts the terms; the owner issues them.
- Answer a held call. Only a signed-in person who may approve can.
- Ask for an owner or admin credential.
- Make a held or refused change another way, through a direct API call,
  a shell or a browser. It reports what happened and stops.
