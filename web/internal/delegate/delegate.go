// Package delegate is the Card engine behind /delegate: a Card built
// from a plain-language delegation, handed to an agent, drawn down as the
// agent works, and moved to another agent without losing its balance or
// its statement.
//
// The engine is deterministic and has no storage. A session is an action
// log (Replay) that the page sends back on every click; the server
// replays it from scratch, so nothing the browser sends is trusted as
// state. An invalid step ends the replay: a tampered log can show less,
// never an impossible Card.
package delegate

import (
	"strconv"
	"strings"
)

// Role is the grammatical job a phrase does in a delegation sentence.
// Each role becomes one line on the Card.
type Role string

const (
	RoleText      Role = ""          // connecting words
	RoleAction    Role = "action"    // what the agent may do (the verb)
	RoleObject    Role = "object"    // what it may do it to
	RolePerAction Role = "peraction" // the most any single action may use
	RoleCeiling   Role = "ceiling"   // the total over the window
	RoleFallback  Role = "fallback"  // what happens to anything outside the terms
)

// Roles lists the roles in the order the Card shows them.
var Roles = []Role{RoleAction, RoleObject, RolePerAction, RoleCeiling, RoleFallback}

// RoleLabel is the short grammar-lesson label under a phrase.
func RoleLabel(r Role) string {
	switch r {
	case RoleAction:
		return "action"
	case RoleObject:
		return "on what"
	case RolePerAction:
		return "per action"
	case RoleCeiling:
		return "in total"
	case RoleFallback:
		return "otherwise"
	}
	return ""
}

// RoleExplain says why that part of the sentence matters.
func RoleExplain(r Role) string {
	switch r {
	case RoleAction:
		return "The one kind of job the Card covers. An agent can have many skills; this Card lends authority for this job only."
	case RoleObject:
		return "Which things the job may touch. Anything else is outside the Card, whatever the agent can technically reach."
	case RolePerAction:
		return "The most a single action may use. One large mistake stays small."
	case RoleCeiling:
		return "The total the agent may use over the window. Many small actions still add up to a known amount."
	case RoleFallback:
		return "Inside the Card, the agent works. Outside the Card, the work comes home: to you, to approve once, change the Card, or reject."
	}
	return ""
}

// Part is one phrase of the sentence.
type Part struct {
	Text string
	Role Role
}

// Audience separates Cards people give their own agents from Cards a
// company gives the agents that work for it.
type Audience string

const (
	Personal Audience = "personal"
	Business Audience = "business"
)

// Sample is one piece of work the holder attempts, in order.
type Sample struct {
	What   string // "Refund order"
	Amount int    // in the Card's unit
	Source string // who decided it should happen; "" = the blueprint's
}

// SourceOf is who decided a sample action should happen. It is sample
// data: judgment can come from a person, a rule, a model or a specialised
// decision system, and the Card does not care which.
func (b Blueprint) SourceOf(s Sample) string {
	if s.Source != "" {
		return s.Source
	}
	if b.Source != "" {
		return b.Source
	}
	return "AI model"
}

// Blueprint is a Card design: the sentence, the terms it becomes, why
// each limit is set the way it is, and where it is useful.
type Blueprint struct {
	ID       string
	Name     string
	Audience Audience
	Owner    string // who issues it: "you", "Retailer Co."
	Summary  string

	Sentence []Part

	Skill     string // the skill an agent needs to use this Card
	Action    string // Card line: action
	Object    string // Card line: on what
	Unit      string // "€" for money; otherwise a noun like "rollbacks"
	PerAction int    // 0 = no per-action limit (count Cards)
	Ceiling   int
	Window    string // "this week", "per trip"
	Fallback  string

	Source   string          // default decision source for its samples
	Why      map[Role]string // why this limit, per role
	UseCases []string
	Samples  []Sample
}

// Money reports whether amounts are currency.
func (b Blueprint) Money() bool { return b.Unit == "€" }

// Format renders an amount in the Card's unit: "€1,500" or "3 rollbacks".
func (b Blueprint) Format(v int) string {
	if b.Money() {
		return "€" + thousands(v)
	}
	return thousands(v) + " " + b.Unit
}

// Line is the Card's wording for one role.
func (b Blueprint) Line(r Role) string {
	switch r {
	case RoleAction:
		return b.Action
	case RoleObject:
		return b.Object
	case RolePerAction:
		if b.PerAction == 0 {
			return "one at a time"
		}
		return "≤ " + b.Format(b.PerAction)
	case RoleCeiling:
		return b.Format(b.Ceiling) + " " + b.Window
	case RoleFallback:
		return b.Fallback
	}
	return ""
}

// Agent is an agent the owner already runs, with the skills it has.
type Agent struct {
	ID     string
	Name   string
	Kind   string // "support", "personal assistant"
	Skills []string
}

// Has reports whether the agent has a skill.
func (a Agent) Has(skill string) bool {
	for _, s := range a.Skills {
		if s == skill {
			return true
		}
	}
	return false
}

func thousands(v int) string {
	s := strconv.Itoa(v)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Pct is v as a whole-number percentage of total, clamped to 0..100.
func Pct(v, total int) int {
	if total <= 0 {
		return 0
	}
	p := v * 100 / total
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
