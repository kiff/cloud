package halts_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/permit"
)

// Every planque is asked, and a halt is confirmed only when each says it
// sees it; none at all confirms at once.
func TestPlanquesConfirmOnlyWhenEachSeesTheHalt(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 0, 0, 123000, time.UTC)
	var got struct {
		Path, Agent, HaltedAt string
	}
	seen := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		got.Path, got.Agent, got.HaltedAt = r.URL.Path, body["agent"], body["halted_at"]
		_ = json.NewEncoder(w).Encode(map[string]bool{"seen": seen})
	}))
	defer srv.Close()
	p := &halts.Planques{URLs: []string{srv.URL + "/"}}
	if err := p.Confirm(ctx, "t1", "refund-agent", at); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/v1/accounts/t1/halts/confirm" || got.Agent != "refund-agent" || got.HaltedAt != at.Format(time.RFC3339Nano) {
		t.Fatalf("asked %+v", got)
	}
	seen = false
	if err := p.Confirm(ctx, "t1", "refund-agent", at); err == nil {
		t.Fatal("a planque that doesn't see the halt confirmed it")
	}
	if err := (&halts.Planques{URLs: []string{"http://127.0.0.1:1"}}).Confirm(ctx, "t1", "", at); err == nil {
		t.Fatal("an unreachable planque confirmed")
	}
	if err := (&halts.Planques{}).Confirm(ctx, "t1", "", at); err != nil {
		t.Fatalf("no planque: %v", err)
	}
}

// KIFF signs each ask with its permit key, as a halt-confirm token a permit
// verifier refuses, naming the halt it asks about.
func TestPlanquesAskIsSignedForThatHalt(t *testing.T) {
	key, err := permit.ParseKey("k1:" + base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 22, 9, 0, 0, 123000, time.UTC)
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get(permit.HaltConfirmHeader)
		_ = json.NewEncoder(w).Encode(map[string]bool{"seen": true})
	}))
	defer srv.Close()
	p := &halts.Planques{URLs: []string{srv.URL}, Key: &key, Issuer: "https://api.kiff.dev"}
	if err := p.Confirm(ctx, "t1", "refund-agent", at); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWS: %q", token)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(key.Public().Pub, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("the ask doesn't verify with the permit key")
	}
	var h struct{ Typ, Kid string }
	var c permit.HaltConfirm
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if json.Unmarshal(hb, &h) != nil || json.Unmarshal(cb, &c) != nil {
		t.Fatal("undecodable")
	}
	if h.Typ != permit.HaltConfirmType || h.Kid != "k1" || c.Iss != "https://api.kiff.dev" || c.Tenant != "t1" || c.Agent != "refund-agent" ||
		c.HaltedAt != at.Format(time.RFC3339Nano) || c.Exp-c.Iat != 60 || c.Jti == "" {
		t.Fatalf("header %+v, claims %+v", h, c)
	}
}
