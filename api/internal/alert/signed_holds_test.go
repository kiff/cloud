package alert_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
)

// The signature is HMAC-SHA256 over "<unix>.<body>"; a receiver
// recomputes it with the secret it was shown.
func TestSignatureHeader(t *testing.T) {
	t.Parallel()
	at := time.Unix(1760443200, 0)
	body := []byte(`{"event_id":"evt_1"}`)
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write([]byte("1760443200." + string(body)))
	want := "t=1760443200,v1=" + hex.EncodeToString(mac.Sum(nil))
	if got := alert.SignatureHeader("whsec_test", at, body); got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if s := alert.NewSigningSecret(); !strings.HasPrefix(s, "whsec_") || len(s) < 40 || s == alert.NewSigningSecret() {
		t.Errorf("secret %q", s)
	}
}

// A target with a secret gets every delivery signed; one made before
// signing is sent unsigned, as before.
func TestHTTPSinkSignsWhenTheTargetHasASecret(t *testing.T) {
	t.Parallel()
	type got struct {
		sig  string
		body []byte
	}
	seen := make(chan got, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen <- got{r.Header.Get("KIFF-Signature"), b}
	}))
	defer srv.Close()
	sink := alert.NewHTTPSink(true)
	body := []byte(`{"event_id":"evt_1"}`)

	if err := sink.Send(ctx(), alert.Target{URL: srv.URL, Secret: "whsec_test"}, body); err != nil {
		t.Fatal(err)
	}
	g := <-seen
	parts := strings.SplitN(g.sig, ",", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") {
		t.Fatalf("signature header %q", g.sig)
	}
	unix, err := strconv.ParseInt(strings.TrimPrefix(parts[0], "t="), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if g.sig != alert.SignatureHeader("whsec_test", time.Unix(unix, 0), g.body) || string(g.body) != string(body) {
		t.Errorf("signature %q does not verify for %s", g.sig, g.body)
	}

	if err := sink.Send(ctx(), alert.Target{URL: srv.URL}, body); err != nil {
		t.Fatal(err)
	}
	if g := <-seen; g.sig != "" {
		t.Errorf("a target without a secret was signed: %q", g.sig)
	}
}

// A Card hold names the held call, where to answer it and when it stops
// waiting; every payload carries an event id. hold_answered goes only to
// targets that list it, never to a target with no outcome filter.
func TestDispatcherHoldPayloads(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-all", TenantID: "T", URL: "https://h/all", Enabled: true})
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-phone", TenantID: "T", URL: "https://h/phone", Outcomes: []string{"owner_required", alert.HoldAnswered}, Enabled: true})
	sink := newCaptureSink(8)
	d := alert.NewDispatcher(store, sink, alert.WithBaseURL("https://app.kiff.dev"))
	expires := time.Date(2026, 10, 14, 12, 10, 0, 0, time.UTC)

	d.Notify(alert.Event{TenantID: "T", Action: "refund", EntityID: "o1", Outcome: "owner_required", DecidedAt: time.Now().UTC(),
		Hold: &alert.Hold{ID: "ce_1", Status: "held", ReviewURL: "/needs-you/ce_1", ExpiresAt: &expires}})
	sink.waitFor(t, 2)
	sink.mu.Lock()
	for _, b := range sink.bodies {
		var p struct {
			EventID string     `json:"event_id"`
			Hold    alert.Hold `json:"hold"`
		}
		_ = json.Unmarshal(b, &p)
		if !strings.HasPrefix(p.EventID, "evt_") || p.Hold.ID != "ce_1" || p.Hold.ReviewURL != "https://app.kiff.dev/needs-you/ce_1" ||
			p.Hold.ExpiresAt == nil || !p.Hold.ExpiresAt.Equal(expires) {
			t.Errorf("owner_required payload %s", b)
		}
	}
	sink.sent, sink.bodies = nil, nil
	sink.mu.Unlock()

	d.Notify(alert.Event{TenantID: "T", Action: "refund", EntityID: "o1", Outcome: alert.HoldAnswered, DecidedAt: time.Now().UTC(),
		Hold: &alert.Hold{ID: "ce_1", Status: "approved"}})
	sink.waitFor(t, 1)
	time.Sleep(100 * time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.sent) != 1 || sink.sent[0].ID != "t-phone" || !strings.Contains(string(sink.bodies[0]), `"type":"kiff.hold.answered"`) {
		t.Fatalf("hold_answered went to %+v: %s", sink.sent, sink.bodies)
	}
}

// A pack request's alert carries the request: who asked, its pack and
// status, and an admin's note, so the app tells the member alone.
func TestDispatcherPackRequestPayload(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-phone", TenantID: "T", URL: "https://h/phone",
		Outcomes: []string{alert.PackRequested, alert.PackRequestAnswered}, Enabled: true})
	sink := newCaptureSink(2)
	d := alert.NewDispatcher(store, sink)
	d.Notify(alert.Event{TenantID: "T", Action: "tafs.pack_request", EntityID: "pr_1", EntityType: "pack_request",
		Outcome: alert.PackRequestAnswered, Reasons: []string{"rayo", "declined"}, DecidedAt: time.Now().UTC(),
		Request: &alert.PackRequest{ID: "pr_1", Pack: "rayo", Status: "declined", AskedBy: "user_bob", Note: "Next month"}})
	sink.waitFor(t, 1)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var p struct {
		Type    string            `json:"type"`
		Request alert.PackRequest `json:"request"`
	}
	if err := json.Unmarshal(sink.bodies[0], &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != "kiff.pack_request" || p.Request != (alert.PackRequest{ID: "pr_1", Pack: "rayo", Status: "declined", AskedBy: "user_bob", Note: "Next month"}) {
		t.Fatalf("payload: %+v", p)
	}
}

// A connection a vendor signed out goes only to targets that list it, with
// the connection and the event id the sender chose, so a resend dedupes.
func TestDispatcherConnectionSignedOutPayload(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-all", TenantID: "T", URL: "https://h/all", Enabled: true})
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-phone", TenantID: "T", URL: "https://h/phone",
		Outcomes: []string{"owner_required", alert.ConnectionSignedOut}, Enabled: true})
	sink := newCaptureSink(2)
	d := alert.NewDispatcher(store, sink)
	at := time.Date(2026, 10, 30, 22, 10, 0, 0, time.UTC)
	d.Notify(alert.Event{TenantID: "T", EntityID: "g_1", EntityType: "connection", Outcome: alert.ConnectionSignedOut, DecidedAt: at,
		EventID:    "conn-out:g_1:2026-10-30T22:10:00Z",
		Connection: &alert.Connection{Grant: "g_1", Agent: "refunds-k3f9", Vendor: "Stripe", Reason: "refresh_refused", SignedOutAt: at}})
	sink.waitFor(t, 1)
	time.Sleep(100 * time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.sent) != 1 || sink.sent[0].ID != "t-phone" {
		t.Fatalf("connection_signed_out went to %+v", sink.sent)
	}
	var p struct {
		Type       string           `json:"type"`
		EventID    string           `json:"event_id"`
		Outcome    string           `json:"outcome"`
		Connection alert.Connection `json:"connection"`
	}
	if err := json.Unmarshal(sink.bodies[0], &p); err != nil {
		t.Fatal(err)
	}
	want := alert.Connection{Grant: "g_1", Agent: "refunds-k3f9", Vendor: "Stripe", Reason: "refresh_refused", SignedOutAt: at}
	if p.Type != "kiff.connection.signed_out" || p.EventID != "conn-out:g_1:2026-10-30T22:10:00Z" || p.Outcome != "connection_signed_out" || p.Connection != want {
		t.Errorf("payload %s", sink.bodies[0])
	}
}
