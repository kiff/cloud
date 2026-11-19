package proofs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// at makes a counted run of app on entry ending at t0+min minutes.
func at(app, entry string, min int, outcome string) Run {
	r := run(app, min, outcome)
	r.Entry = entry
	r.RunID = entry + "-" + time.Duration(min).String()
	return r
}

func TestANewEntryRevisionStartsUnknown(t *testing.T) {
	runs := []Run{at("stripe", "stripe@1", 1, OutcomePass), at("stripe", "stripe@1", 2, OutcomePass),
		at("stripe", "stripe@2", 3, OutcomePass)}
	s := StateOfApp("stripe", runs, nil, t0)
	if s.State != StateUnknown || s.Entry != "stripe@2" || s.Held {
		t.Fatalf("a new revision inherited the app's green: %+v", s)
	}
	runs = append(runs, at("stripe", "stripe@2", 4, OutcomePass))
	if s := StateOfApp("stripe", runs, nil, t0); s.State != StateGreen || s.Entry != "stripe@2" {
		t.Fatalf("two passes of the new revision: %+v", s)
	}
}

func TestARedRevisionKeepsTheHoldUntilTheNextIsGreen(t *testing.T) {
	red := []Run{at("gmail", "gmail@1", 1, OutcomeFail), at("gmail", "gmail@1", 2, OutcomeFail), at("gmail", "gmail@1", 3, OutcomeFail)}
	runs := append(red, at("gmail", "gmail@2", 4, OutcomePass))
	s := StateOfApp("gmail", runs, nil, t0)
	if s.State != StateUnknown || !s.Held || s.Reason != ReasonPreviousEntryRed {
		t.Fatalf("a catalog change alone released the hold: %+v", s)
	}
	runs = append(runs, at("gmail", "gmail@2", 5, OutcomePass))
	if s := StateOfApp("gmail", runs, nil, t0); s.State != StateGreen || s.Held {
		t.Fatalf("the new revision went green, still held: %+v", s)
	}
	// A green previous revision holds nothing.
	green := []Run{at("x", "x@1", 1, OutcomePass), at("x", "x@1", 2, OutcomePass), at("x", "x@2", 3, OutcomeFail)}
	if s := StateOfApp("x", green, nil, t0); s.Held {
		t.Fatalf("held after a green revision: %+v", s)
	}
}

func TestAnUncountedRunCantChangeTheEntry(t *testing.T) {
	red := []Run{at("gmail", "gmail@1", 1, OutcomeFail), at("gmail", "gmail@1", 2, OutcomeFail), at("gmail", "gmail@1", 3, OutcomeFail)}
	sneaky := at("gmail", "gmail@99", 4, OutcomePass)
	sneaky.Counted = false
	s := StateOfApp("gmail", append(red, sneaky), nil, t0)
	if s.Entry != "gmail@1" || s.State != StateRed || !s.Held {
		t.Fatalf("an uncounted run moved the app to another entry: %+v", s)
	}
}

func TestPlanquesAnswerForAnEntry(t *testing.T) {
	now := t0.Add(time.Hour)
	green := []Run{at("stripe", "stripe@7", 1, OutcomePass), at("stripe", "stripe@7", 2, OutcomePass)}
	until := now.Add(time.Hour)
	cases := []struct {
		name      string
		runs      []Run
		overrides []Override
		entry     string
		now       time.Time
		state     string
		reason    string
	}{
		{"green and fresh", green, nil, "stripe@7", now, StateGreen, ReasonTwoPassed},
		{"no runs", nil, nil, "stripe@7", now, StateUnknown, ReasonNoRuns},
		{"another revision", green, nil, "stripe@8", now, StateUnknown, ReasonNoRuns},
		{"green but its last run is a day old", green, nil, "stripe@7", t0.Add(25 * time.Hour), StateUnknown, ReasonOldRun},
		{"one pass", green[:1], nil, "stripe@7", now, StateUnknown, ReasonNotGreen},
		{"red", []Run{at("s", "s@1", 1, OutcomeFail), at("s", "s@1", 2, OutcomeFail), at("s", "s@1", 3, OutcomeFail)}, nil, "s@1", now, StateUnknown, ReasonThreeFailed},
		{"an operator hold", green, []Override{{App: "stripe", Action: OverrideHold, CreatedAt: now.Add(-time.Minute), Until: &until}}, "stripe@7", now, StateUnknown, ReasonOperatorHold},
		{"a release makes nothing green", green[:1], []Override{{App: "stripe", Action: OverrideRelease, CreatedAt: now.Add(-time.Minute), Until: &until}}, "stripe@7", now, StateUnknown, ReasonNotGreen},
		{"error runs only", []Run{at("stripe", "stripe@7", 1, OutcomeError)}, nil, "stripe@7", now, StateUnknown, ReasonNoRuns},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := EntryState(c.runs, c.overrides, c.entry, c.now)
			if a.State != c.state || a.Reason != c.reason || a.Entry != c.entry {
				t.Fatalf("EntryState = %+v, want %s/%s", a, c.state, c.reason)
			}
			if a.State == StateGreen && (a.App != "stripe" || a.Since.IsZero()) {
				t.Fatalf("green without its app or since: %+v", a)
			}
		})
	}
	// Uncounted passes never make an entry green.
	unc := []Run{at("stripe", "stripe@9", 1, OutcomePass), at("stripe", "stripe@9", 2, OutcomePass)}
	for i := range unc {
		unc[i].Counted = false
	}
	if a := EntryState(unc, nil, "stripe@9", now); a.State != StateUnknown {
		t.Fatalf("uncounted passes made it green: %+v", a)
	}
}

func TestTheReaderSaysUnknownForAnEntryPast24Hours(t *testing.T) {
	ctx := context.Background()
	store := &flakyStore{MemoryStore: NewMemoryStore()}
	_ = store.Append(ctx, at("stripe", "stripe@7", 1, OutcomePass))
	_ = store.Append(ctx, at("stripe", "stripe@7", 2, OutcomePass))
	now := t0.Add(time.Hour)
	reader := NewReader(store, nil).WithClock(func() time.Time { return now })
	if a := reader.EntryFor(ctx, "stripe@7"); a.State != StateGreen {
		t.Fatalf("fresh: %+v", a)
	}
	store.fail = true
	now = now.Add(25 * time.Hour)
	if a := reader.EntryFor(ctx, "stripe@7"); a.State != StateUnknown || a.Reason != ReasonStale {
		t.Fatalf("unreadable past 24 h: %+v", a)
	}
}

func TestTestKeys(t *testing.T) {
	der := []byte("a test phone's DER public key")
	sum := sha256.Sum256(der)
	full := hex.EncodeToString(sum[:])
	short := full[:4] + " " + full[4:8] + " " + full[8:12] + " " + full[12:16]

	k, bad := ParseTestKeys([]string{strings.ToUpper(full), "", "not-hex", "abcd"})
	if !k.Has(der) || len(bad) != 2 {
		t.Fatalf("full fingerprint: has=%v bad=%v", k.Has(der), bad)
	}
	k, _ = ParseTestKeys([]string{short})
	if !k.Has(der) {
		t.Fatal("short fingerprint not matched")
	}
	if k.Has([]byte("another key")) || k.Has(nil) {
		t.Fatal("matched another key")
	}
	if (TestKeys{}).Has(der) {
		t.Fatal("an empty list matched")
	}
}

// A green app's latest counted pass stands for PassLapse (48 h): one 47 h
// old is still green, one 49 h old reads unknown with ReasonExpired, not
// held, and planque's answer for the entry reads it as not proved too, even
// while newer runs (fails that don't yet make it red) keep coming.
func TestAGreenAppsLatestPassLapsesAfter48Hours(t *testing.T) {
	passes := []Run{at("typeform", "typeform@1", 1, OutcomePass), at("typeform", "typeform@1", 2, OutcomePass)}
	lastPassAt := passes[1].EndedAt
	if s := StateOfApp("typeform", passes, nil, lastPassAt.Add(47*time.Hour)); s.State != StateGreen || s.Reason != ReasonTwoPassed {
		t.Fatalf("a pass 47 h old: %+v", s)
	}
	s := StateOfApp("typeform", passes, nil, lastPassAt.Add(49*time.Hour))
	if s.State != StateUnknown || s.Reason != ReasonExpired || s.Held || !s.Since.Equal(lastPassAt.Add(PassLapse)) {
		t.Fatalf("a pass 49 h old: %+v", s)
	}
	if rows := Rows(passes, nil, lastPassAt.Add(49*time.Hour)); len(rows) != 1 || rows[0].State.State == StateGreen {
		t.Fatalf("the console still shows it proved: %+v", rows)
	}
	// Two fails since, the latest 2 h ago: still green by its runs, and its latest run is fresh, but the pass is old.
	fails := append(passes, at("typeform", "typeform@1", 40*60, OutcomeFail), at("typeform", "typeform@1", 47*60, OutcomeFail))
	now := lastPassAt.Add(49 * time.Hour)
	if s := StateOfApp("typeform", fails, nil, now); s.State != StateUnknown || s.Reason != ReasonExpired {
		t.Fatalf("fails kept an old pass proved: %+v", s)
	}
	if a := EntryState(fails, nil, "typeform@1", now); a.State != StateUnknown || a.Reason != ReasonExpired {
		t.Fatalf("planque's answer at 49 h: %+v", a)
	}
	if a := EntryState(fails, nil, "typeform@1", lastPassAt.Add(47*time.Hour)); a.State != StateGreen {
		t.Fatalf("planque's answer at 47 h: %+v", a)
	}
}
