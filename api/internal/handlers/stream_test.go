package handlers

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/stream"
)

// serveStream stands up the SSE handler behind a middleware that injects
// the given tenant identity (mimicking the auth middleware).
func serveStream(t *testing.T, b *stream.Broker, tenant string) *httptest.Server {
	t.Helper()
	h := NewStreamHandler(b, nil)
	h.heartbeat = 150 * time.Millisecond
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A person on the account, who the stream is for.
		ctx := auth.WithIdentity(r.Context(), auth.Identity{Tenant: tenant, Subject: "u",
			MemberRole: membership.RoleViewer, Roles: membership.IdentityRoles(membership.RoleViewer)})
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	return httptest.NewServer(wrapped)
}

func TestStreamHandler_StreamsDecisionEvent(t *testing.T) {
	t.Parallel()
	b := stream.NewBroker()
	srv := serveStream(t, b, "t1")
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/me/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	// Wait for the connection to be established, then publish.
	time.Sleep(80 * time.Millisecond)
	b.Publish("t1", stream.Event{Type: "decision", Data: map[string]any{"outcome": "allowed", "action_name": "REFUND_ORDER"}})

	sawEvent, sawData := false, false
	deadline := time.After(3 * time.Second)
	for !(sawEvent && sawData) {
		select {
		case l := <-lines:
			if strings.HasPrefix(l, "event: decision") {
				sawEvent = true
			}
			if strings.HasPrefix(l, "data:") && strings.Contains(l, "allowed") && strings.Contains(l, "REFUND_ORDER") {
				sawData = true
			}
		case <-deadline:
			t.Fatalf("did not receive decision event frame (event=%v data=%v)", sawEvent, sawData)
		}
	}
}

func TestStreamHandler_Unauthenticated401(t *testing.T) {
	t.Parallel()
	b := stream.NewBroker()
	h := NewStreamHandler(b, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	// No identity injected → 401.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/stream", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestNewStreamHandler_NilBroker(t *testing.T) {
	t.Parallel()
	if NewStreamHandler(nil, nil) != nil {
		t.Fatal("nil broker should yield a nil handler")
	}
	// Register on nil handler is a no-op (must not panic).
	var h *StreamHandler
	h.Register(http.NewServeMux())
}
