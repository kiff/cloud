package posture_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/posture"
)

func tm(min int) time.Time {
	return time.Date(2026, time.May, 24, 12, min, 0, 0, time.UTC)
}

// ModeAt returns the posture in force at a given time: the `to` of the
// latest transition at or before t, or the default before the first
// recorded transition.
func TestModeAt(t *testing.T) {
	t.Parallel()
	// observe at :10, enforce at :20, back to observe at :30.
	trs := []posture.Transition{
		{From: "enforce", To: "observe", ChangedAt: tm(10)},
		{From: "observe", To: "enforce", ChangedAt: tm(20)},
		{From: "enforce", To: "observe", ChangedAt: tm(30)},
	}
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"before first transition uses default", tm(5), "enforce"},
		{"exactly at first transition", tm(10), "observe"},
		{"between first and second", tm(15), "observe"},
		{"at second", tm(20), "enforce"},
		{"between second and third", tm(25), "enforce"},
		{"at third", tm(30), "observe"},
		{"after last", tm(40), "observe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := posture.ModeAt(trs, c.at, "enforce"); got != c.want {
				t.Errorf("ModeAt(%v) = %q, want %q", c.at, got, c.want)
			}
		})
	}
}

func TestModeAt_EmptyReturnsDefault(t *testing.T) {
	t.Parallel()
	if got := posture.ModeAt(nil, tm(10), "observe"); got != "observe" {
		t.Errorf("ModeAt(nil) = %q, want observe (default)", got)
	}
}

// LastEnforce returns the most recent transition into enforce; ok=false
// when the tenant never flipped into enforce.
func TestHistory_LastEnforce(t *testing.T) {
	t.Parallel()
	h := posture.History{Transitions: []posture.Transition{
		{From: "enforce", To: "observe", ChangedBy: "op1", ChangedAt: tm(10)},
		{From: "observe", To: "enforce", ChangedBy: "op2", ChangedAt: tm(20)},
		{From: "enforce", To: "observe", ChangedBy: "op3", ChangedAt: tm(30)},
	}}
	be, ok := h.LastEnforce()
	if !ok {
		t.Fatal("LastEnforce ok = false, want true")
	}
	if be.ChangedAt != tm(20) || be.ChangedBy != "op2" {
		t.Errorf("LastEnforce = %+v, want the :20/op2 transition", be)
	}

	last, ok := h.LastChange()
	if !ok || last.ChangedAt != tm(30) {
		t.Errorf("LastChange = %+v (ok=%v), want :30", last, ok)
	}
}

func TestHistory_LastEnforce_NeverEnforced(t *testing.T) {
	t.Parallel()
	h := posture.History{Transitions: []posture.Transition{
		{From: "enforce", To: "observe", ChangedAt: tm(10)},
	}}
	if _, ok := h.LastEnforce(); ok {
		t.Error("LastEnforce ok = true, want false (never flipped to enforce)")
	}
}

func TestHistory_Empty(t *testing.T) {
	t.Parallel()
	var h posture.History
	if _, ok := h.LastChange(); ok {
		t.Error("LastChange ok = true on empty history")
	}
	if _, ok := h.LastEnforce(); ok {
		t.Error("LastEnforce ok = true on empty history")
	}
}

// MemoryReader returns a tenant's transitions oldest-first and scopes
// by tenant.
func TestMemoryReader_TransitionsScopedAndOrdered(t *testing.T) {
	t.Parallel()
	r := posture.NewMemoryReader()
	// Seed out of order to confirm the reader sorts.
	r.Append("t1", posture.Transition{From: "observe", To: "enforce", ChangedAt: tm(20)})
	r.Append("t1", posture.Transition{From: "enforce", To: "observe", ChangedAt: tm(10)})
	r.Append("t2", posture.Transition{From: "enforce", To: "observe", ChangedAt: tm(15)})

	got, err := r.Transitions(context.Background(), "t1")
	if err != nil {
		t.Fatalf("Transitions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d transitions for t1, want 2", len(got))
	}
	if !got[0].ChangedAt.Equal(tm(10)) || !got[1].ChangedAt.Equal(tm(20)) {
		t.Errorf("not oldest-first: %v, %v", got[0].ChangedAt, got[1].ChangedAt)
	}

	// Unknown tenant -> empty, no error.
	none, err := r.Transitions(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Transitions unknown: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("unknown tenant returned %d", len(none))
	}
}
