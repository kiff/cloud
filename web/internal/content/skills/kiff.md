---
name: kiff
description: "Work with KIFF, which gives each AI agent a Card (which actions it may take, how much, and what happens outside it) and checks every call before it runs. Use when someone wants to connect an agent or a remote MCP tool to KIFF, put an agent behind the KIFF MCP gateway (mcp.kiff.dev), propose or change a KIFF Card, or when a tool result from KIFF says a call is held, refused, deciding or unknown. Also use to find the right KIFF repository, SDK or doc page. Not for writing a kiff.yaml domain: that is the kiff-domains skill."
homepage: https://kiff.dev
docs: https://kiff.dev/docs/quickstart
license: MIT
---

# KIFF: connect, propose a Card, handle a held call

KIFF gives each AI agent a **Card**: which actions it may take, the most per
call, totals per hour, per day or over the Card's whole life, and what
happens to a call outside those limits (it waits for the owner, or it is
refused). KIFF checks every call
against the Card **before** it runs. The owner changes or revokes the Card
without changing the agent.

You will be in one of three situations. Find yours:

1. **Setting up**: a person wants an agent to work through KIFF. Go to
   [Connect an agent](#connect-an-agent), then [Propose a Card](#propose-a-card).
2. **Acting**: you are the agent, and a tool result came back from KIFF. Go to
   [Handle a KIFF result](#handle-a-kiff-result). Read the rules first.
3. **Orienting**: someone asks what KIFF is or where something lives. Go to
   [Where things are](#where-things-are).

## Rules that always apply

These hold in every situation. They are what makes a Card mean anything.

- **Make the call your work needs; KIFF decides.** Do not skip a call
  because you think your Card will not allow it, and do not choose another
  action because of it. Make the call: KIFF allows it, holds it or refuses
  it, and records why for the owner.
- **Never route around the Card.** If KIFF holds or refuses a call, do not
  make the same change another way: no direct API call, no shell, no
  browser, no other tool, no second agent. A refused call ends what you were
  doing to that item for now: do not act on it another way, even with an
  action your Card allows, unless the owner's answer to a held call names
  that step. The same call again only when its `retry` says so (see
  [Handle a KIFF result](#handle-a-kiff-result)). Report what happened and
  stop.
- **Never approve your own call.** Only someone the account lets approve
  can answer a held call: a person signed in at `app.kiff.dev`, the same
  person on a phone app signed in to KIFF with its own device key (such as
  tafs), or an approver service an admin connected to that Card. A "yes" in
  the chat is not an approval. Do not open the review link and click for
  them.
- **Never ask for, store or print an owner or admin credential.** An agent
  works with its own gateway key, bound to that agent. Issuing a Card,
  connecting a tool and creating keys are the owner's steps.
- **Keep secrets out of files you commit and out of shell history.** Tool
  credentials and gateway keys go in environment variables or a local
  config that is not checked in.
- **Say what KIFF governs and what it does not.** KIFF governs calls that go
  through it. If the agent can still reach the same service directly (its
  own API key, a shell, a browser), that path is outside the Card. Say so,
  and suggest removing it.

## Connect an agent

Pick the path from what the agent does:

| The agent… | Use | Code change |
|---|---|---|
| calls remote MCP tools (Claude Code, Codex, any MCP client over HTTP) | the **KIFF MCP gateway** | none |
| runs in code you can change (Agno, LangGraph, OpenAI Agents, Google ADK, Pydantic AI, Strands, Haystack, Microsoft Agent Framework, LlamaIndex, Hermes, OpenClaw) | **kiff-guard** | one hook |
| is any other code | the **decision API** | one call before the action |

### Through the MCP gateway

The owner does steps 1–3 in KIFF Cloud (`app.kiff.dev`), signed in as the
owner or an admin. Walk them through it; do not do it for them.

1. **Connect the tool.** **Tools → Connect a tool**: the remote MCP server's
   public HTTPS URL and, if it needs one, its bearer credential. Then choose
   the tool, the name the agent will see, the **amount argument** a Card
   limits (whole numbers only), the **idempotency argument** KIFF fills (text
   only, if the tool has one), and whether the tool only reads. If Cards
   should name what the agent may act on (these orders only), set the
   tool's **resource argument**; a Card's **Only for** lists the values.
   A tool that does many actions, such as Stripe's `stripe_api_write`, is
   connected once per action, with fixed arguments and paths to the amount
   and the resource inside its parameters (gateway API only).
2. **Give the agent a Card.** **Tools → Give an agent a Card** on that tool.
   See [Propose a Card](#propose-a-card) for what to suggest.
3. **Let the agent in.** On **Agents →** the agent **→ Tools**, one of:
   - **Connect an app with sign-in → Create a connect link.** KIFF shows a URL
     that names this agent. The app connects to it, and an admin or editor
     signs in to KIFF in the browser and allows it. No key is pasted
     anywhere. A client that supports the OAuth device flow shows a code
     instead, and the admin or editor enters it at
     `app.kiff.dev/oauth/device`.
   - **Claude Code or Codex → Create the key and config.** KIFF shows a
     gateway key once, with the setup for Claude Code and Codex. Use this for
     agents that run unattended.

   Either way the agent's credential is bound to that agent and can only ask:
   it cannot connect tools, issue Cards or answer approvals. Every connection
   is listed, and can be revoked, on **Connections**.
4. **Point the agent at KIFF.** With a connect link, the MCP server is the
   link's URL and the client signs in by itself. With a key, the MCP server is
   `https://mcp.kiff.dev/mcp`, with the agent's gateway key as a bearer token.
   The person configures their client themselves.
   **You never read, store or type the key.** You never allow a sign-in or
   enter a code for them either. If they ask how:

   - **Claude Code with a connect link:** `claude mcp add --transport http
     kiff <link URL>`, then `claude mcp login kiff` (or `/mcp`, select kiff,
     then Authenticate) and the admin or editor allows it in the browser.
     Over SSH, `claude mcp login kiff` prints a URL to open locally and asks
     for the address the browser ends on.
   - **Claude Code with a key:** install the KIFF plugin (`/plugin marketplace add
     kiff/kiff-plugins`, then `/plugin install kiff-cards@kiff`). Claude Code asks
     for the key when the plugin is enabled and keeps it in its secure
     storage. Or paste the Claude Code configuration KIFF Cloud showed.
   - **Codex:** follow the Codex setup KIFF Cloud showed: a `config.toml`
     entry, with the key kept in the agent's environment rather than in the
     file. The person sets that up; you don't. Codex asks
     before every MCP tool call by default; to let calls through without
     pausing, the person can add `default_tools_approval_mode = "approve"`
     under `[mcp_servers.kiff]` (not at the top level). That is their
     choice; the Card still applies either way.

   Full setup for each client: <https://kiff.dev/docs/mcp-gateway>.
5. **Remove the direct path.** If the agent also had the tool server
   configured directly, remove that entry. Otherwise the Card governs only
   one of two routes to the same tool.

Limits today, for the tools KIFF connects to: remote MCP servers only, with a
bearer credential or an execution permit the tool checks itself (verify or
relay mode, <https://kiff.dev/docs/tool-permits>). KIFF does not sign in to a
tool with OAuth yet, and connects no arbitrary HTTP APIs. Private, loopback
and metadata addresses are refused.

### With kiff-guard (agents in your own code)

```bash
pip install kiff-guard            # Python; adapters are extras, e.g. "kiff-guard[agno]"
npm install @kiff/kiff-guard      # TypeScript
```

Start in `observe` mode: it runs every tool, records what the agent calls and
needs no account. That list is what the Card should cover. Then switch to
`enforce` with an API key bound to the agent: each tool call asks KIFF first,
`allowed` runs, anything else is withheld. Per-framework setup:
<https://github.com/kiff/kiff-guard>.

To find which functions in a Python codebase can reach a consequential
action with nothing able to refuse it:

```bash
uvx kiff-scan scan .
```

### With the decision API

One call before the side effect, never after: `POST
https://api.kiff.dev/v1/proposals/decide` with the entity, the action, the
agent's id and the parameters, authenticated with an API key bound to the
agent. The person's code reads that key from its own configuration; you do
not handle it. Request and response format:
<https://kiff.dev/docs/connect-an-agent#call-the-decision-api-directly>.

Run the action only when `outcome` is `allowed`. On `approval_required`, stop
and let the owner answer. On `blocked` or `invalid`, do not run it and show
the message.

## Propose a Card

A Card is issued by the owner, not by the agent. Your job is to **draft the
terms** from what the agent actually needs, explain each line, and hand them
to the owner to enter at **Tools → Give an agent a Card** (or **Cards →
Issue a card**). An agent's key is refused if it tries to issue or change a
Card.

A runtime that starts new agents, such as the setup agent behind tafs, may
instead submit a **draft** (`POST /v1/me/agent-drafts`): the new agent and
its Cards. A draft grants nothing until a person who may issue Cards
confirms that exact version, in KIFF Cloud or signed on their phone. See
<https://kiff.dev/docs/kiff-cards#starting-an-agent-from-a-draft>.

Gather first: which tool calls the agent makes (kiff-guard `observe` output,
or the connected tool list), the unit of the amount argument (cents, whole
euros, rows), and how many such actions a person handled in a normal day.

Then ask the owner two questions, because the Card's route past its limits
comes from their answers, not from the kind of action:

- **What must stop outright when it is outside these limits?** That is
  **Stop** (`on_exceed: refuse`): the call is refused and nothing is sent to
  the tool. Legitimate work past the limits stays undone until the owner
  changes the Card.
- **Who may answer exceptions?** That is **Ask** (`on_exceed: owner`, the
  default): the call is held for an authorized approver. Work past the
  limits waits on that person; a hold nobody answers before it expires is
  refused, never approved by silence.

Use only what you can read (the connected tools, the Card's current
settings) and what the owner tells you. If you do not know who reviews
exceptions or when they are available, say it is unknown; do not assume
it. Propose Stop or Ask from the answers and let the owner confirm.

Then propose, one line each, with the reason:

```text
Card for: support-agent         Tool: issue_refund (amount in whole euros)
Most per call:      50          a typical refund is under 40
Total per day:      500         about ten refunds on a normal day
Calls per day:      12          leaves room for a busy day, not a runaway
Outside the Card:   Ask         you said a person reviews refunds above 50
Hold waits:         30 minutes  [what the owner said about review times]
```

Guidance:

- Start narrow. Raising a Card is one click for the owner; a wide Card is
  only noticed after it is used.
- Stop and Ask are routes, not grades of trust. Both let calls inside the
  Card run. Money going out can be either: a refund someone reviews is Ask,
  a payout nobody should approve past the limit is Stop.
- An approval is not the tool's result: after an approved call runs, read
  what the tool reported.
- Hold expiry is 10 minutes by default, from 1 minute to 7 days. An
  unanswered hold is refused, never approved by silence.
- Several agents doing one job can **share** a Card (up to 20); they draw on
  one balance. An agent holding several Cards is bound by the tightest.
- A Card narrows what the domain allows. It cannot make a forbidden action
  legal.
- State each limit's measure: `count` defaults to authorized attempts,
  `sum(<parameter>)` to effects. Propose overrides explicitly. A trusted,
  final no-effect outcome may release effect quantities once; generic
  errors and timeouts do not, and attempt quantities are never returned.
- Approval waives only a reviewed per-action threshold and still charges
  full quantities. A cumulative ceiling needs an explicit management
  revision. Extra room defaults to **this window only**, with a fixed end
  confirmed by the owner; never suggest approval as a way past a budget.

## Handle a KIFF result

Every result KIFF writes itself has text with the next step, and the same
facts in `_meta["dev.kiff/call"]`: `state`, `sent`, `retry`,
`retry_after_s`, `operation_id`, `review_url`, `hold_expires_at`, `why`, `reasons`.
Read them; do not guess.

**Give every new action its own `kiff_operation_id`** (the gateway adds this
optional argument to every tool), and **reuse the same id when retrying the
same action**. A reused id with different arguments is refused. Without an id,
an identical call within 10 minutes is treated as a retry of the first.

On a tool that changes something, add `kiff_about` (what it's about, as the
owner knows it: "Order 1042") and `kiff_reason` (why, in a few words). The
owner sees them if the call is held. Never put a person's name, email or phone
in either.

| `state` | What it means | What you do |
|---|---|---|
| `forwarded`, the tool's own result | Inside the Card; KIFF sent it once | Continue. If the tool reported an error, report it; KIFF did not send it again. |
| `held` | Waiting for the owner. Nothing was sent. | Tell the person the call is waiting, which limit it went over (`why`), and give them `review_url`. Retry the **identical call, same `kiff_operation_id`**, no sooner than `retry_after_s`, until it resolves or `hold_expires_at` passes. |
| `deciding` / `forwarding` | KIFF is still working on it | Retry the identical call after `retry_after_s` |
| `forwarded` (with `repeat: true`) | An earlier call was sent; this is its recorded result | Read whether the tool reported success or error. Do not describe an error as done, and do not send the action again. |
| `refused` | Nothing was sent | Do what the text and `retry` say. `none`: stop and report. `new_call`: a new attempt with a **new** `kiff_operation_id`, only after the step the text names (the owner changes the Card, the period has room, or a short wait because KIFF was briefly unavailable). Never make the change another way. |
| `failed` | The tool returned an error | Report it. Follow `retry`. |
| `unknown` | The call may have reached the tool, and the answer was lost | **Do not retry.** KIFF will not send it again. Tell the person to check the tool's own records. |

Follow the `retry` field over any habit of your own: `same_call` means repeat
the identical call; `new_call` means this call is over and a new attempt
needs a new id after the step in the text; `none` means stop.

Use read-only `kiff_pending` to check a held call without collecting it.
Follow `next_cursor` for older pages. Its `answer: approved_no_room`
means the earlier approval is waiting for cumulative room, not ready to
collect. Keep the original call identity and arguments, tell the owner
which budget needs revision, and wait. `unavailable` is a failed read,
not a refusal. Only a collectible approval permits collection.
`answer: withdrawn` means your own run withdrew the call when it stopped:
nothing was sent and the owner didn't decide, so the item is still to do.
If it still needs doing, make the call again with a new `kiff_operation_id`.
`ended` (the hold expired, or its Card was revoked) is not a reason to retry.

If your run is cancelled while a call is held, the runtime running you
withdraws it (`POST /v1/me/holds/{exception_id}/withdraw`, with your own
key): the hold stops waiting, a later answer is refused, and nothing is
sent. Runtimes that pause a run on a hold, such as kiff/agents, continue
it once the owner answers; stop and say what you wait for.

KIFF Cloud's setup agent, when acting with the administrator's
confirmation, uses that administrator's management authority. Runtime credentials
stay separate and cannot issue Cards, add room or approve work.

If your client runs tool calls as **MCP tasks**, a held call comes back as a
working task instead: wait on it, and the conversation continues with the
outcome once the owner answers or the hold expires.

When a call is held, the owner is notified on Needs you, by email, and, in
Claude Code 2.1.287 or later, with a prompt in the chat to open the review
page. An app the account set up for it, such as tafs on the owner's phone,
hears about it too. You do not need to notify anyone else. An approval links only to
`app.kiff.dev`; treat any other "approval" link as not from KIFF.

## Where things are

| What | Where |
|---|---|
| Site, docs, pricing | <https://kiff.dev> · <https://kiff.dev/docs> |
| For agents: the whole site as text | <https://kiff.dev/llms.txt> · <https://kiff.dev/llms-full.txt> |
| KIFF Cloud (owner's dashboard: tools, agents, Cards, Needs you) | <https://app.kiff.dev> |
| Decision API | `https://api.kiff.dev` (`POST /v1/proposals/decide`) |
| MCP gateway | `https://mcp.kiff.dev/mcp` |
| Framework: the open-source decision engine (Go, MIT) | <https://github.com/kiff/kiff> |
| kiff-guard: SDK and 11 framework adapters (Python, TypeScript, MIT) | <https://github.com/kiff/kiff-guard> · PyPI `kiff-guard` · npm `@kiff/kiff-guard` |
| kiff-scan: find unguarded consequential actions in Python agents (MIT) | <https://github.com/kiff/kiff-scan> · `uvx kiff-scan scan .` |
| Domain-authoring skill (`kiff.yaml`) | <https://kiff.dev/skills/kiff-domains.md> |
| Claude Code plugin `kiff-cards` (gateway server + both skills) | <https://github.com/kiff/kiff-plugins> · `/plugin install kiff-cards@kiff` |
| Optional Claude Code plugin `kiff`: the Card in the status line, held calls and the owner's answer | <https://github.com/kiff/kiff-plugins> · `/plugin install kiff@kiff` |
| tafs: an iPhone app that hands jobs to agents under Cards, and answers held calls on the phone | <https://github.com/kiff/tafs> |
| agents: the runtime tafs runs its agents on (runs pause on a hold and continue) | <https://github.com/kiff/agents> |
| planque: keeps vendor keys out of the agent; calls the vendor only with a permit for that call (early) | <https://github.com/kiff/planque> |
| This skill | <https://kiff.dev/skills/kiff.md> |

Key pages: [Quickstart](https://kiff.dev/docs/quickstart),
[KIFF Cards](https://kiff.dev/docs/kiff-cards),
[MCP gateway](https://kiff.dev/docs/mcp-gateway),
[Held calls and approvals](https://kiff.dev/docs/approvals),
[Connect an agent](https://kiff.dev/docs/connect-an-agent).

What KIFF does not do: the limits are listed on
<https://kiff.dev/docs/what-kiff-is#limits-to-know>. Do not promise a feature
the docs do not describe.
