package gateway

import (
	"fmt"
	"testing"
	"time"
	"unicode/utf8"
)

func TestChatClientsVerified(t *testing.T) {
	c := ParseChatClients("claude-code>=2.1.287, bad, other>=x, codex>=1.2")
	for _, tc := range []struct {
		name, version string
		want          bool
	}{
		{"claude-code", "2.1.287", true},
		{"claude-code", "2.1.300", true},
		{"claude-code", "2.2", true},
		{"claude-code", "3.0.0-beta", true},
		{"claude-code", "2.1.286", false},
		{"claude-code", "2.1", false},
		{"claude-code", "", false},
		{"claude-code", "latest", false},
		{"codex", "1.2.0", true},
		{"other", "9", false},
		{"bad", "1", false},
	} {
		if got := c.Verified(tc.name, tc.version); got != tc.want {
			t.Errorf("%s %s: %v", tc.name, tc.version, got)
		}
	}
	if len(ParseChatClients("none")) != 0 {
		t.Error(`"none" verifies a client`)
	}
}

// Only this app's own Needs you page for the hold is ever elicited.
func TestOwnNeedsYou(t *testing.T) {
	g := &Gateway{AppURL: "https://app.kiff.dev"}
	if !g.ownNeedsYou("https://app.kiff.dev/needs-you/exc-1", "exc-1") {
		t.Fatal("own page refused")
	}
	for _, link := range []string{
		"http://app.kiff.dev/needs-you/exc-1",
		"https://app.kiff.dev.evil.example/needs-you/exc-1",
		"https://evil.example/needs-you/exc-1",
		"https://user@app.kiff.dev/needs-you/exc-1",
		"https://app.kiff.dev/needs-you/exc-2",
		"https://app.kiff.dev/needs-you/exc-1?next=//evil.example",
		"https://app.kiff.dev/needs-you/exc-1#x",
		"/needs-you/exc-1",
	} {
		if g.ownNeedsYou(link, "exc-1") {
			t.Errorf("%s accepted", link)
		}
	}
	if (&Gateway{AppURL: "http://localhost:8181"}).ownNeedsYou("http://localhost:8181/needs-you/exc-1", "exc-1") {
		t.Error("a non-https app is elicited")
	}
}

// One agent varying its client name or version cannot grow the cache
// past maxClientsPerAgent entries, a changed version is written at most
// once a minute, and old entries are dropped.
func TestSeenCacheIsBounded(t *testing.T) {
	var c seenCache
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 1000; i++ {
		c.admit(ClientSeen{TenantID: "t", AgentID: "a", Name: fmt.Sprint("n", i), Version: fmt.Sprint(i)}, now)
	}
	writes := 0
	for i := 0; i < 5000; i++ {
		if c.admit(ClientSeen{TenantID: "t", AgentID: "a", Name: "n0", Version: fmt.Sprint("v", i)}, now.Add(time.Duration(i)*time.Millisecond)) {
			writes++
		}
	}
	if len(c.last) != maxClientsPerAgent || writes != 0 {
		t.Fatalf("cache holds %d entries (want %d), %d version writes within a minute (want 0)", len(c.last), maxClientsPerAgent, writes)
	}
	if !c.admit(ClientSeen{TenantID: "t", AgentID: "a", Name: "n0", Version: "new"}, now.Add(clientChangeEvery)) {
		t.Error("a changed version was not written after a minute")
	}
	if !c.admit(ClientSeen{TenantID: "t", AgentID: "b", Name: "x"}, now) {
		t.Error("another agent was refused")
	}
	c.admit(ClientSeen{TenantID: "t", AgentID: "c", Name: "y"}, now.Add(2*clientSeenEvery))
	if len(c.last) != 1 {
		t.Errorf("old entries kept: %d", len(c.last))
	}
}

func TestCleanKeepsWholeCharacters(t *testing.T) {
	if got := clean("ab\x00c\nd", 10); got != "abcd" {
		t.Errorf("control characters: %q", got)
	}
	if got := clean("ééé", 2); got != "éé" || !utf8.ValidString(got) {
		t.Errorf("split a character: %q", got)
	}
	if got := clean("a\xffb", 10); got != "ab" {
		t.Errorf("invalid UTF-8: %q", got)
	}
}
