# Authority for a fleet of agents.

<p class="inv-lede">KIFF lets the owner decide what each AI agent may do, and change it in seconds, without touching the agent.</p>

---

## Meet Pete. His agents work.

<p class="inv-lede">Pete builds agents for other companies. They read the case, check the order and propose the right refund. His clients are happy.</p>

<p class="inv-body">Each agent gets a key to the tools it needs. A key lets it reach the refund tool. It cannot say how much that agent may spend today.</p>

---

## Success brings twenty agents. And a bottleneck.

<div class="inv-steps"><div><strong>Limits live in code.</strong><span>Twenty prompts and config files only Pete can read.</span></div><div><strong>Every change is a release.</strong><span>A busy week means a ticket and a deploy.</span></div><div class="inv-hit"><strong>Every incident calls Pete.</strong><span>At 2 a.m., the only way to stop an agent is Pete.</span></div></div>

---

## Then Pete meets Anne.

<p class="inv-lede">Anne runs operations with her own fleet of agents. She doesn't write limits into them. She issues each one a card.</p>

<p class="inv-body">The card says which actions, in which states, and how much. It lives outside the agent. To change what an agent may do, Anne changes the card.</p>

---

## A key reaches the tool. A card says how far.

<table class="inv-table"><thead><tr><th></th><th>Key, limits in code</th><th>KIFF Card</th></tr></thead><tbody><tr><td>Which actions</td><td>Anything the tool allows</td><td>Named actions only</td></tr><tr><td>How much</td><td>A counter in each agent</td><td>A ceiling, one balance per card</td></tr><tr><td>Who changes it</td><td>Pete, with a deploy</td><td>The owner, instantly</td></tr></tbody></table>

---

## Incidents: stop three agents, not twenty.

<p class="inv-lede">Refunds spike on three agents at 2 a.m. The client revokes their three cards. The other seventeen keep working. Nobody calls Pete.</p>

---

## Busy weeks: more room, no release.

<p class="inv-lede">Black Friday: the client raises eight cards from €5,000 to €20,000 a day, and back on Monday. Same agents, same code, every change on the record.</p>

---

## New models: ship on a narrower card.

<p class="inv-lede">Five agents move to a new model. They start on narrower cards and widen when their statements look right. The other fifteen never change.</p>

---

## Anyone can write a limit.

<p class="inv-lede">Nobody wants every deployment to own the lifecycle of authority: who holds it, what is left, who changed it, how to take it back.</p>

<table class="inv-table"><tbody><tr><td>Twenty agents, twenty balances</td><td>Each agent draws on its own card. One agent can never spend another's authority.</td></tr><tr><td>Two runs of one agent spend at once</td><td>Draws on that agent's card are locked. The second is refused if the first leaves too little.</td></tr><tr><td>A request is retried</td><td>Counted once.</td></tr><tr><td>The balance cannot be read</td><td>Refused, never treated as zero.</td></tr></tbody></table>

<p class="inv-note">Proof that this is infrastructure worth centralizing, not the reason to buy it.</p>

---

## Pete now builds every agent with a card.

<p class="inv-lede">Pete doesn't adopt KIFF because he can't code a limit. He adopts it because authority should no longer belong to each agent implementation.</p>

<div class="inv-steps"><div><strong>He learned.</strong><span>Authority belongs outside the agent, with the owner.</span></div><div><strong>He updated.</strong><span>Each new agent reuses the same domain and receives its own card.</span></div><div class="inv-hit"><strong>He improved.</strong><span>Clients run their own fleet. Pete builds instead of firefighting.</span></div></div>

---

## Why now: cheap agents create fleets.

<p class="inv-lede">Agents are becoming cheap to build and cheap to run. That creates fleets. Fleets turn authority from application logic into infrastructure.</p>

---

## Traction: shipped and verified. Not yet sold.

<table class="inv-table"><tbody><tr><td><span class="inv-dot inv-ok"></span>Shipped</td><td>Hosted decision runtime, cards, card inventory and statements, signed receipts. MIT framework.</td></tr><tr><td><span class="inv-dot inv-ok"></span>Verified</td><td>A Refund Card on a real production run.</td></tr><tr><td><span class="inv-dot inv-off"></span>To prove</td><td>Paying customers running cards across real fleets.</td></tr></tbody></table>

---

## Business: builders carry KIFF to clients.

<p class="inv-lede">Distribution hypothesis: builders bring the authority layer with the agents they ship.</p>

<p class="inv-body">The framework is open source, so a builder can adopt it without a contract. Each client pays for Cloud, the recurring layer: a platform fee plus usage. Adoption starts founder-led: observe one action on real traffic, then issue the cards.</p>

<p class="inv-note">A hypothesis to prove, not a proven channel.</p>

---

## The real competitor: building it yourself.

<p class="inv-lede">KIFF makes authority a first-class object outside the agent.</p>

<table class="inv-table"><tbody><tr><td>Independently owned</td><td>By the business, not by the code that uses it.</td></tr><tr><td>Changeable without a redeploy</td><td>Raised, narrowed or revoked while the agent runs.</td></tr><tr><td>Stateful and consumable</td><td>One balance, drawn down by every authorization.</td></tr><tr><td>Versioned and auditable</td><td>Every change attributed; every decision on a signed receipt.</td></tr></tbody></table>

<p class="inv-note">Payment-focused agent spend controls bound money leaving a wallet. KIFF bounds any business action, checked against live state.</p>

---

## Team: Gabriel, founder.

<p class="inv-lede">Gabriel is a serial entrepreneur and builder. The investor is backing him.</p>

<p class="inv-body">Proof of velocity: the framework, the hosted runtime and the cards were shipped by Gabriel working with two AI engineering agents, running on Claude and GPT: <a href="https://github.com/gabosarmiento">github.com/gabosarmiento</a> and <a href="https://github.com/agentgabo">github.com/agentgabo</a>.</p>

---

## What the money builds.

<div class="inv-cols"><div><h3>Fleet control for owners</h3><p>See, change and stop authority across many agents at once, not one card at a time.</p></div><div><h3>The builder's view</h3><p>Builders like Pete run every client deployment from one place.</p></div></div>

<p class="inv-body">Later, enabled by the authority layer: judgment for held exceptions by fast decision models such as TypeSafe's Jev, always inside the card, with low confidence still going to a person.</p>

<p class="inv-note">Planned, not built. Today, held proposals go to a person, and revocation is one action per card.</p>

---

## Where KIFF goes: from a card to the standard stack.

<ol class="inv-timeline"><li><span>Month 0</span><strong>The card works.</strong><em>Verified on a production run.</em></li><li><span>Month 6</span><strong>Builders trust it in production.</strong><em>First builders running KIFF for paying clients.</em></li><li><span>Month 12</span><strong>Builders reuse it across customers.</strong><em>Builders running KIFF for two or more clients.</em></li><li><span>Month 18 to 24</span><strong>Part of the standard agent stack.</strong><em>New agents ship with a card by default.</em></li></ol>

---

## The ask: $8M.

<ol class="inv-chain"><li>Builders adopt</li><li>Some reuse KIFF across clients</li><li>Customers expand cards across their fleets</li><li>ARR follows</li></ol>

<div class="inv-cols"><div><h3>First 12 months</h3><p>Fleet control and exception handling shipped, builder operations live, 25 to 40 paying production customers expanding from one card to multiple agents and workflows, and $1M to $1.5M ARR.</p></div><div><h3>18 to 24 months</h3><p>KIFF as the authority layer for autonomous agents: 75 to 100+ production customers, toward $3M to $5M ARR, with clear expansion economics.</p></div></div>
