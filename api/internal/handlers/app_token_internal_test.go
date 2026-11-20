package handlers

import (
	"testing"
	"time"
)

// Reading the limit never changes it: 19 failures at T and 1 at T+5 min, then
// at T+11 min only the one is in the window, however often it's asked. Twenty
// fresh failures block on the first ask.
func TestTheFailureLimitIsReadWithoutChangingIt(t *testing.T) {
	var f appFailures
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for range 19 {
		f.add("ip", at)
	}
	f.add("ip", at.Add(5*time.Minute))
	later := at.Add(11 * time.Minute)
	for i := range 30 {
		if f.blocked("ip", later) {
			t.Fatalf("blocked on ask %d with one failure in the window", i+1)
		}
	}
	if n := len(f.recent("ip", later)); n != 1 {
		t.Fatalf("in the window: %d, want 1", n)
	}
	var g appFailures
	for range appFailLimit {
		g.add("ip", at)
	}
	if !g.blocked("ip", at.Add(time.Second)) {
		t.Fatal("twenty fresh failures don't block")
	}
}
