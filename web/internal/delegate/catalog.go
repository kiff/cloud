package delegate

// The Card gallery. Each blueprint's sentence is written so every role
// appears exactly once, and its samples are chosen so the holder does
// real work first and then meets a limit, because a Card is easiest to
// understand at the moment it says no.

// Default is the blueprint /delegate opens with.
const Default = "eu-refunds"

// Catalog is every blueprint, business first.
var Catalog = []Blueprint{
	{
		ID: "eu-refunds", Name: "EU Refunds", Audience: Business, Owner: "Retailer Co.",
		Summary: "Lets a support agent close refund tickets without a person approving each one.",
		Sentence: []Part{
			{"Let the agent ", RoleText},
			{"resolve refunds", RoleAction},
			{" on ", RoleText},
			{"eligible EU orders", RoleObject},
			{", ", RoleText},
			{"up to €500 each", RolePerAction},
			{" and ", RoleText},
			{"€30,000 this week", RoleCeiling},
			{". ", RoleText},
			{"Bring anything else back to us", RoleFallback},
			{".", RoleText},
		},
		Skill: "refund execution", Action: "resolve refunds", Object: "eligible EU orders",
		Unit: "€", PerAction: 500, Ceiling: 30000, Window: "this week", Fallback: "comes home to you",
		Why: map[Role]string{
			RolePerAction: "Most refunds are small. Anything over €500 is unusual enough that a person should look at it.",
			RoleCeiling:   "A normal week of refunds for the EU store, with room to spare. Past this, something is off: a bug, a fraud wave, a loop.",
			RoleObject:    "Only EU orders, so the Card cannot be used on a region with different refund rules.",
		},
		UseCases: []string{
			"A support agent answering refund tickets overnight.",
			"A returns agent that settles damaged-in-transit claims.",
			"Moving refunds from one agent vendor to another without losing this week's count.",
		},
		Samples: []Sample{
			{What: "Refund order #8812, damaged in transit", Amount: 200},
			{What: "Refund order #8820, wrong size", Amount: 350},
			{What: "Refund order #8831, order never arrived", Amount: 1500},
			{What: "Refund order #8840, duplicate charge", Amount: 400},
		},
	},
	{
		ID: "supplier-payments", Name: "Supplier Payments", Audience: Business, Owner: "Retailer Co.",
		Summary: "Lets a finance agent pay approved supplier invoices on time.",
		Sentence: []Part{
			{"Let the agent ", RoleText},
			{"pay invoices", RoleAction},
			{" from ", RoleText},
			{"approved suppliers", RoleObject},
			{", ", RoleText},
			{"up to €2,000 each", RolePerAction},
			{" and ", RoleText},
			{"€40,000 this month", RoleCeiling},
			{". ", RoleText},
			{"Bring anything else back to us", RoleFallback},
			{".", RoleText},
		},
		Skill: "invoice payment", Action: "pay invoices", Object: "approved suppliers",
		Unit: "€", PerAction: 2000, Ceiling: 40000, Window: "this month", Fallback: "comes home to you",
		Why: map[Role]string{
			RolePerAction: "Routine invoices sit under €2,000. Bigger ones usually carry a contract a person should check.",
			RoleCeiling:   "The month's expected supplier spend. A payment run that goes past it stops instead of draining the account.",
			RoleObject:    "Only suppliers already approved, so a convincing invoice from a new name cannot be paid.",
		},
		UseCases: []string{
			"Paying recurring supplier invoices on their due date.",
			"Clearing a backlog of small invoices after a holiday.",
		},
		Samples: []Sample{
			{What: "Pay invoice INV-2026-0831 (packaging)", Amount: 1200},
			{What: "Pay invoice INV-2026-0839 (logistics)", Amount: 1800},
			{What: "Pay invoice INV-2026-0842 (new warehouse lease)", Amount: 9500},
		},
	},
	{
		ID: "production-rollback", Name: "Production Rollback", Audience: Business, Owner: "Platform team",
		Summary: "Lets an on-call agent roll back a bad deploy without waking someone up.",
		Sentence: []Part{
			{"Let the agent ", RoleText},
			{"roll back deploys", RoleAction},
			{" of ", RoleText},
			{"the checkout and search services", RoleObject},
			{", ", RoleText},
			{"one at a time", RolePerAction},
			{" and ", RoleText},
			{"3 rollbacks today", RoleCeiling},
			{". ", RoleText},
			{"Bring anything else back to us", RoleFallback},
			{".", RoleText},
		},
		Skill: "deploy rollback", Action: "roll back deploys", Object: "checkout and search services",
		Unit: "rollbacks", PerAction: 0, Ceiling: 3, Window: "today", Fallback: "comes home to you",
		Source: "Rule engine",
		Why: map[Role]string{
			RoleCeiling: "One or two rollbacks fix a bad release. A fourth in a day means the problem is not the release.",
			RoleObject:  "Two services where a rollback is safe and well tested. Databases and payments are not on this Card.",
		},
		UseCases: []string{
			"An on-call agent that reverts a release when error rates spike.",
			"Night and weekend cover without a person watching dashboards.",
		},
		Samples: []Sample{
			{What: "Roll back checkout v412 (error rate 8%)", Amount: 1},
			{What: "Roll back search v97 (latency 3×)", Amount: 1},
			{What: "Roll back checkout v413 (error rate 6%)", Amount: 1},
			{What: "Roll back checkout v414 (error rate 7%)", Amount: 1},
		},
	},
	{
		ID: "plan-changes", Name: "Plan Changes", Audience: Business, Owner: "Subscription Co.",
		Summary: "Lets a retention agent move customers between plans when they ask.",
		Sentence: []Part{
			{"Let the agent ", RoleText},
			{"change subscription plans", RoleAction},
			{" for ", RoleText},
			{"customers who ask in chat", RoleObject},
			{", ", RoleText},
			{"one at a time", RolePerAction},
			{" and ", RoleText},
			{"40 changes today", RoleCeiling},
			{". ", RoleText},
			{"Bring anything else back to us", RoleFallback},
			{".", RoleText},
		},
		Skill: "plan management", Action: "change subscription plans", Object: "customers who ask in chat",
		Unit: "changes", PerAction: 0, Ceiling: 40, Window: "today", Fallback: "comes home to you",
		Why: map[Role]string{
			RoleCeiling: "A busy day is about 30 plan changes. Forty leaves headroom; a runaway loop hits the wall early.",
			RoleObject:  "Only customers who asked, so the agent cannot move accounts on its own initiative.",
		},
		UseCases: []string{
			"Downgrading a customer who would otherwise cancel.",
			"Upgrading a team that outgrew its plan, mid-conversation.",
		},
		Samples: []Sample{
			{What: "Downgrade sub-4471 to Basic", Amount: 1},
			{What: "Upgrade sub-4502 to Team", Amount: 1},
		},
	},
	{
		ID: "flight-rescue", Name: "Flight Rescue", Audience: Personal, Owner: "you",
		Summary: "When a flight is cancelled while you are in the air, the agent rebooks you before the seats are gone.",
		Sentence: []Part{
			{"When my flight is disrupted, let my agent ", RoleText},
			{"rebook me and find a bed", RoleAction},
			{" for ", RoleText},
			{"this trip to Tokyo", RoleObject},
			{", ", RoleText},
			{"up to €600 per booking", RolePerAction},
			{" and ", RoleText},
			{"€1,500 for the trip", RoleCeiling},
			{". ", RoleText},
			{"Bring anything else back to me", RoleFallback},
			{".", RoleText},
		},
		Skill: "travel booking", Action: "rebook flights and hotels", Object: "the Tokyo trip",
		Unit: "€", PerAction: 600, Ceiling: 1500, Window: "for the trip", Fallback: "comes home to you",
		Why: map[Role]string{
			RoleAction:    "When a flight is cancelled, the last seats go in minutes. You are in the air or asleep; asking you first means sleeping at the gate.",
			RolePerAction: "Covers an economy rebooking or a night near the airport. A €1,200 business seat is your call.",
			RoleCeiling:   "What a bad travel day can reasonably cost. Past it, something bigger is going on and you should know.",
		},
		UseCases: []string{
			"Rebooking the connection while you are still on the delayed first leg.",
			"Grabbing the last airport hotel room at 1 a.m.",
			"The same Card, handed to a different travel agent next trip, without a new budget.",
		},
		Samples: []Sample{
			{What: "Rebook NH 216 to tomorrow 07:40", Amount: 240},
			{What: "Airport hotel, 1 night", Amount: 180},
			{What: "Upgrade to business on the only seat left today", Amount: 1200},
			{What: "Train to the other airport", Amount: 60},
		},
	},
	{
		ID: "ticket-drop", Name: "Ticket Drop", Audience: Personal, Owner: "you",
		Summary: "Tickets sell out in minutes. The agent buys the moment they drop, while you are in a meeting.",
		Sentence: []Part{
			{"Let my agent ", RoleText},
			{"buy tickets the moment they drop", RoleAction},
			{" for ", RoleText},
			{"the three shows on my list", RoleObject},
			{", ", RoleText},
			{"up to €180 per ticket", RolePerAction},
			{" and ", RoleText},
			{"€600 this season", RoleCeiling},
			{". ", RoleText},
			{"Bring anything else back to me", RoleFallback},
			{".", RoleText},
		},
		Skill: "shopping", Action: "buy tickets at release", Object: "three shows on your list",
		Unit: "€", PerAction: 180, Ceiling: 600, Window: "this season", Fallback: "comes home to you",
		Why: map[Role]string{
			RoleAction:    "Popular shows sell out in under five minutes. By the time the agent asks and you answer, they are gone.",
			RolePerAction: "Face value for good seats. Resale at three times the price is a decision for you, not a script.",
			RoleObject:    "Only the shows you picked, so a clever listing cannot talk the agent into something else.",
		},
		UseCases: []string{
			"Release day falls in the middle of your working day.",
			"Queues that open at 3 a.m. in another time zone.",
		},
		Samples: []Sample{
			{What: "2 tickets, Friday show, floor", Amount: 170},
			{What: "1 ticket, Saturday show, balcony", Amount: 95},
			{What: "Resale ticket, Sunday show", Amount: 420},
			{What: "1 ticket, Sunday show, released late", Amount: 150},
		},
	},
	{
		ID: "home-away", Name: "Home While Away", Audience: Personal, Owner: "you",
		Summary: "A sensor spots a leak while you are abroad. The agent books the plumber and orders the part.",
		Sentence: []Part{
			{"While I am away, let my home agent ", RoleText},
			{"book repairs and order parts", RoleAction},
			{" for ", RoleText},
			{"problems my sensors report", RoleObject},
			{", ", RoleText},
			{"up to €250 per job", RolePerAction},
			{" and ", RoleText},
			{"€800 this month", RoleCeiling},
			{". ", RoleText},
			{"Bring anything else back to me", RoleFallback},
			{".", RoleText},
		},
		Skill: "home services", Action: "book repairs and order parts", Object: "problems your sensors report",
		Unit: "€", PerAction: 250, Ceiling: 800, Window: "this month", Fallback: "comes home to you",
		Source: "Rule engine",
		Why: map[Role]string{
			RoleAction:    "A leak does its damage in hours. The agent acts on the sensor reading instead of waiting for you to land.",
			RoleObject:    "Only what a sensor actually reports, so a phishing email about an 'urgent repair' is not enough.",
			RolePerAction: "A call-out and a standard part. Replacing the boiler is not an emergency fix.",
		},
		UseCases: []string{
			"A water sensor under the sink fires during your holiday.",
			"The freezer alarm goes off and food needs saving.",
		},
		Samples: []Sample{
			{What: "Emergency plumber, leak under the kitchen sink", Amount: 180},
			{What: "Replacement valve, next-day delivery", Amount: 45},
			{What: "New boiler, recommended by the plumber", Amount: 2400},
			{What: "Dehumidifier rental, 1 week", Amount: 70},
		},
	},
	{
		ID: "car-negotiator", Name: "Car Negotiator", Audience: Personal, Owner: "you",
		Summary: "The agent negotiates with dealers by email for days and holds the best car with a refundable deposit.",
		Sentence: []Part{
			{"Let my agent ", RoleText},
			{"negotiate and place refundable deposits", RoleAction},
			{" with ", RoleText},
			{"dealers for the cars on my shortlist", RoleObject},
			{", ", RoleText},
			{"up to €500 per deposit", RolePerAction},
			{" and ", RoleText},
			{"€1,000 in total", RoleCeiling},
			{". ", RoleText},
			{"Bring anything else back to me", RoleFallback},
			{".", RoleText},
		},
		Skill: "negotiation", Action: "negotiate and hold with deposits", Object: "cars on your shortlist",
		Unit: "€", PerAction: 500, Ceiling: 1000, Window: "in total", Fallback: "comes home to you",
		Why: map[Role]string{
			RoleAction:    "Negotiation is days of back-and-forth email. The agent keeps pressure on every dealer at once; you only see the result.",
			RolePerAction: "Enough to hold a car while you decide. Signing the purchase is never on this Card.",
			RoleCeiling:   "At most two cars held at a time, so the agent cannot tie up your savings in deposits.",
		},
		UseCases: []string{
			"Getting dealers to compete on price without a week of phone calls.",
			"Holding the best offer before it disappears at the end of the month.",
		},
		Samples: []Sample{
			{What: "Refundable deposit to hold a 2023 Model 3 at €34,900 (was €37,500)", Amount: 300},
			{What: "Refundable deposit on a second offer, as leverage", Amount: 400},
			{What: "Non-refundable deposit to lock the €34,900 price", Amount: 2000},
		},
	},
}

// AgentsFor is the roster the owner of a blueprint already runs. Two
// agents share the Card's skill (so it can move), one does not (so the
// match has to choose).
func AgentsFor(b Blueprint) []Agent {
	if b.Audience == Personal {
		return []Agent{
			{ID: "assistant", Name: "Personal assistant", Kind: "general assistant",
				Skills: []string{"travel booking", "shopping", "home services", "negotiation", "email", "calendar"}},
			{ID: "concierge", Name: "Concierge agent", Kind: "errands",
				Skills: []string{"travel booking", "shopping", "home services", "negotiation"}},
			{ID: "research", Name: "Research agent", Kind: "research",
				Skills: []string{"web search", "summaries"}},
		}
	}
	return []Agent{
		{ID: "support", Name: "Support agent", Kind: "customer support",
			Skills: []string{"refund execution", "order lookup", "plan management"}},
		{ID: "backoffice", Name: "Back-office agent", Kind: "operations",
			Skills: []string{"refund execution", "invoice payment", "plan management", "deploy rollback"}},
		{ID: "finance", Name: "Finance agent", Kind: "finance",
			Skills: []string{"invoice payment", "reconciliation"}},
		{ID: "oncall", Name: "On-call agent", Kind: "operations",
			Skills: []string{"deploy rollback", "incident triage"}},
		{ID: "writer", Name: "Content agent", Kind: "marketing",
			Skills: []string{"copywriting", "summaries"}},
	}
}

// Find returns the blueprint with id, or the default.
func Find(id string) Blueprint {
	for _, b := range Catalog {
		if b.ID == id {
			return b
		}
	}
	for _, b := range Catalog {
		if b.ID == Default {
			return b
		}
	}
	return Catalog[0]
}

// ByAudience filters the catalog.
func ByAudience(a Audience) []Blueprint {
	var out []Blueprint
	for _, b := range Catalog {
		if b.Audience == a {
			out = append(out, b)
		}
	}
	return out
}

// Verdict is the match's view of one agent.
type Verdict struct {
	Agent     Agent
	Fits      bool
	Suggested bool
	Why       string
}

// Match proposes which agent should hold the Card. It is deterministic:
// an agent fits when it has the skill the Card needs; among those, the
// one with the fewest other skills is suggested, because a Card is best
// held by the most specialised agent that can do the job.
func Match(b Blueprint, agents []Agent) []Verdict {
	out := make([]Verdict, 0, len(agents))
	best := -1
	for i, a := range agents {
		v := Verdict{Agent: a, Fits: a.Has(b.Skill)}
		if v.Fits {
			v.Why = "Has the " + b.Skill + " skill this Card needs."
			if best < 0 || len(a.Skills) < len(agents[best].Skills) {
				best = i
			}
		} else {
			v.Why = "No " + b.Skill + " skill, so the Card would be useless to it."
		}
		out = append(out, v)
	}
	if best >= 0 {
		out[best].Suggested = true
		out[best].Why = "Has the " + b.Skill + " skill and the fewest other skills: the narrowest agent that can do this job."
	}
	return out
}
