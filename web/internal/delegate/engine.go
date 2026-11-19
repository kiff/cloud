package delegate

import (
	"strconv"
	"strings"
)

// The rule the engine enforces: inside the Card, the agent works;
// outside the Card, the work comes home. An action outside the Card is
// held for the owner, never silently dropped and never allowed by
// stretching the Card. The owner then approves it once, changes the Card,
// or rejects it. Meanwhile the agent keeps working on everything else.

// Steps in the action log. A log is a comma-separated list such as
// "a:support,r,r,auto,ok".
const (
	StepAssign  = "a"      // a:<agent> — the owner gives the Card to an agent
	StepRun     = "r"      // the holder attempts the next action
	StepAuto    = "auto"   // the holder works through everything left on its own
	StepMove    = "m"      // m:<agent> — the owner moves the Card to another agent
	StepRevoke  = "x"      // the owner takes the Card back
	StepOnce    = "ok"     // the owner approves the oldest held action, once
	StepRaise   = "change" // the owner changes the Card to cover the oldest held action
	StepDecline = "no"     // the owner rejects the oldest held action
)

// MaxSteps caps a replay, so a URL or form cannot make the server do
// unbounded work.
const MaxSteps = 40

// Outcome of an agent's action.
type Outcome string

const (
	Allowed  Outcome = "allowed"  // inside the Card: done, drawn from the Card
	Held     Outcome = "held"     // outside the Card: waiting for the owner
	Once     Outcome = "once"     // outside the Card, approved once by the owner
	Rejected Outcome = "rejected" // outside the Card, rejected by the owner
	Resolved Outcome = "resolved" // outside the Card, done after the owner changed it
)

// Entry is one line of the Card's statement.
type Entry struct {
	Seq     int
	Who     string // agent name, or "owner"
	What    string
	Source  string  // who decided the action should happen
	Outcome Outcome // empty for owner events
	Reason  string  // how it fell outside the Card
	Delta   int     // amount drawn from the Card
	Left    int     // remaining after this line
}

// Hold is an action waiting for the owner.
type Hold struct {
	Sample Sample
	Role   Role // the limit it is outside of
	Over   int  // how far outside, in the Card's unit
	Agent  string
	Entry  int // Seq of its statement line
}

// Change is what the last step did, so the page can point at it instead
// of making the reader scan for differences.
type Change struct {
	Kind    string // assigned, drew, held, auto, once, changed, rejected, moved, revoked
	Delta   int
	Role    Role   // the Card line involved, for held
	Changed []Role // the Card lines the owner changed, for changed
	From    string
	To      string
	Summary string
	Rows    int // statement rows this step added
}

// Session is a Card after replaying a log. Card is the session's own
// copy: the owner can change it.
type Session struct {
	Card     Blueprint
	Agents   []Agent
	Steps    []string
	Holder   string // agent ID, "" when unassigned
	Used     int
	Next     int // index of the next sample action
	Pending  []Hold
	Revoked  bool
	Revision int // bumps on every owner change to the Card
	Entries  []Entry
	Last     *Change
}

// Replay rebuilds a session for card from log, stopping at the first
// step that is invalid in the current state.
func Replay(card Blueprint, agents []Agent, log string) Session {
	s := Session{Card: card, Agents: agents, Revision: 1}
	if log == "" {
		return s
	}
	for i, step := range strings.Split(log, ",") {
		if i >= MaxSteps || !s.apply(step) {
			break
		}
		s.Steps = append(s.Steps, step)
	}
	return s
}

// Remaining is what is left on the Card.
func (s Session) Remaining() int { return s.Card.Ceiling - s.Used }

// HolderAgent is the agent holding the Card, if any.
func (s Session) HolderAgent() (Agent, bool) { return s.agent(s.Holder) }

// CanRun reports whether the holder has more work to attempt.
func (s Session) CanRun() bool {
	return s.Holder != "" && !s.Revoked && s.Next < len(s.Card.Samples)
}

// NextSample is the action the holder would attempt next.
func (s Session) NextSample() (Sample, bool) {
	if s.Next >= len(s.Card.Samples) {
		return Sample{}, false
	}
	return s.Card.Samples[s.Next], true
}

// Waiting is the oldest action waiting for the owner.
func (s Session) Waiting() (Hold, bool) {
	if len(s.Pending) == 0 {
		return Hold{}, false
	}
	return s.Pending[0], true
}

// MoveTargets are the other agents that can use this Card.
func (s Session) MoveTargets() []Agent {
	var out []Agent
	for _, a := range s.Agents {
		if a.ID != s.Holder && a.Has(s.Card.Skill) {
			out = append(out, a)
		}
	}
	return out
}

// Log is the session's steps plus one more, ready to post back.
func (s Session) Log(step string) string {
	return strings.Join(append(append([]string{}, s.Steps...), step), ",")
}

// CurrentLog is the replayed log.
func (s Session) CurrentLog() string { return strings.Join(s.Steps, ",") }

// Allows describes what the Card allows on the line an action fell
// outside of: "up to €500 per action".
func (s Session) Allows(r Role) string {
	c := s.Card
	if r == RolePerAction {
		return "up to " + c.Format(c.PerAction) + " per action"
	}
	return c.Format(c.Remaining(s.Used)) + " left " + c.Window
}

// ChangeTo is the Card that Change Card would set to cover the held
// action. It changes every limit the action exceeds as things stand now,
// not only the one it first hit: raising the per-action limit alone
// would let the action overrun the total.
func (s Session) ChangeTo(h Hold) (perAction, ceiling int, changed []Role) {
	c := s.Card
	perAction, ceiling = c.PerAction, c.Ceiling
	amount := h.Sample.Amount
	if c.PerAction > 0 && amount > c.PerAction {
		perAction = roundUp(amount, c.Money())
		changed = append(changed, RolePerAction)
	}
	if s.Used+amount > c.Ceiling {
		ceiling = c.Ceiling + roundUp(s.Used+amount-c.Ceiling, c.Money())
		changed = append(changed, RoleCeiling)
	}
	return perAction, ceiling, changed
}

// Remaining is what is left given used.
func (b Blueprint) Remaining(used int) int { return b.Ceiling - used }

func (s Session) agent(id string) (Agent, bool) {
	for _, a := range s.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

func (s *Session) apply(step string) bool {
	kind, arg, _ := strings.Cut(step, ":")
	start := len(s.Entries)
	if kind != StepAssign && kind != StepMove && arg != "" {
		return false
	}
	switch kind {
	case StepAssign:
		a, ok := s.agent(arg)
		if !ok || s.Holder != "" || s.Revoked || !a.Has(s.Card.Skill) {
			return false
		}
		s.Holder = a.ID
		s.Revision++
		s.record(Entry{Who: "owner", What: "Card given to " + a.Name})
		s.Last = &Change{Kind: "assigned", To: a.Name,
			Summary: a.Name + " now holds the Card. Inside it, the agent works on its own."}
	case StepRun:
		if !s.CanRun() {
			return false
		}
		s.run()
	case StepAuto:
		if !s.CanRun() {
			return false
		}
		used := s.Used
		for s.CanRun() {
			s.run()
		}
		var done, held int
		for _, e := range s.Entries[start:] {
			if e.Outcome == Held {
				held++
			} else {
				done++
			}
		}
		sum := "The agent handled " + itoa(done) + " actions on its own (" + s.Card.Format(s.Used-used) + " used), with nobody approving them"
		if held > 0 {
			sum += ". " + itoa(held) + " fell outside the Card and came home to you"
		}
		s.Last = &Change{Kind: "auto", Delta: s.Used - used, Summary: sum + "."}
	case StepOnce, StepRaise, StepDecline:
		if len(s.Pending) == 0 || s.Revoked {
			return false
		}
		s.resolve(kind)
	case StepMove:
		to, ok := s.agent(arg)
		if !ok || s.Holder == "" || s.Revoked || to.ID == s.Holder || !to.Has(s.Card.Skill) {
			return false
		}
		from, _ := s.HolderAgent()
		s.Holder = to.ID
		s.Revision++
		s.record(Entry{Who: "owner", What: "Card moved from " + from.Name + " to " + to.Name})
		s.Last = &Change{Kind: "moved", From: from.Name, To: to.Name,
			Summary: "Same Card, new holder: " + to.Name + " starts with " + s.Card.Format(s.Remaining()) + " left and the full statement."}
	case StepRevoke:
		if s.Revoked {
			return false
		}
		s.Revoked = true
		s.Pending = nil
		s.Revision++
		s.record(Entry{Who: "owner", What: "Card revoked"})
		s.Last = &Change{Kind: "revoked",
			Summary: "The Card is revoked. Nothing more can be done under it; its statement stays."}
	default:
		return false
	}
	s.Last.Rows = len(s.Entries) - start
	return true
}

// run has the holder attempt the next action.
func (s *Session) run() {
	sample := s.Card.Samples[s.Next]
	s.Next++
	holder, _ := s.HolderAgent()
	if role, over := s.outside(sample.Amount); role != "" {
		reason := s.Card.Format(over) + " outside your delegation: the Card allows " + s.Allows(role)
		s.record(Entry{Who: holder.Name, What: sample.What, Source: s.Card.SourceOf(sample), Outcome: Held, Reason: reason})
		s.Pending = append(s.Pending, Hold{Sample: sample, Role: role, Over: over, Agent: holder.Name, Entry: len(s.Entries)})
		s.Last = &Change{Kind: "held", Role: role,
			Summary: "This is outside what you delegated, so it came home to you. The agent carries on with everything else."}
		return
	}
	s.Used += sample.Amount
	s.record(Entry{Who: holder.Name, What: sample.What, Source: s.Card.SourceOf(sample), Outcome: Allowed, Delta: sample.Amount})
	s.Last = &Change{Kind: "drew", Delta: sample.Amount,
		Summary: holder.Name + " did it on its own and used " + s.Card.Format(sample.Amount) + ". " + s.Card.Format(s.Remaining()) + " left " + s.Card.Window + "."}
}

// resolve applies the owner's answer to the oldest held action.
func (s *Session) resolve(kind string) {
	h := s.Pending[0]
	s.Pending = s.Pending[1:]
	idx := h.Entry - 1 // an index, not a pointer: record() may reallocate Entries
	switch kind {
	case StepOnce:
		// It runs on the owner's authority, not the Card's: recorded, not
		// drawn, and the Card is unchanged.
		s.Entries[idx].Outcome = Once
		s.record(Entry{Who: "owner", What: "Approved once: " + h.Sample.What})
		s.Last = &Change{Kind: "once",
			Summary: "Done, this once. The Card is unchanged: the next one like it will come home to you again."}
	case StepDecline:
		s.Entries[idx].Outcome = Rejected
		s.record(Entry{Who: "owner", What: "Rejected: " + h.Sample.What})
		s.Last = &Change{Kind: "rejected", Summary: "Rejected. Nothing happened, and the Card is unchanged."}
	case StepRaise:
		per, ceil, changed := s.ChangeTo(h)
		var lines []string
		if per != s.Card.PerAction {
			lines = append(lines, "per action "+s.Card.Format(s.Card.PerAction)+" → "+s.Card.Format(per))
		}
		if ceil != s.Card.Ceiling {
			lines = append(lines, "in total "+s.Card.Format(s.Card.Ceiling)+" → "+s.Card.Format(ceil))
		}
		s.Card.PerAction, s.Card.Ceiling = per, ceil
		s.Revision++
		s.record(Entry{Who: "owner", What: "Changed the Card: " + strings.Join(lines, ", ")})
		// Re-check the action against the whole updated Card before it
		// draws anything. ChangeTo covers both limits, so this holds; if it
		// ever did not, the action stays with the owner rather than
		// overrunning the Card.
		if role, over := s.outside(h.Sample.Amount); role != "" {
			h.Role, h.Over = role, over
			s.Pending = append([]Hold{h}, s.Pending...)
			s.Last = &Change{Kind: "held", Role: role, Changed: changed,
				Summary: "The Card changed, but this action is still outside it, so it is still waiting for you."}
			return
		}
		s.Entries[idx].Outcome = Resolved
		s.Used += h.Sample.Amount
		s.record(Entry{Who: h.Agent, What: h.Sample.What, Source: s.Card.SourceOf(h.Sample), Outcome: Allowed, Delta: h.Sample.Amount})
		var allows []string
		for _, r := range changed {
			allows = append(allows, s.Allows(r))
		}
		s.Last = &Change{Kind: "changed", Changed: changed,
			Summary: "The Card now allows " + strings.Join(allows, " and ") + ", for this and everything after it. The held action went through under the new terms."}
	}
}

// outside reports which limit an action of amount falls outside of, and
// by how much. The per-action limit is checked first: it is the one that
// would still apply on a fresh Card.
func (s Session) outside(amount int) (Role, int) {
	c := s.Card
	if c.PerAction > 0 && amount > c.PerAction {
		return RolePerAction, amount - c.PerAction
	}
	if s.Used+amount > c.Ceiling {
		return RoleCeiling, s.Used + amount - c.Ceiling
	}
	return "", 0
}

func (s *Session) record(e Entry) {
	e.Seq = len(s.Entries) + 1
	e.Left = s.Remaining()
	s.Entries = append(s.Entries, e)
}

// Statement is the entries newest first.
func (s Session) Statement() []Entry {
	out := make([]Entry, len(s.Entries))
	for i, e := range s.Entries {
		out[len(s.Entries)-1-i] = e
	}
	return out
}

// roundUp rounds money up to a round number a person would choose; counts
// are left alone.
func roundUp(v int, money bool) int {
	if !money {
		return v
	}
	step := 100
	if v >= 5000 {
		step = 1000
	}
	return (v + step - 1) / step * step
}

func itoa(n int) string { return strconv.Itoa(n) }
