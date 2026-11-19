package pages

import (
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestCardConnection(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)
	keys := []apiclient.KeyListItem{
		{ID: "k-bound", Label: "agent key", AgentID: "refund-agent"},
		{ID: "k-other", Label: "other", AgentID: "other-agent"},
	}

	// Only test requests from the card page (no principal key): not
	// connected, but they still count toward today.
	tests := []apiclient.ProposalDecision{
		{Outcome: "allowed", CreatedAt: now.Add(-time.Hour)},
		{Outcome: "blocked", CreatedAt: now.Add(-26 * time.Hour)},
	}
	c := NewCardConnection("refund-agent", tests, false, keys, false, nil, false, now)
	if c.Connected {
		t.Error("test requests from the card page must not read as a connection")
	}
	if c.Today != 1 || c.TodayLabel() != "1 decision today" {
		t.Errorf("today = %d (%q), want 1", c.Today, c.TodayLabel())
	}
	if len(c.Keys) != 1 || c.Keys[0].ID != "k-bound" {
		t.Errorf("keys bound to the holder = %+v, want only k-bound", c.Keys)
	}

	// The newest request from an agent key is the last request.
	rows := append([]apiclient.ProposalDecision{
		{Outcome: "allowed", CreatedAt: now.Add(-10 * time.Minute), PrincipalAPIKeyID: "k-bound", ActorVerified: true},
	}, tests...)
	c = NewCardConnection("refund-agent", rows, false, keys, false, nil, false, now)
	if !c.Connected || c.LastKeyName() != "agent key" || !c.LastAt.Equal(now.Add(-10*time.Minute)) {
		t.Errorf("connection = %+v", c)
	}

	// A key no longer listed is named by its id.
	c = NewCardConnection("refund-agent", rows, false, nil, true, nil, false, now)
	if c.LastKeyName() != "k-bound" || !c.KeysError {
		t.Errorf("unlisted key: name %q, keysError %v", c.LastKeyName(), c.KeysError)
	}

	// An unbound key naming the holder is a claim, not a connection.
	claimed := []apiclient.ProposalDecision{{Outcome: "allowed", CreatedAt: now.Add(-time.Minute), PrincipalAPIKeyID: "k-unbound"}}
	if NewCardConnection("refund-agent", claimed, false, keys, false, nil, false, now).Connected {
		t.Error("an unverified key request counted as the agent's connection")
	}

	// A full page read: the count is a floor and absence is not a fact.
	c = NewCardConnection("refund-agent", tests, true, keys, false, nil, false, now)
	if !c.Truncated || c.TodayLabel() != "at least 1 decision today" {
		t.Errorf("truncated: %+v, label %q", c, c.TodayLabel())
	}
}

func TestCardConnection_GuardRuntimes(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)
	runtimes := []apiclient.GuardConnection{
		{AgentID: "refund-agent", Adapter: "agno", Mode: "observe", LastSeenAt: now.Add(-2 * time.Minute)},
		{AgentID: "refund-agent", Adapter: "agno", Mode: "observe", LastSeenAt: now.Add(-3 * time.Hour)},
		{AgentID: "someone-else", Adapter: "langgraph", Mode: "enforce", LastSeenAt: now},
	}
	c := NewCardConnection("refund-agent", nil, false, nil, false, runtimes, false, now)
	if len(c.Runtimes) != 2 || !c.Runtimes[0].LastSeenAt.Equal(now.Add(-2*time.Minute)) {
		t.Fatalf("holder's runtimes, newest first: %+v", c.Runtimes)
	}
	if !c.Live(now) || c.Connected {
		t.Error("a live observe runtime is running, not connected to the card")
	}
	if !c.ObserveOnly() {
		t.Error("all-observe runtimes must be flagged: the card is never consulted")
	}
	runtimes[0].Mode = "enforce"
	if NewCardConnection("refund-agent", nil, false, nil, false, runtimes, false, now).ObserveOnly() {
		t.Error("an enforcing runtime is not observe-only")
	}
	if NewCardConnection("refund-agent", nil, false, nil, false, runtimes, false, now.Add(2*time.Hour)).Live(now.Add(2 * time.Hour)) {
		t.Error("a runtime unseen for over an hour is not live")
	}
}
