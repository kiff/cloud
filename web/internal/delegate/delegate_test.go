package delegate

import (
	"strings"
	"testing"
)

func refunds(t *testing.T, log string) Session {
	t.Helper()
	b := Find("eu-refunds")
	return Replay(b, AgentsFor(b), log)
}

// Inside the Card the agent works; the €1,500 refund is outside it and
// comes home, and the agent carries on with the €400 one.
func TestReplay_InsideWorksOutsideComesHome(t *testing.T) {
	t.Parallel()
	s := refunds(t, "a:support,r,r,r,r")
	if s.Used != 950 || s.Remaining() != 29050 {
		t.Fatalf("used %d remaining %d", s.Used, s.Remaining())
	}
	h, ok := s.Waiting()
	if !ok || h.Sample.Amount != 1500 || h.Role != RolePerAction || h.Over != 1000 {
		t.Fatalf("waiting = %+v %v", h, ok)
	}
	held := s.Entries[h.Entry-1]
	if held.Outcome != Held || !strings.Contains(held.Reason, "€1,000 outside your delegation: the Card allows up to €500 per action") {
		t.Fatalf("held entry = %+v", held)
	}
	if held.Source != "AI model" {
		t.Errorf("decision source = %q", held.Source)
	}
}

// Approve once: this action only. Nothing drawn, the Card unchanged, the
// held line resolved.
func TestResolve_ApproveOnce(t *testing.T) {
	t.Parallel()
	before := refunds(t, "a:support,r,r,r")
	s := refunds(t, "a:support,r,r,r,ok")
	if len(s.Pending) != 0 || s.Used != before.Used || s.Card.PerAction != 500 || s.Revision != before.Revision {
		t.Fatalf("once changed the Card: used %d per-action %d rev %d", s.Used, s.Card.PerAction, s.Revision)
	}
	if s.Entries[before.Pending[0].Entry-1].Outcome != Once || s.Last.Kind != "once" {
		t.Fatalf("held line not resolved as once: %+v", s.Entries)
	}
}

// Change Card: the delegation changes for the future, the revision goes
// up, and the held action goes through under the new terms.
func TestResolve_ChangeCard(t *testing.T) {
	t.Parallel()
	s := refunds(t, "a:support,r,r,r,change")
	if s.Card.PerAction != 1500 || s.Revision != 3 || s.Used != 2050 || len(s.Pending) != 0 {
		t.Fatalf("per-action %d rev %d used %d pending %d", s.Card.PerAction, s.Revision, s.Used, len(s.Pending))
	}
	if s.Last.Kind != "changed" || len(s.Last.Changed) != 1 || s.Last.Changed[0] != RolePerAction ||
		!strings.Contains(s.Last.Summary, "up to €1,500 per action") {
		t.Fatalf("last = %+v", s.Last)
	}
	// The original catalog Card is not modified.
	if Find("eu-refunds").PerAction != 500 {
		t.Fatal("changing a session Card mutated the catalog")
	}
}

func TestResolve_Reject(t *testing.T) {
	t.Parallel()
	s := refunds(t, "a:support,r,r,r,no")
	if s.Used != 550 || s.Card.PerAction != 500 || len(s.Pending) != 0 || s.Last.Kind != "rejected" {
		t.Fatalf("used %d per-action %d pending %d last %+v", s.Used, s.Card.PerAction, len(s.Pending), s.Last)
	}
}

// Letting the agent run: everything within the Card goes through with
// nobody approving it; what is outside comes home.
func TestReplay_AutoRun(t *testing.T) {
	t.Parallel()
	s := refunds(t, "a:support,auto")
	if s.Used != 950 || s.CanRun() || s.Last.Kind != "auto" || s.Last.Rows != 4 || len(s.Pending) != 1 {
		t.Fatalf("used %d canRun %v last %+v pending %d", s.Used, s.CanRun(), s.Last, len(s.Pending))
	}
	if !strings.Contains(s.Last.Summary, "handled 3 actions on its own (€950 used)") ||
		!strings.Contains(s.Last.Summary, "1 fell outside the Card and came home to you") {
		t.Fatalf("summary = %q", s.Last.Summary)
	}
}

// Moving the Card keeps the balance and the statement.
func TestReplay_MoveKeepsBalanceAndStatement(t *testing.T) {
	t.Parallel()
	before := refunds(t, "a:support,r,r")
	after := refunds(t, "a:support,r,r,m:backoffice")
	if after.Remaining() != before.Remaining() || len(after.Entries) != len(before.Entries)+1 {
		t.Fatalf("move reset the Card: %d → %d, %d → %d rows",
			before.Remaining(), after.Remaining(), len(before.Entries), len(after.Entries))
	}
	if after.Last.Kind != "moved" || after.Last.To != "Back-office agent" {
		t.Fatalf("last = %+v", after.Last)
	}
}

func TestReplay_CeilingHold(t *testing.T) {
	t.Parallel()
	b := Find("production-rollback")
	s := Replay(b, AgentsFor(b), "a:oncall,auto")
	h, ok := s.Waiting()
	if s.Used != 3 || !ok || h.Role != RoleCeiling || h.Over != 1 {
		t.Fatalf("used %d waiting %+v", s.Used, h)
	}
	if s.Entries[len(s.Entries)-1].Source != "Rule engine" {
		t.Errorf("rollback decisions come from a rule engine")
	}
	s = Replay(b, AgentsFor(b), "a:oncall,auto,change")
	if s.Card.Ceiling != 4 || s.Used != 4 {
		t.Fatalf("change on a count Card: ceiling %d used %d", s.Card.Ceiling, s.Used)
	}
}

// Invalid steps end the replay: a tampered log shows less, never an
// impossible Card.
func TestReplay_InvalidStepsStop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		log   string
		steps int
	}{
		{"r", 0},                      // run before any holder
		{"a:writer", 0},               // agent without the skill
		{"a:support,a:backoffice", 1}, // assign twice
		{"a:support,m:support", 1},    // move to the current holder
		{"a:support,ok", 1},           // approve with nothing waiting
		{"a:support,r,change", 2},     // change with nothing waiting
		{"a:support,r,r,r,ok,ok", 5},  // approve twice
		{"a:support,x,r", 2},          // run after revoke
		{"a:support,r,r,r,x,ok", 5},   // revoking clears what was waiting
		{"a:support,r,r,r,r,r", 5},    // no fifth sample
		{"a:support,r:9999", 1},       // arguments on run are rejected
		{"a:support,ok:1", 1},         // arguments on answers are rejected
		{"a:support,<script>", 1},     // junk
	} {
		if got := len(refunds(t, tc.log).Steps); got != tc.steps {
			t.Errorf("Replay(%q) kept %d steps, want %d", tc.log, got, tc.steps)
		}
	}
	long := "a:support" + strings.Repeat(",m:backoffice,m:support", 40)
	if n := len(refunds(t, long).Steps); n != MaxSteps {
		t.Errorf("steps = %d, want cap %d", n, MaxSteps)
	}
}

func TestMatch_SuggestsNarrowestFit(t *testing.T) {
	t.Parallel()
	b := Find("eu-refunds")
	var suggested []string
	for _, v := range Match(b, AgentsFor(b)) {
		if v.Suggested {
			suggested = append(suggested, v.Agent.ID)
		}
		if v.Agent.ID == "writer" && v.Fits {
			t.Error("content agent cannot hold a refunds Card")
		}
	}
	if len(suggested) != 1 || suggested[0] != "support" {
		t.Fatalf("suggested = %v, want [support]", suggested)
	}
}

// Every gallery Card must teach the same lesson: a sentence with each
// role once, an agent to suggest and one to move to, work done inside
// the Card, and (with more than two samples) one action that comes home.
func TestCatalog_EveryCardIsComplete(t *testing.T) {
	t.Parallel()
	ids := map[string]bool{}
	for _, b := range Catalog {
		if ids[b.ID] {
			t.Errorf("duplicate id %s", b.ID)
		}
		ids[b.ID] = true
		seen := map[Role]int{}
		for _, p := range b.Sentence {
			seen[p.Role]++
		}
		for _, r := range Roles {
			if seen[r] != 1 {
				t.Errorf("%s: role %s appears %d times in the sentence", b.ID, r, seen[r])
			}
		}
		agents := AgentsFor(b)
		var fits int
		suggested := ""
		for _, v := range Match(b, agents) {
			if v.Fits {
				fits++
			}
			if v.Suggested {
				suggested = v.Agent.ID
			}
		}
		if fits < 2 || suggested == "" {
			t.Errorf("%s: %d agents fit (need 2: one to hold, one to move to)", b.ID, fits)
		}
		s := Replay(b, agents, "a:"+suggested+",auto")
		var done int
		for _, e := range s.Entries {
			if e.Outcome == Allowed {
				done++
			}
		}
		if done == 0 || (len(b.Samples) > 2 && len(s.Pending) != 1) {
			t.Errorf("%s: %d done inside the Card, %d came home; want work and one exception", b.ID, done, len(s.Pending))
		}
		if b.Audience != Business && b.Audience != Personal {
			t.Errorf("%s: unknown audience %q", b.ID, b.Audience)
		}
		if len(b.UseCases) == 0 || b.Summary == "" || len(b.Why) == 0 {
			t.Errorf("%s: missing summary, why or use cases", b.ID)
		}
	}
	if len(ByAudience(Business)) == 0 || len(ByAudience(Personal)) == 0 {
		t.Error("gallery needs both business and personal Cards")
	}
}

func TestFormatAndRound(t *testing.T) {
	t.Parallel()
	if got := Find("eu-refunds").Format(30000); got != "€30,000" {
		t.Errorf("Format = %q", got)
	}
	if got := Find("production-rollback").Line(RoleCeiling); got != "3 rollbacks today" {
		t.Errorf("Line = %q", got)
	}
	for _, tc := range []struct{ in, want int }{{1500, 1500}, {1501, 1600}, {9500, 10000}, {260, 300}} {
		if got := roundUp(tc.in, true); got != tc.want {
			t.Errorf("roundUp(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// a review: Change Card must keep both limits. Home While Away's
// €2,400 boiler is over the €250 per-action limit and, after €295 of
// work, over the €800 month too; raising only the per-action limit
// drove the Card to −€1,895.
func TestResolve_ChangeCardCoversBothLimits(t *testing.T) {
	t.Parallel()
	b := Find("home-away")
	s := Replay(b, AgentsFor(b), "a:concierge,auto,change")
	if s.Remaining() < 0 {
		t.Fatalf("Card went negative: %d", s.Remaining())
	}
	if s.Card.PerAction != 2400 || s.Card.Ceiling != 2700 || s.Used != 2695 {
		t.Fatalf("per-action %d ceiling %d used %d", s.Card.PerAction, s.Card.Ceiling, s.Used)
	}
	if len(s.Last.Changed) != 2 || !strings.Contains(s.Last.Summary, "up to €2,400 per action and €5 left this month") {
		t.Fatalf("last = %+v", s.Last)
	}
	if s.Entries[3].What != "New boiler, recommended by the plumber" || s.Entries[3].Outcome != Resolved {
		t.Fatalf("held line not marked resolved: %+v", s.Entries)
	}
}

// No Card in the gallery can go below zero, whatever the owner answers.
func TestCatalog_NeverNegative(t *testing.T) {
	t.Parallel()
	for _, b := range Catalog {
		agents := AgentsFor(b)
		var holder string
		for _, v := range Match(b, agents) {
			if v.Suggested {
				holder = v.Agent.ID
			}
		}
		for _, answer := range []string{"ok", "change", "no"} {
			log := "a:" + holder + ",auto"
			for i := 0; i < 3; i++ {
				log += "," + answer
			}
			s := Replay(b, agents, log)
			if s.Remaining() < 0 {
				t.Errorf("%s with %q: remaining %d", b.ID, answer, s.Remaining())
			}
			for _, e := range s.Entries {
				if e.Left < 0 {
					t.Errorf("%s with %q: statement line %d left %d", b.ID, answer, e.Seq, e.Left)
				}
			}
		}
	}
}
