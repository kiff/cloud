package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/gateway/internal/mcp"
)

// legacyServer is an initialize-era tool server that issues session ids,
// one per initialize, and can be told to expire them or to fail a call.
type legacyServer struct {
	mu        sync.Mutex
	inits     int
	ran       int               // tools/calls run
	sessions  map[string]string // session id → the bearer that opened it
	expireAll bool              // answer 404 to every known session once
	fail500   bool              // answer the next tools/call with 500, after running it
	bearers   []string          // the bearer of each tools/call
}

func (l *legacyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sessions == nil {
		l.sessions = map[string]string{}
	}
	w.Header().Set("Content-Type", "application/json")
	switch req.Method {
	case "server/discover":
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, req.ID)
	case "initialize":
		l.inits++
		id := fmt.Sprintf("s-%d", l.inits)
		l.sessions[id] = r.Header.Get("Authorization")
		w.Header().Set("Mcp-Session-Id", id)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, req.ID)
	case "tools/call":
		sid := r.Header.Get("Mcp-Session-Id")
		if owner, ok := l.sessions[sid]; !ok || owner != r.Header.Get("Authorization") || l.expireAll {
			// An expired or foreign session: refused, not run (MCP's 404).
			l.expireAll = false
			l.sessions = map[string]string{}
			http.Error(w, "no such session", http.StatusNotFound)
			return
		}
		l.ran++
		l.bearers = append(l.bearers, r.Header.Get("Authorization"))
		if l.fail500 {
			l.fail500 = false
			http.Error(w, "upstream broke", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"done"}]}}`, req.ID)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

func (l *legacyServer) counts() (inits, ran int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inits, l.ran
}

func call(t *testing.T, c *mcp.Client, url, bearer string) error {
	t.Helper()
	res, err := c.CallTool(context.Background(), url, bearer, "refund", nil)
	if err == nil && !strings.Contains(string(res), "done") {
		t.Fatalf("result %s", res)
	}
	return err
}

// One session per server and credential, reused: a call doesn't pay
// initialize and initialized each time.
func TestClientReusesALegacySession(t *testing.T) {
	l := &legacyServer{}
	srv := httptest.NewServer(l)
	defer srv.Close()
	c := &mcp.Client{}
	for i := 0; i < 3; i++ {
		if err := call(t, c, srv.URL, "cred-a"); err != nil {
			t.Fatal(err)
		}
	}
	if inits, ran := l.counts(); inits != 1 || ran != 3 {
		t.Fatalf("initialize %d times for %d calls; want 1 for 3", inits, ran)
	}
}

// A session is never shared across credentials, so never across accounts
// or tools.
func TestClientSessionsAreNotSharedAcrossCredentials(t *testing.T) {
	l := &legacyServer{}
	srv := httptest.NewServer(l)
	defer srv.Close()
	c := &mcp.Client{}
	for _, b := range []string{"cred-a", "cred-b", "cred-a", "cred-b"} {
		if err := call(t, c, srv.URL, b); err != nil {
			t.Fatal(err)
		}
	}
	if inits, ran := l.counts(); inits != 2 || ran != 4 {
		t.Fatalf("initialize %d, ran %d; want 2 and 4", inits, ran)
	}
}

// A kept session the server has expired is answered 404 without running
// the call, as MCP says: a new session is opened and the call sent once in
// it, so it runs exactly once.
func TestClientReopensAnExpiredSessionAndRunsTheCallOnce(t *testing.T) {
	l := &legacyServer{}
	srv := httptest.NewServer(l)
	defer srv.Close()
	c := &mcp.Client{}
	if err := call(t, c, srv.URL, "cred-a"); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.expireAll = true
	l.mu.Unlock()
	if err := call(t, c, srv.URL, "cred-a"); err != nil {
		t.Fatalf("after the session expired: %v", err)
	}
	if inits, ran := l.counts(); inits != 2 || ran != 2 {
		t.Fatalf("initialize %d, ran %d; want 2 and 2 (each call once)", inits, ran)
	}
}

// Any other failure of a call in a kept session is reported as before (it
// may have run: Sent), never re-sent, and the session is dropped.
func TestClientNeverResendsACallThatMayHaveRun(t *testing.T) {
	l := &legacyServer{}
	srv := httptest.NewServer(l)
	defer srv.Close()
	c := &mcp.Client{}
	if err := call(t, c, srv.URL, "cred-a"); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.fail500 = true
	l.mu.Unlock()
	err := call(t, c, srv.URL, "cred-a")
	var ce *mcp.CallError
	if !errors.As(err, &ce) || !ce.Sent {
		t.Fatalf("a 500 after the call: %v, want a CallError that may have been sent", err)
	}
	if _, ran := l.counts(); ran != 2 {
		t.Fatalf("ran %d; the failed call must run once and not be re-sent", ran)
	}
	// The next call opens a new session.
	if err := call(t, c, srv.URL, "cred-a"); err != nil {
		t.Fatal(err)
	}
	if inits, _ := l.counts(); inits != 2 {
		t.Fatalf("initialize %d; the failed session must be dropped", inits)
	}
}

// The call's own headers (the permit) go on its tools/call only, with a
// kept session too.
func TestClientCallHeadersStayOnTheirCall(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "server/discover":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, req.ID)
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, req.ID)
		case "tools/call":
			mu.Lock()
			seen[string(req.Params.Arguments)] = r.Header.Get("KIFF-Permit")
			mu.Unlock()
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"done"}]}}`, req.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	c := &mcp.Client{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := mcp.WithCallHeader(context.Background(), "KIFF-Permit", fmt.Sprintf("permit-%d", i))
			if _, err := c.CallTool(ctx, srv.URL, "cred-a", "refund", json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		if got := seen[fmt.Sprintf(`{"n":%d}`, i)]; got != fmt.Sprintf("permit-%d", i) {
			t.Fatalf("call %d carried permit %q", i, got)
		}
	}
}

// Each call in a kept session has its own request id, never used before in
// that session, at once or one after the other: initialize is 1, the
// calls 2, 3, …
func TestClientRequestIDsAreNotReusedInASession(t *testing.T) {
	var mu sync.Mutex
	ids := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "server/discover":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, req.ID)
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s-1")
			mu.Lock()
			ids[string(req.ID)]++
			mu.Unlock()
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, req.ID)
		case "tools/call":
			mu.Lock()
			ids[string(req.ID)]++
			mu.Unlock()
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"done"}]}}`, req.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	c := &mcp.Client{}
	if err := call(t, c, srv.URL, "cred-a"); err != nil { // opens the session
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := call(t, c, srv.URL, "cred-a"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := call(t, c, srv.URL, "cred-a"); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 11 {
		t.Fatalf("%d distinct ids for 1 initialize and 10 calls: %v", len(ids), ids)
	}
	for id, n := range ids {
		if n != 1 {
			t.Fatalf("id %s used %d times in one session", id, n)
		}
	}
}
