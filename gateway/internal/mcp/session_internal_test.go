package mcp

import (
	"testing"
	"time"
)

// An idle session is dropped after sessionIdle; a used one is kept.
func TestSessionIdleExpiry(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	c := &Client{now: func() time.Time { return now }}
	key := sessionKey("https://tool.example.com/mcp", "cred-a")
	c.keep(key, openSession{id: "s-1"})
	now = now.Add(sessionIdle - time.Second)
	if s, ok := c.session(key); !ok || s.id != "s-1" {
		t.Fatalf("a recently used session: %v %v", s, ok)
	}
	now = now.Add(sessionIdle + time.Second)
	if _, ok := c.session(key); ok {
		t.Fatal("an idle session was kept")
	}
	if sessionKey("u", "cred-a") == sessionKey("u", "cred-b") {
		t.Fatal("two credentials share a key")
	}
}
