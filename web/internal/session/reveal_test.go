package session_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/session"
)

// A minted API key must not travel in a readable cookie. The flash
// carries an opaque reference; the key is revealed once, to the
// session that minted it, within the flash lifetime.

const revealedKey = "kiff_live_t1_supersecretplaintext0000"

type revealRig struct {
	mgr   *session.Manager
	clock time.Time
}

func newRevealRig(t *testing.T) *revealRig {
	t.Helper()
	rig := &revealRig{clock: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)}
	mgr, err := session.NewManager([]byte(testSecretA), session.WithClock(func() time.Time { return rig.clock }))
	if err != nil {
		t.Fatal(err)
	}
	rig.mgr = mgr
	return rig
}

// sessionCookie is a signed-in session for subject.
func (rig *revealRig) sessionCookie(t *testing.T, subject string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := rig.mgr.Issue(rec, session.Session{Subject: subject, Bearer: "b", ExpiresAt: rig.clock.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return rec.Result().Cookies()[0]
}

func (rig *revealRig) issue(t *testing.T, owner string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := rig.mgr.IssueFlash(rec, session.Flash{
		Kind: session.FlashKeyRevealed,
		Key:  &session.FlashKeyPayload{ID: "k1", Plaintext: revealedKey, Label: "agent", Owner: owner},
	}); err != nil {
		t.Fatal(err)
	}
	return rec.Result().Cookies()[0]
}

func (rig *revealRig) read(t *testing.T, flash, sess *http.Cookie) *session.Flash {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(flash)
	if sess != nil {
		req.AddCookie(sess)
	}
	got, ok := rig.mgr.ReadFlash(httptest.NewRecorder(), req)
	if !ok {
		t.Fatal("ReadFlash: no flash")
	}
	return got
}

func TestKeyReveal_CookieCannotBeDecodedToTheKey(t *testing.T) {
	t.Parallel()
	rig := newRevealRig(t)
	c := rig.issue(t, "alice")
	if strings.Contains(c.Value, revealedKey) {
		t.Fatal("the key is in the cookie value")
	}
	payload, _, _ := strings.Cut(c.Value, ".")
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding} {
		if raw, err := enc.DecodeString(payload); err == nil && strings.Contains(string(raw), revealedKey) {
			t.Fatalf("decoding the cookie recovers the key: %s", raw)
		}
	}
}

func TestKeyReveal_OnceForTheMintingSession(t *testing.T) {
	t.Parallel()
	rig := newRevealRig(t)
	c := rig.issue(t, "alice")
	alice := rig.sessionCookie(t, "alice")

	if got := rig.read(t, c, alice); got.Key == nil || got.Key.Plaintext != revealedKey {
		t.Fatalf("first read by the minting session: %+v", got.Key)
	}
	// Replaying the same cookie value reveals nothing: consumed.
	if got := rig.read(t, c, alice); got.Key == nil || got.Key.Plaintext != "" || got.Key.ID != "k1" {
		t.Fatalf("replayed reveal: %+v", got.Key)
	}
}

func TestKeyReveal_AnotherSessionCannotRedeemIt(t *testing.T) {
	t.Parallel()
	rig := newRevealRig(t)
	c := rig.issue(t, "alice")

	if got := rig.read(t, c, rig.sessionCookie(t, "mallory")); got.Key.Plaintext != "" {
		t.Fatal("another session redeemed the reveal")
	}
	if got := rig.read(t, c, nil); got.Key.Plaintext != "" {
		t.Fatal("a request with no session redeemed the reveal")
	}
	// The attempt burned it, so the owner cannot be raced for it either.
	if got := rig.read(t, c, rig.sessionCookie(t, "alice")); got.Key.Plaintext != "" {
		t.Fatal("a reference survived a failed redemption")
	}
}

func TestKeyReveal_ExpiresWithTheFlash(t *testing.T) {
	t.Parallel()
	rig := newRevealRig(t)
	c := rig.issue(t, "alice")
	alice := rig.sessionCookie(t, "alice")
	rig.clock = rig.clock.Add(6 * time.Minute)
	if got := rig.read(t, c, alice); got.Key.Plaintext != "" {
		t.Fatal("an expired reveal was redeemed")
	}
}

// A page that shows a revealed key is never stored by a browser or a
// proxy, whichever page it is; a page whose flash carries no key, or a
// spent one, keeps its usual caching.
func TestKeyReveal_PageIsNotStored(t *testing.T) {
	t.Parallel()
	rig := newRevealRig(t)
	c := rig.issue(t, "alice")
	alice := rig.sessionCookie(t, "alice")
	readHeader := func(flash *http.Cookie) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(flash)
		req.AddCookie(alice)
		rec := httptest.NewRecorder()
		if _, ok := rig.mgr.ReadFlash(rec, req); !ok {
			t.Fatal("ReadFlash: no flash")
		}
		return rec.Header().Get("Cache-Control")
	}
	if got := readHeader(c); got != "no-store" {
		t.Fatalf("page revealing the key: Cache-Control %q", got)
	}
	if got := readHeader(c); got != "" {
		t.Errorf("replay with nothing to reveal: Cache-Control %q", got)
	}
	rec := httptest.NewRecorder()
	if err := rig.mgr.IssueFlash(rec, session.Flash{Kind: session.FlashError, Message: "no key here"}); err != nil {
		t.Fatal(err)
	}
	if got := readHeader(rec.Result().Cookies()[0]); got != "" {
		t.Errorf("flash without a key: Cache-Control %q", got)
	}
}
