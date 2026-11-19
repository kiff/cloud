# Decide once.<br>Enforced every time.

<div class="sd-opening"><p class="sd-opening-lede">Your agent already proposes refunds, credits or account changes. A person still executes every one.</p><div class="sd-starts"><div class="sd-start"><span class="sd-label">keep the queue</span><strong>Every action waits.</strong><span>The agent drafts. A person finishes it.</span></div><div class="sd-start-arrow">or</div><div class="sd-start"><span class="sd-label">hand it the key</span><strong>Every action runs.</strong><span>The tool can execute. Nothing says how much.</span></div></div><p class="sd-opening-close">The third option: the owner decides the agent's authority once, and KIFF enforces it on every action.</p></div>

---

## Cheap actions multiply the decisions.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Actions</span><strong>An agent's action costs almost nothing.</strong><span>So teams ask it to do far more of them.</span></div><div class="sd-system-row"><span class="sd-system-name">Volume</span><strong>The number of consequential actions grows.</strong><span>Refunds, credits, access changes, deploys.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">Approval</span><strong>A person approving each one becomes the whole cost.</strong><span>Supervision grows with the work, and the saving stops there.</span></div></div>

When something gets cheaper, we use far more of it: the Jevons paradox. Applied to agents, the limit moves from doing the work to deciding it.

---

## Three answers today. None of them scale.

<div class="sd-fragments"><div class="sd-fragment"><span class="sd-label">approve everything</span><strong>A person clicks each one</strong><span>Safe, and the agent saves typing, not work.</span></div><div class="sd-fragment"><span class="sd-label">hand over the key</span><strong>The agent can do anything</strong><span>Fast, and nothing bounds how much.</span></div><div class="sd-fragment"><span class="sd-label">write the limit in</span><strong>A prompt, a config, a function</strong><span>Nobody on the business side can read it, and every change is a deploy.</span></div></div>

---

## Move people from actions to authority.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Before</span><strong>A person approves every refund.</strong><span>400 drafted, 400 executed by hand.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">With a card</span><strong>The owner sets what the agent may do, once.</strong><span>Actions, valid states, conditions, a ceiling over a window.</span></div><div class="sd-system-row"><span class="sd-system-name">After</span><strong>The clear cases run. A person sees the exceptions.</strong><span>Judgment is spent where it matters, not on every click.</span></div></div>

---

## Correct, and still refused.

<div class="sd-limit"><div class="sd-limit-top"><span>Illustrative: Refund Card, one day</span><strong>€1,100 ceiling</strong></div><div class="sd-limit-bar"><span class="sd-limit-used" style="width:93.82%"></span></div><div class="sd-limit-row"><span>€42 drawn</span><span>€990 drawn</span><strong>€68 left</strong></div><div class="sd-limit-refuse">Next refund: €88. Valid order. Permitted agent. €20 over the ceiling. KIFF refuses before execution.</div></div>

An alert tomorrow would tell you what happened. The card decides what may happen now.

---

## Change the authority, not the agent.

<div class="sd-lifecycle-copy"><p>€5,000 a day. €20,000 for Black Friday. €5,000 again on Monday. Revoked at 2 a.m. if something looks wrong.</p><strong>The owner changes the card. The agent keeps running the same code: no prompt edit, no config change, no deploy.</strong><span>issue &middot; widen &middot; narrow &middot; revoke</span></div>
<div class="sd-lifecycle-visual"><div class="sd-life-track"><span class="sd-life-state">Issue</span><span class="sd-life-line"></span><span class="sd-life-state">Widen</span><span class="sd-life-line"></span><span class="sd-life-state">Narrow</span><span class="sd-life-line"></span><span class="sd-life-state sd-life-state-accent">Revoke</span></div></div>

---

## A limit you write yourself breaks under load.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Two at once</span><strong>Two runs both read €1,000 left.</strong><span>KIFF locks draws per card: the second is refused.</span></div><div class="sd-system-row"><span class="sd-system-name">Retry</span><strong>A timeout retries the same refund.</strong><span>KIFF keys the draw to the proposal: it counts once.</span></div><div class="sd-system-row"><span class="sd-system-name">Restart</span><strong>The agent scales to three workers.</strong><span>One balance per card, held centrally: no fresh allowance.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">Outage</span><strong>The balance cannot be read.</strong><span>KIFF refuses. Unknown is never read as zero.</span></div></div>

---

## One holder. Separate cards.

<div class="sd-fragments"><div class="sd-fragment"><span class="sd-label">refunds</span><strong>Refund Card</strong><span>€5,000 / day &middot; REFUND_ORDER</span></div><div class="sd-fragment"><span class="sd-label">credits</span><strong>Store Credit Card</strong><span>€2,000 / day &middot; ISSUE_STORE_CREDIT</span></div><div class="sd-fragment"><span class="sd-label">plans</span><strong>Plan Change Card</strong><span>40 / day &middot; CHANGE_PLAN</span></div></div>

<div class="sd-costline"><span>Revoke one capability</span><strong>without stopping the agent's other work.</strong></div>

---

## "Who allowed this?" has an answer.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Issued</span><strong>Who gave this agent which authority, and when.</strong><span>Every ceiling change is attributed.</span></div><div class="sd-system-row"><span class="sd-system-name">Drawn</span><strong>What each authorization used.</strong><span>The statement shows the balance and how it moved.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">Refused</span><strong>What was stopped, and why.</strong><span>Ceiling, revocation, or a balance that could not be read.</span></div></div>

Issuing and ceiling changes are attributed revisions. Each authorization and refusal has a signed, tamper-evident receipt. The agent can write neither.

---

## Your organization issues it. KIFF checks it.

<div class="sd-system"><div class="sd-system-row"><span class="sd-system-name">Your organization</span><strong>Issues the card.</strong><span>Actions, valid states, conditions, and the ceiling. The owner changes or revokes it; the agent cannot.</span></div><div class="sd-system-row sd-system-row-accent"><span class="sd-system-name">KIFF</span><strong>Checks every proposal.</strong><span>Allow, hold for a person, or refuse, using current state and the card.</span></div><div class="sd-system-row"><span class="sd-system-name">Your system</span><strong>Runs what's allowed.</strong><span>Your code keeps the execution path. KIFF never moves anything itself.</span></div></div>

A card narrows what your domain and permissions already allow. It cannot make a forbidden action legal.

---

## Start with one action.

<div class="sd-launch"><div class="sd-launch-offer"><span class="sd-label">one card, one action</span><strong class="sd-launch-price">Observe first</strong><span class="sd-launch-period">agreed before it starts &middot; founder-led</span><a class="sd-launch-cta" href="/pricing">See how it runs →</a></div><div class="sd-launch-result"><span class="sd-label">what you receive</span><ul><li><strong>Observe.</strong> KIFF on the action&#39;s path, deciding and recording while refusing nothing, on your real traffic.</li><li><strong>Issue.</strong> Your organization issues the card, with a ceiling from the observed number.</li><li><strong>Prove.</strong> Control tests on that path: limits, retries, concurrency, revocation.</li><li><strong>Hand over.</strong> The owner accepts the boundaries; your team runs it.</li></ul></div></div>

Issuing, the tests and the handover are scoped from what observing shows. If almost everything still needs a person, stop there.

---

## Bring us one action.

<div class="sd-launch"><div class="sd-launch-offer"><span class="sd-label">card review</span><strong class="sd-launch-price">30 min</strong><span class="sd-launch-period">founder-led · no cost</span><a class="sd-launch-cta" href="mailto:gabriel@kiff.dev?subject=KIFF%20card%20review">Book the review →</a></div><div class="sd-launch-result"><span class="sd-label">bring three things</span><ul><li>One action your agent proposes.</li><li>How many a person executed unchanged last month.</li><li>The system that would execute it.</li></ul></div></div>
