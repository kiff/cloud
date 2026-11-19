# A card for every agent.

<div class="sd-opening"><p class="sd-opening-lede">Agents now refund, book, buy and cancel on their own. The owner decides once what each one may do. KIFF checks every action against it before it runs.</p><div class="sd-starts"><div class="sd-start"><span class="sd-label">the owner</span><strong>Issues a Card.</strong><span>Which actions, how much each, how many a day, and what happens outside it.</span></div><div class="sd-start-arrow">then</div><div class="sd-start"><span class="sd-label">KIFF</span><strong>Checks every call.</strong><span>Inside the Card it goes through. Outside, it waits for the owner or is refused.</span></div></div><p class="sd-opening-close">The agent keeps doing the work. The owner keeps the authority.</p></div>

---

## Agents are starting to act on money.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Interfaces</span><strong>Personal and business agents now complete tasks end to end.</strong><span>Instinct raised $1B at a $10B valuation in September 2026 for an agent that buys, books and cancels.</span></div><div class="sd-system-row"><span class="sd-system-name">Payments</span><strong>The rails give agents their own credentials.</strong><span>Stripe's agent tokens are tied to one merchant, time-limited, capped and revocable.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">The gap</span><strong>A payment cap knows one payment.</strong><span>It doesn't know the agent's other actions, its total this week, or the state of the order.</span></div></div>

---

## The rails cap a payment. KIFF caps the agent.

<div class="sd-fragments"><div class="sd-fragment"><span class="sd-label">not only payments</span><strong>Every consequential action</strong><span>Refunds, cancellations, bookings, plan and access changes.</span></div><div class="sd-fragment"><span class="sd-label">over time</span><strong>Totals per window</strong><span>Ten refunds a day, shared by two agents, across every merchant.</span></div><div class="sd-fragment"><span class="sd-label">when nobody is watching</span><strong>The owner answers</strong><span>Outside the Card, the call waits for the owner, and is refused if nobody answers.</span></div></div>

---

## Cheap actions multiply the decisions.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Actions</span><strong>An agent's action costs almost nothing.</strong><span>So teams ask it to do far more of them.</span></div><div class="sd-system-row"><span class="sd-system-name">Volume</span><strong>The number of consequential actions grows.</strong><span>Refunds, credits, access changes, deploys.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">Approval</span><strong>A person approving each one becomes the whole cost.</strong><span>Supervision grows with the work, and the saving stops there.</span></div></div>

---

## Three answers today. None of them scale.

<div class="sd-fragments"><div class="sd-fragment"><span class="sd-label">approve everything</span><strong>A person clicks each one</strong><span>Safe, and the agent saves typing, not work.</span></div><div class="sd-fragment"><span class="sd-label">hand over the key</span><strong>The agent can do anything</strong><span>Fast, and nothing bounds how much.</span></div><div class="sd-fragment"><span class="sd-label">write the limit in</span><strong>A prompt, a config, a function</strong><span>Nobody on the business side can read it, and every change is a deploy.</span></div></div>

---

## What a Card says.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Actions</span><strong>Which actions this agent may take.</strong><span>Refund an order. Not cancel a subscription.</span></div><div class="sd-system-row"><span class="sd-system-name">How much</span><strong>The most per action, and totals per window.</strong><span>€50 a refund, €500 and 10 refunds a day.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">Outside it</span><strong>Hold it for the owner, or refuse it.</strong><span>A held call waits a set time, then it is refused.</span></div><div class="sd-system-row"><span class="sd-system-name">Who holds it</span><strong>One agent, or several sharing one balance.</strong><span>Revoke it, and only that authority stops.</span></div></div>

---

## Correct, and still refused.

<div class="sd-limit"><div class="sd-limit-top"><span>Illustrative: Refund Card, one day</span><strong>€1,100 ceiling</strong></div><div class="sd-limit-bar"><span class="sd-limit-used" style="width:93.82%"></span></div><div class="sd-limit-row"><span>€42 drawn</span><span>€990 drawn</span><strong>€68 left</strong></div><div class="sd-limit-refuse">Next refund: €88. Valid order. Permitted agent. €20 over the ceiling. KIFF holds it before anything runs.</div></div>

An alert tomorrow would tell you what happened. The Card decides what may happen now.

---

## Outside the Card, the owner answers.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Held</span><strong>Nothing reaches the tool.</strong><span>The agent is told the call is waiting, with a link.</span></div><div class="sd-system-row"><span class="sd-system-name">Reached</span><strong>Needs you, an email, and the agent's chat.</strong><span>In Claude Code, the chat offers to open the owner's page.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">Answered</span><strong>Approve once, change the Card, or reject.</strong><span>Only the owner's signed-in session answers. No answer in time: refused.</span></div></div>

---

## See it run.

<div class="sd-launch"><div class="sd-launch-offer"><span class="sd-label">recorded, not mocked</span><strong class="sd-launch-price">1.5 min</strong><span class="sd-launch-period">real model · real gateway · Stripe test mode</span><a class="sd-launch-cta" href="/proof#videos">Watch the demo →</a></div><div class="sd-launch-result"><span class="sd-label">what happens</span><ul><li>A support operator asks the agent for a €24 refund. Inside its Card: it goes through.</li><li>An €86 refund is over the €50 limit. KIFF holds it; nothing reaches Stripe.</li><li>The owner approves it in KIFF.</li><li>The agent, waiting in the same conversation, finishes the refund once.</li></ul></div></div>

---

## A limit you write yourself breaks under load.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Two at once</span><strong>Two runs both read €1,000 left.</strong><span>KIFF locks draws per card: the second is refused.</span></div><div class="sd-system-row"><span class="sd-system-name">Retry</span><strong>A timeout retries the same refund.</strong><span>KIFF keys the call: it counts once, and is sent once.</span></div><div class="sd-system-row"><span class="sd-system-name">Scale</span><strong>The agent runs as three workers, or two agents share a Card.</strong><span>One balance, held centrally: no fresh allowance.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">Outage</span><strong>The balance cannot be read.</strong><span>KIFF refuses. Unknown is never read as zero.</span></div></div>

---

## "Who allowed this?" has an answer.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Issued</span><strong>Who gave this agent which authority, and when.</strong><span>Every change to the Card is attributed.</span></div><div class="sd-system-row"><span class="sd-system-name">Drawn</span><strong>What each authorization used.</strong><span>The statement shows the balance and how it moved.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">Held or refused</span><strong>What was stopped, and why.</strong><span>Each decision has a signed, tamper-evident receipt. The agent can write neither.</span></div></div>

---

## Three ways to connect an agent.

<div class="sd-fragments"><div class="sd-fragment"><span class="sd-label">calls MCP tools</span><strong>The KIFF gateway</strong><span>Point the agent at mcp.kiff.dev. KIFF holds the tool's credential. No code.</span></div><div class="sd-fragment"><span class="sd-label">runs in your code</span><strong>kiff-guard</strong><span>One hook in the agent's tool loop. Eleven frameworks verified.</span></div><div class="sd-fragment"><span class="sd-label">any code</span><strong>The decision API</strong><span>One call before the action. Your code still runs it.</span></div></div>

---

## Where the Card is the final authority.

<div class="sd-system"><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">Today</span><strong>Wherever KIFF is on the only path.</strong><span>Through the gateway, KIFF holds the credential: the agent can't reach the tool around its Card.</span></div><div class="sd-system-row"><span class="sd-system-name">Not by itself</span><strong>An agent that can still act through a browser or a direct credential.</strong><span>Close that path, or KIFF doesn't govern it. We say so.</span></div><div class="sd-system-row"><span class="sd-system-name">Next</span><strong>Tools that verify KIFF, and payments that follow the Card.</strong><span>Direction, not product yet.</span></div></div>

---

## Give one agent a Card.

<div class="sd-launch"><div class="sd-launch-offer"><span class="sd-label">start free</span><strong class="sd-launch-price">10 min</strong><span class="sd-launch-period">Developer plan · no code</span><a class="sd-launch-cta" href="/docs/quickstart">Open the quickstart →</a></div><div class="sd-launch-result"><span class="sd-label">or bring us one action</span><ul><li>One action your agent takes, or should.</li><li>How many a person handled last month.</li><li>The system that runs it.</li><li><a href="mailto:gabriel@kiff.dev?subject=KIFF%20card%20review">Book a 30-minute card review →</a></li></ul></div></div>
