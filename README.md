<p align="center">
  <img src=".github/kiff-logo.png" width="88" alt="KIFF">
</p>

<h1 align="center">Your agents act for you.<br>Your Cards decide how far.</h1>

<p align="center">
  <b>The enforcement core of KIFF Cloud: it checks every call an AI agent makes<br>against the Card its owner issued, before anything is sent.</b>
</p>

<p align="center">
  <a href="https://kiff.dev">kiff.dev</a> ·
  <a href="https://kiff.dev/docs/what-kiff-is">What KIFF is</a> ·
  <a href="https://kiff.dev/integrate">How it works</a> ·
  <a href="https://kiff.dev/proof">Proof</a> ·
  <a href="https://kiff.dev/invariants">Invariants</a> ·
  <a href="https://kiff.dev/whitepaper">Whitepaper</a> ·
  <a href="https://kiff.dev/docs">Docs</a> ·
  <a href="https://status.kiff.dev">Status</a>
</p>

<p align="center"><img src=".github/readme/art-anne-authority.jpg" alt="An owner hands work to several agents. Every call passes through one checkpoint before it reaches a tool; one call is stopped." width="900"></p>

---

AI agents can now refund an order, pay a bill or answer a dispute. That's
useful right up until one of them is tricked, confused or compromised. Then
the question isn't *"is the model smart?"* any more. It's *"what can it
actually do?"*

Today the answer lives in a prompt, a config file, an `if amount > 500`
someone wrote last quarter, or nowhere at all, behind a key that can do
anything the service allows. The business can't read it, every change is a
release, and **the agent holds the keys.**

**KIFF answers it outside the model.** Each agent gets a **Card**, like a
corporate card: which actions it may take, on which records, how much per
action and in total, and what happens when it asks for more. The agent asks;
KIFF decides, before anything is sent. The tool's sign-in never reaches the
agent. Anything past the Card waits for the owner. Every decision leaves a
signed receipt anyone can check.

This repository is where that's enforced: the MCP gateway at `mcp.kiff.dev`
and the parts of the API at `api.kiff.dev` that decide a call, mint its
permit, apply a halt and write its receipt. Below is its blueprint, drawn
from the code in this repository.

## See it in a real product: tafs

[tafs](https://github.com/kiff/tafs) is KIFF's own app, for iPhone and iPad.
A business hands routine jobs to AI agents, called tafs, and each taf
works under its own Card. Every action a taf takes goes through the gateway and
the decision path in this repository:

- **Inside its Card, a taf acts,** and the action leaves a signed receipt. The
  app's Record shows it, with the receipt checked on the owner's device.
- **Outside its Card, it waits.** The held call appears in *Needs you*, and the
  owner approves it once or refuses it, signed on their own device.
- **Halt all** stops every taf, and nothing new is sent until the owner resumes.
- **Coach mode** sets a taf up: it connects the tools, proposes the Card, and
  the owner approves both.

The tools' sign-ins stay in [planque](https://github.com/kiff/planque); the
tafs themselves never hold them. The same code decides for tafs and for any
agent you connect yourself.

## The blueprint

| | Diagram | Answers |
|---|---|---|
| 1 | [System context](#system-context) | Who touches a call, and who holds which key |
| 2 | [How a Card decides](#cards-the-key) | What a Card checks, and why one balance per Card is the key |
| 3 | [Connected tools](#connected-tools-who-holds-the-key) | sealed, verify and relay: where the credential lives |
| 4 | [One allowed call](#one-call-end-to-end) | A call from the agent to the vendor and back |
| 5 | [The hold](#the-hold) | A call past the Card, and the owner's approval |
| 6 | [Lifecycle of a call](#lifecycle-of-a-call) | Why a call is never sent twice |
| 7 | [The halt](#the-halt) | Stopping an agent, confirmed on the vendor's side |
| 8 | [Receipts](#receipts-from-a-decision-to-a-public-anchor) | From a decision to a public anchor |
| 9 | [Components](#components) | The two Go modules and their packages |
| 10 | [Data model](#data-model) | The tables behind Cards, holds, halts and proof |
| 11 | [Deployment](#deployment) | What runs, and what it keeps |
| 12 | [Network topology](#network-topology) | Trust zones, and the only way out |
| 13 | [Cloud architecture](#cloud-architecture-and-the-kiff-family) | How kiff/cloud fits with tafs and the rest of the family |

The diagrams are drawn with [Archify](https://github.com/tt-a1i/archify);
their sources are in [`.github/readme/src`](.github/readme/src).

## System context

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/01-context-dark.png">
  <img src=".github/readme/01-context.png" alt="System context: the owner sets Cards, approves and halts through the KIFF API. The agent sends tools/call to the KIFF gateway, which asks the API to decide and mint a permit, then sends the call with its permit to planque (which adds the sign-in and calls the vendor) or straight to a tool that checks the permit. The API anchors receipt roots on Base Sepolia, where anyone can check them.">
</picture>

- **The agent asks, KIFF decides.** Every `tools/call` goes through the
  gateway, and KIFF answers *allowed*, *held* or *refused* before anything is
  sent. A clever prompt changes nothing: the Card is outside the model.
- **Nobody hands the agent a key.** The agent holds only its KIFF agent key.
  The tool's sign-in is sealed in KIFF, kept by
  [planque](https://github.com/kiff/planque), or not needed at all because the
  tool checks KIFF's permit.
- **Proof anyone can check.** Every decision leaves a signed receipt, and
  receipts are anchored on a public network.

## Cards: the key

<p align="center"><img src=".github/readme/art-card-control.jpg" alt="A row of agents, each under its own Card; one Card's gauge is nearly full and its agent waits." width="820"></p>

A Card is authority you can hand over, watch being used, and take back.

| On a Card you set | The owner can, at any time, without touching the agent |
|---|---|
| **The action**, such as refunding an order | **Raise or narrow** the limits: €5,000 a day, €20,000 for Black Friday, back on Monday |
| **Which records** it applies to, such as one order or paid orders only | Give **extra room** once, for a busy hour |
| **A limit per action**, such as €500 per refund | **Share** it, so several agents draw on one balance |
| **A total** per day, week or month, as an amount or a count | Choose **approvers**, who answer the calls it holds |
| **An expiry**, if you want one | **Revoke** it at 2 a.m., and only that authority stops |
| **What happens over the limit**: it waits for you, or it's refused | Read its **statement**: what was drawn, what's left, every change and who made it |

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/02-card-dark.png">
  <img src=".github/readme/02-card.png" alt="How a Card decides one call: the call is checked against the domain, the Card and any halt. The Card's balance is locked per draw and read with the draw ledger. If it fits, it is allowed with a permit and sent once; over the limit it is held for the owner or refused.">
</picture>

**The key is the running total.** A per-action limit bounds the size of one
mistake. Only a cumulative limit bounds a bad day, and that is the number
that grows as agents get cheaper to run. So a Card keeps **one balance**,
locked for every draw: two agents sharing a Card can't both spend its last
€100, a retry is counted once, and a balance that can't be read is refused,
never waved through.

### Understand the business first, then structure it

This is the part that takes more than a weekend, and the reason KIFF decides
better than a limit in code.

A Card doesn't float on its own. It **narrows a domain**: a model of how your
business actually works. The records you care about, the states they move
through, the actions that are valid in each state, and who may take them. An
order is `CART → PAID → FULFILLED → RETURNED`. A refund is valid on a paid
order and refused on one already refunded, whoever asks and however the
prompt is worded.

- **The domain lives in a `kiff.yaml`** in your repository. Your coding agent
  writes it with the [kiff-domains skill](https://kiff.dev/docs/domains), from
  what your agents already do:
  [kiff-guard](https://github.com/kiff/kiff-guard) in observe mode learns
  which tools they call and hands you a starter domain from real traffic.
- **A Card constrains; it never confers.** It can only narrow what the domain
  allows. It can never make a forbidden action legal.
- **Observe, then enforce.** A new domain first decides and records on your
  real traffic while refusing nothing. When the numbers look right, it
  enforces.
- **Build once, reuse everywhere.** Every agent you add, in any framework,
  uses the same domain and gets its own Card.

**Why not write the limit yourself?** Anyone can write `if amount > 500`.
Nobody wants every agent to own the lifecycle of authority: who holds it,
what's left, who changed it, how to take it back, and what happens with two
runs at once, a retry, a restart or an outage. That lifecycle is what this
repository is.

## Connected tools: who holds the key

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/03-tools-dark.png">
  <img src=".github/readme/03-tools.png" alt="Connected tools: the gateway forwards only allowed calls. In sealed mode KIFF holds the tool's key; in verify mode nobody holds a key and the tool checks the permit; in relay mode the call goes with its permit to planque, which adds the sign-in it keeps.">
</picture>

Each connected tool has a custody mode that says who holds its credential:

| Mode | Who holds the tool's credential | What the tool receives |
|---|---|---|
| `sealed` | KIFF, encrypted at rest and redacted from results and logs | the call, with that credential |
| `verify` | nobody | the call with a `KIFF-Permit` header, which the tool checks |
| `relay` | [planque](https://github.com/kiff/planque), on your side or KIFF's | the call with its permit; planque adds the sign-in |

A **permit** is an Ed25519 JWT (`typ` `kiff-permit+jwt`) over the SHA-256 of
the call's JCS-canonical arguments. It covers one execution of one exact
call, its lifetime is capped, and arguments the owner fixed (such as the
Stripe operation) are ones the agent can't set. The keys are published at
`https://api.kiff.dev/.well-known/kiff-permit-keys.json`
([docs](https://kiff.dev/docs/tool-permits)).

Every outbound call leaves through one **egress guard**. Tool URLs are
untrusted, so they're checked when a tool is connected and again on the
resolved IP of every connection: no internal or metadata addresses, no
redirects, no proxies.

## One call, end to end

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/04-seq-call-dark.png">
  <img src=".github/readme/04-seq-call.png" alt="Sequence of one allowed call in relay mode: the agent calls the gateway; the gateway records the call and asks the API to decide; the API draws on the Card and returns allowed with a permit; the gateway sends the call with its permit to planque; planque checks the halt with the API, adds the sign-in and calls the vendor; the result returns to the agent with KIFF's key for the call.">
</picture>

## The hold

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/05-seq-hold-dark.png">
  <img src=".github/readme/05-seq-hold.png" alt="Sequence of a held call: a €900 refund is over the €500 per-action limit, so the API holds it and nothing is sent. The agent is told to wait. The owner is asked to approve that exact call and approves once. The agent retries the same call with the same kiff_operation_id, the API allows it with one permit, and the gateway sends it once.">
</picture>

When a call goes past the Card, **nothing runs until the owner answers**.
They approve *that exact call*, once, on the resource it named. An approval
that isn't used is withdrawn, a new breach after it waits for room, and a
hold nobody answers expires as refused. If a call like this should be
normal, the owner changes the Card instead.

## Lifecycle of a call

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/08-lifecycle-dark.png">
  <img src=".github/readme/08-lifecycle.png" alt="Gateway call states: deciding, then forwarding, then forwarded. From deciding a call can be held, refused, or fail without a permit. A held call is approved and retried into forwarding, or rejected, expired or halted into refused. A crash mid-send leaves it unknown, never re-sent.">
</picture>

The gateway records a call **before** it is decided; a call it hasn't
recorded is never sent. One attempt wins the move to `forwarding`, and every
other retry reads that attempt's answer. A call whose outcome is lost is
recorded as `unknown` and never sent again.

## The halt

<p align="center"><img src=".github/readme/art-incident.jpg" alt="A fleet of agents at work; a few are stopped in red while the owner watches from a control desk." width="820"></p>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/06-seq-halt-dark.png">
  <img src=".github/readme/06-seq-halt.png" alt="Sequence of a halt: the owner halts an agent and KIFF stores it at once. KIFF asks planque, in a request signed for that halt, to confirm; planque reads the halt back and confirms, and only then is it shown as halted. New calls are refused at decision time. A call carrying a permit issued before the halt is refused by planque at dispatch.">
</picture>

One tap halts an agent, or the whole account. KIFF decides none of its calls
from that moment: new ones, held ones and retries are refused. The halt reads
as **halted, not halting, only once each planque has confirmed** it can see
it, so a permit issued just before the halt can't slip through on the
vendor's side. After a resume, only new calls go through.

## Receipts: from a decision to a public anchor

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/07-receipts-dark.png">
  <img src=".github/readme/07-receipts.png" alt="Receipts: every decision and halt event becomes a receipt with a causal chain, saved per account. Its chain hash is signed with secp256k1; the signed receipt becomes a salted keccak-256 leaf whose Merkle root is submitted to the KiffAnchor contract on Base Sepolia. kiff-verify checks a record against the anchored root using the batch from the receipts API.">
</picture>

**Don't take our word for it.**

1. **Get a receipt.** `GET https://api.kiff.dev/v1/receipts/{trace_id}`, with
   your account's key, returns it
   ([docs](https://kiff.dev/docs/receipts-and-proof)). Its `attestation` is a
   secp256k1 signature over the receipt's causal-chain hash;
   `receipt.VerifyAttestation` recovers the signer and compares it with
   `attestation.wallet_address`.
2. **Check the anchor, in public.** Receipts are anchored on **Base Sepolia,
   a public test network**, by the KiffAnchor contract at
   [`0x2F5468eE0De25c6a86D5F1445F37E6a79039DEa4`](https://sepolia.basescan.org/address/0x2F5468eE0De25c6a86D5F1445F37E6a79039DEa4).
   With the batch for a receipt, `kiff-verify` checks the record is in a
   batch that contract anchored:

```
cd api && go run ./cmd/kiff-verify --rpc https://sepolia.base.org \
  --contract 0x2F5468eE0De25c6a86D5F1445F37E6a79039DEa4 \
  --record-id <record id> --batch batch.json
```

A test network shows the mechanism end to end, in public, but it carries
**no economic finality and isn't a durability guarantee**. Anchoring on Base
mainnet is supported in the code and **not offered**: every plan anchors on
Base Sepolia. The signed receipt is the guarantee; the anchor is a public
witness.

## Components

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/09-components-dark.png">
  <img src=".github/readme/09-components.png" alt="Components: the gateway module (cmd/gateway, gateway, invocation, secret, egress, kiffapi) and the api module (API HTTP layer, mandate, domain, permit, halts, receipt, anchor). The API HTTP layer is not yet published.">
</picture>

```
gateway/                  the MCP gateway (its own Go module)
  cmd/gateway             the server
  cmd/testtool            an MCP tool for tests that counts the calls it gets
  internal/gateway        tools/call: ask KIFF, hold or refuse, send once
  internal/invocation     the call ledger: one send per approval, never re-sent
  internal/egress         which tool URLs may be reached (no internal addresses)
  internal/secret         sealed tool credentials, redacted from results and logs
  internal/kiffapi        the gateway's client for the API
api/                      the decision path (a second Go module)
  internal/mandate        Cards: limits per subject, holds and the decision
  internal/proposals      decided operations
  internal/permit         execution permits: Ed25519 JWTs over the exact arguments
  internal/halts          the owner's halt, and planque's confirmation of it
  internal/receipt        receipts, their attestation and its verification
  internal/anchor         anchoring receipt batches; Merkle proofs
  cmd/kiff-verify         checks a record against an anchored batch
  internal/...            what those packages import
```

**Not yet published:** the orchestration that ties a decision together in the
API's HTTP layer, including refusing a halted agent's calls at decision time.
That layer is bundled with billing, support and operations code today; the
orchestration moves to its own package after v1 and is published then.

## Data model

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/10-erd-dark.png">
  <img src=".github/readme/10-erd.png" alt="Core tables with key columns: Cards (mandates, mandate_holders, mandate_draws, mandate_revisions), Holds (mandate_exceptions, mandate_exception_entries), Halts (halts, halt_events), Gateway (tools, invocations, dedup_slots) and Proof (permit_bindings, receipts).">
</picture>

Key columns only; the full schemas are in each package's `sql/` folder. A
Card is one `mandates` row, and its balance comes from its draws in the
window. A held call is a `mandate_exceptions` row; approving it lets it draw
once. The gateway's ledger is separate: it decides nothing, it makes sure
nothing is sent twice. Relationships are by id; not all of them are
database foreign keys.

## Deployment

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/11-deploy-dark.png">
  <img src=".github/readme/11-deploy.png" alt="Deployment: a gateway service and an API service, each with background loops (gateway sweeps every minute; the receipt pipeline reconciles and anchors) and its own PostgreSQL schema. Signing keys sign for the API. The receipt pipeline submits roots to Base Sepolia over public RPC.">
</picture>

Two services, two modules. The gateway reaches the API only over HTTP
(`KIFF_API_URL`). Key material stays server-side: Ed25519 for permits and
signed halt asks, secp256k1 for receipt attestations and anchoring. Hosting,
networks and deploy configuration aren't part of this export.
[status.kiff.dev](https://status.kiff.dev) runs apart from KIFF's own
services.

## Network topology

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/12-network-dark.png">
  <img src=".github/readme/12-network.png" alt="Network topology: the owner and the untrusted agent reach api.kiff.dev and mcp.kiff.dev over TLS. Inside the KIFF trust boundary, allowed calls leave only through the egress guard, with their permit, to planque or your own tool on the customer side. planque calls the vendor with the sign-in. The API anchors on Base Sepolia and publishes the permit keys.">
</picture>

What crosses each line: agent to KIFF, the call and never a vendor key; KIFF
to the tool, the call with its permit; planque to the vendor, the call with
the sign-in planque keeps.

## Cloud architecture and the KIFF family

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/readme/13-cloud-dark.png">
  <img src=".github/readme/13-cloud.png" alt="The KIFF family: tafs, KIFF's own app, runs on agents, and every task is decided by kiff/cloud. kiff/cloud is built on the kiff framework, sends permits to planque, which adds the sign-in for your tools, and anchors receipt roots on Base Sepolia. Below, the developer tools: kiff-plugins, kiff-guard and kiff-scan.">
</picture>

| Repository | What it is |
|---|---|
| **kiff/cloud** | You are here: the enforcement core. |
| [kiff/tafs](https://github.com/kiff/tafs) | KIFF's own app: hand routine work to agents, each under its Card, and keep the keys. The app and its cloud service. |
| [kiff/planque](https://github.com/kiff/planque) | Keeps your tools' sign-ins and sends the calls KIFF allows. |
| [kiff/agents](https://github.com/kiff/agents) | The runtime tafs' agents run on. |
| [kiff/kiff](https://github.com/kiff/kiff) | The KIFF framework (MIT): the open-source engine behind KIFF Cards, run on your own machine. |
| [kiff/status](https://github.com/kiff/status) | The source of [status.kiff.dev](https://status.kiff.dev). |
| [kiff/kiff-plugins](https://github.com/kiff/kiff-plugins) | KIFF for Claude Code: the gateway, and skills to connect an agent and handle held calls. |
| [kiff/kiff-guard](https://github.com/kiff/kiff-guard) | Put an agent you already run under a Card (MIT): Python and TypeScript, with adapters for Agno, LangGraph, OpenAI, Google ADK, Strands, n8n and your own code. |
| [kiff/kiff-scan](https://github.com/kiff/kiff-scan) | *What can your AI agent do without asking?* Finds functions a model can call that delete data, deploy, move money or run a shell, with nothing on the path that can refuse. `uvx kiff-scan scan .` (MIT) |

## What this code enforces

Each guarantee is stated as narrowly as its tests prove, and every test named
here is in this repository. Tests whose doc comment carries an
`// Invariant:` line are also listed on
[kiff.dev/invariants](https://kiff.dev/invariants).

| Guarantee | Proved by |
|---|---|
| **Every call is decided.** The gateway sends a call only once KIFF has allowed it, and a call it hasn't recorded is never sent.<br><sub>`gateway/internal/gateway`</sub> | <sub>`TestWithinCardForwardsOutsideHolds`<br>`TestUnrecordedCallIsNotSent`</sub> |
| **No key in the agent's hands.** On `verify` the tool gets the call with its permit and no credential. On `relay` the call goes to planque with its permit and a transport credential for the relay; the tool's own sign-in stays in planque. A call allowed without its permit isn't sent.<br><sub>`gateway/internal/gateway`</sub> | <sub>`TestVerifyModeSendsThePermitAndNoCredential`<br>`TestRelayModeSendsTransportCredentialAndPermit`<br>`TestAllowedWithoutPermitIsNotSent`</sub> |
| **A permit covers one exact call.** An Ed25519 JWT over the SHA-256 of the call's canonical arguments; the arguments sent are those it was minted for.<br><sub>`api/internal/permit`, `gateway/internal/gateway`</sub> | <sub>`TestPermitBindsTheFixedArguments`<br>`TestArgsSHA256IgnoresOrderAndWhitespace`<br>`TestLifetimeIsCapped`</sub> |
| **Limits hold.** A call past a Card's limit is refused or held, totals count every allowed call, and an expired Card doesn't fall through to no limit. Room comes back only on the tool's own trusted, final outcome that nothing was done; the operation stays spent and is never sent again.<br><sub>`api/internal/mandate`</sub> | <sub>`TestPerActionLimit`<br>`TestCumulativeLimitRefusesTheActionThatCrossesIt`<br>`TestExpiredMandateDoesNotFallThroughToUnbounded`<br>`TestMandateFailsClosedUnlikeTheUsageLimiter`<br>`TestReleasedEffectsRestoreRoomWithoutReturningAttempts`<br>`TestMixedEffectReleaseIdempotentAndOperationStaysSpent`</sub> |
| **Approval is narrow.** Approving a held call covers that call once, on the resource it named. An unused approval is withdrawn, and a new breach after it waits for room.<br><sub>`api/internal/mandate`</sub> | <sub>`TestApproveOnce`<br>`TestResourceOutsideComesHomeAndApprovalBindsTheResource`<br>`TestUnusedApprovalIsWithdrawn`<br>`TestNewCumulativeBreachAfterApprovalWaitsForRoom`</sub> |
| **One send per approval.** An approved call is sent once, even under concurrent retries. A call whose outcome is lost is recorded as unknown and never re-sent.<br><sub>`gateway/internal/gateway`</sub> | <sub>`TestApprovedHoldForwardsOnceUnderConcurrentRetries`<br>`TestCrashWhileForwardingIsUnknownAndNotResent`</sub> |
| **The halt reaches the vendor's side.** A halt is shown as done only once each planque has confirmed it, over a request signed for that halt.<br><sub>`api/internal/halts`</sub> | <sub>`TestPlanquesConfirmOnlyWhenEachSeesTheHalt`<br>`TestPlanquesAskIsSignedForThatHalt`</sub> |
| **Receipts are verifiable.** A receipt's causal chain has one pinned encoding, its signature fails on any change, and a tampered receipt fails verification against its anchor.<br><sub>`api/internal/receipt`</sub> | <sub>`TestCausalChainCanonicalEncodingIsPinned`<br>`TestAnchorAttester_TamperedReceiptFailsOnchainVerify`</sub> |

### Where it stops, said plainly

- **Credentials handed to an agent outside KIFF and planque.** "No key in the
  agent's hands" holds on supported connections, the ones KIFF's gateway and
  planque make.
- **Undoing what a vendor already did.** A halt stops new calls; a request
  already sent may still complete.
- **The trusted boundary** is KIFF, planque and your approval device. Attacks
  on your own servers, or on a device you approve with, are outside it.
- **Part of the halt isn't here yet.** Refusing a halted agent's calls at
  decision time lives in the API's HTTP layer, which isn't published yet.
  planque's own check at dispatch is public in
  [kiff/planque](https://github.com/kiff/planque).

## Build and test

Go 1.25 or later. Each module builds and tests on its own:

```
cd gateway && go test -race ./...
cd api && go test -race ./...
```

Tests that need Postgres skip unless `KIFF_TEST_POSTGRES_URL` points at a
database the tests may create databases in, for example
`postgres://kiff:kiff@localhost:5432/postgres?sslmode=disable`. Run them with
`TZ=UTC`.

## How this repository works

It's an export of KIFF's private repository, taken at the commit named in
[`SOURCE`](./SOURCE), so the code you read is the code that runs. Pull
requests aren't merged here: changes land in the private repository and
reach this one with the next export.

## Security

Found a way around a Card, a permit, a halt or a receipt? We want to hear it
first. Report it privately: see [SECURITY.md](./SECURITY.md).

## License

[FSL-1.1-ALv2](./LICENSE.md): use, change and redistribute it for any purpose
other than a competing product. Each version becomes Apache 2.0 two years
after it's released.
