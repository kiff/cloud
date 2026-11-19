package session_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/session"
)

const testSecretA = "a-secret-of-at-least-thirty-two-bytes-long"
const testSecretB = "another-secret-of-at-least-thirty-two-bytes-long"

func newTestManager(t *testing.T, secret string) *session.Manager {
	t.Helper()
	m, err := session.NewManager([]byte(secret))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestNewManager_RejectsShortSecret(t *testing.T) {
	t.Parallel()
	if _, err := session.NewManager([]byte("too-short")); !errors.Is(err, session.ErrSecretTooShort) {
		t.Fatalf("err = %v, want ErrSecretTooShort", err)
	}
}

func TestIssueAndRead_RoundTrip(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testSecretA)
	rec := httptest.NewRecorder()
	want := session.Session{
		Subject:   "user_alice",
		Email:     "alice@example.com",
		Bearer:    "stub-user_alice",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := m.Issue(rec, want); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != session.CookieName {
		t.Fatalf("expected one %q cookie; got %d", session.CookieName, len(cookies))
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookies[0])
	got, err := m.Read(req)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Subject != want.Subject || got.Bearer != want.Bearer || got.Email != want.Email {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestRead_RejectsTamperedCookie(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testSecretA)
	rec := httptest.NewRecorder()
	if err := m.Issue(rec, session.Session{
		Subject: "u", Bearer: "b", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	c := rec.Result().Cookies()[0]
	// Flip a byte in the payload portion (before the dot).
	idx := strings.IndexByte(c.Value, '.')
	if idx <= 0 {
		t.Fatalf("cookie shape unexpected: %q", c.Value)
	}
	tampered := "A" + c.Value[1:]
	c.Value = tampered

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	if _, err := m.Read(req); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestRead_RejectsCookieFromDifferentSecret(t *testing.T) {
	t.Parallel()
	mA := newTestManager(t, testSecretA)
	mB := newTestManager(t, testSecretB)

	rec := httptest.NewRecorder()
	_ = mA.Issue(rec, session.Session{
		Subject: "u", Bearer: "b", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	c := rec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	if _, err := mB.Read(req); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid (cross-secret)", err)
	}
}

func TestRead_RejectsExpiredCookie(t *testing.T) {
	t.Parallel()
	frozen := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	m, err := session.NewManager([]byte(testSecretA), session.WithClock(func() time.Time { return frozen }))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	rec := httptest.NewRecorder()
	_ = m.Issue(rec, session.Session{
		Subject:   "u",
		Bearer:    "b",
		ExpiresAt: frozen.Add(-time.Minute), // already expired
	})
	c := rec.Result().Cookies()[0]
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	if _, err := m.Read(req); err == nil {
		t.Fatalf("expected expiry error")
	}
}

func TestRead_NoCookieReturnsErrNoSession(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testSecretA)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := m.Read(req); !errors.Is(err, session.ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

func TestClear_SetsExpiredCookie(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testSecretA)
	rec := httptest.NewRecorder()
	m.Clear(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	if cookies[0].MaxAge >= 0 {
		t.Fatalf("MaxAge = %d, want < 0", cookies[0].MaxAge)
	}
}

func TestSession_Valid(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	tests := []struct {
		name string
		s    session.Session
		ok   bool
	}{
		{"happy", session.Session{Subject: "s", Bearer: "b", ExpiresAt: now.Add(time.Hour)}, true},
		{"empty subject", session.Session{Bearer: "b", ExpiresAt: now.Add(time.Hour)}, false},
		{"empty bearer", session.Session{Subject: "s", ExpiresAt: now.Add(time.Hour)}, false},
		{"expired", session.Session{Subject: "s", Bearer: "b", ExpiresAt: now.Add(-time.Hour)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Valid(now)
			if (err == nil) != tt.ok {
				t.Fatalf("Valid() err=%v, want ok=%v", err, tt.ok)
			}
		})
	}
}

// TestIssue_CookieDomain covers the sign-in-loop fix: when a cookie
// domain is configured, the session cookie must carry that Domain
// so a session minted on the marketing apex (kiff.dev) is also sent
// to the dashboard host (app.kiff.dev). Without WithDomain the
// cookie is host-only and the two hosts loop through Clerk forever.
func TestIssue_CookieDomain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		opts       []session.ManagerOption
		wantDomain string
	}{
		{
			name:       "no domain option = host-only",
			opts:       nil,
			wantDomain: "",
		},
		{
			name: "with domain = scoped to kiff.dev (covers subdomains)",
			opts: []session.ManagerOption{session.WithDomain(".kiff.dev")},
			// net/http serializes Domain=.kiff.dev as "kiff.dev"
			// (the leading dot is legacy; RFC 6265 Domain=kiff.dev
			// already matches app.kiff.dev + the apex). Either form
			// fixes the loop; assert the normalized value the
			// stdlib emits.
			wantDomain: "kiff.dev",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := session.NewManager([]byte(testSecretA), tc.opts...)
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			rec := httptest.NewRecorder()
			if err := m.Issue(rec, session.Session{
				Subject:   "user_alice",
				Bearer:    "stub-user_alice",
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			}); err != nil {
				t.Fatalf("Issue: %v", err)
			}
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("expected 1 cookie, got %d", len(cookies))
			}
			if cookies[0].Domain != tc.wantDomain {
				t.Errorf("cookie Domain = %q, want %q", cookies[0].Domain, tc.wantDomain)
			}
		})
	}
}

// TestClear_CookieDomain confirms the clear cookie carries the same
// Domain — otherwise sign-out on app.kiff.dev wouldn't clear a
// .kiff.dev-scoped cookie and the user would appear stuck signed in.
// When a Domain is configured, Clear ALSO emits a host-only deletion
// so a stale pre-domain duplicate (the sign-in-loop cause) is evicted
// too — see TestRead_DuplicateCookies_*.
func TestClear_CookieDomain(t *testing.T) {
	t.Parallel()
	m, err := session.NewManager([]byte(testSecretA), session.WithDomain(".kiff.dev"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	rec := httptest.NewRecorder()
	m.Clear(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected 2 clear cookies (domain + host-only), got %d", len(cookies))
	}
	var sawDomain, sawHostOnly bool
	for _, c := range cookies {
		if c.MaxAge >= 0 {
			t.Errorf("clear cookie %q MaxAge = %d, want negative (expired)", c.Domain, c.MaxAge)
		}
		switch c.Domain {
		case "kiff.dev":
			sawDomain = true
		case "":
			sawHostOnly = true
		default:
			t.Errorf("unexpected clear cookie Domain %q", c.Domain)
		}
	}
	if !sawDomain || !sawHostOnly {
		t.Errorf("clear must evict both variants: sawDomain=%v sawHostOnly=%v", sawDomain, sawHostOnly)
	}
}

// TestClear_NoDomain_SingleCookie confirms that without a configured
// Domain, Clear stays a single host-only deletion (no spurious second
// Set-Cookie).
func TestClear_NoDomain_SingleCookie(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testSecretA)
	rec := httptest.NewRecorder()
	m.Clear(rec)
	if cookies := rec.Result().Cookies(); len(cookies) != 1 {
		t.Fatalf("expected 1 clear cookie, got %d", len(cookies))
	}
}

// issueValue mints a cookie value via the manager and returns just the
// value string, so tests can compose multi-cookie request headers.
func issueValue(t *testing.T, m *session.Manager, s session.Session) string {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := m.Issue(rec, s); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return rec.Result().Cookies()[0].Value
}

// TestRead_DuplicateCookies_AcceptsValidAmongStale is the direct
// regression for the sign-in loop: the browser presents two cookies
// named kiff_session — a stale/invalid one first (the host-only
// pre-domain duplicate) and the current valid one second. r.Cookie
// would return only the first (stale) and fail; Read must scan all
// and accept the valid one.
func TestRead_DuplicateCookies_AcceptsValidAmongStale(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testSecretA)
	valid := issueValue(t, m, session.Session{
		Subject: "user_alice", Bearer: "b", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// Order matters: stale first (as the browser sends the older,
	// host-only cookie first), valid second.
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: "stale.garbage"})
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: valid})

	got, err := m.Read(req)
	if err != nil {
		t.Fatalf("Read should accept the valid duplicate; got err %v", err)
	}
	if got.Subject != "user_alice" {
		t.Fatalf("subject = %q, want user_alice", got.Subject)
	}
}

// TestRead_DuplicateCookies_SkipsExpiredStale covers the same shadowing
// when the stale duplicate is a correctly-signed but EXPIRED session
// (a host-only cookie left over from days ago).
func TestRead_DuplicateCookies_SkipsExpiredStale(t *testing.T) {
	t.Parallel()
	frozen := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	m, err := session.NewManager([]byte(testSecretA), session.WithClock(func() time.Time { return frozen }))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	expired := issueValue(t, m, session.Session{Subject: "old", Bearer: "b", ExpiresAt: frozen.Add(-time.Minute)})
	fresh := issueValue(t, m, session.Session{Subject: "current", Bearer: "b", ExpiresAt: frozen.Add(time.Hour)})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: expired})
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: fresh})

	got, err := m.Read(req)
	if err != nil {
		t.Fatalf("Read should skip the expired stale duplicate; got err %v", err)
	}
	if got.Subject != "current" {
		t.Fatalf("subject = %q, want current", got.Subject)
	}
}

// TestRead_AllDuplicatesInvalid returns the validation error when no
// presented cookie validates (so RequireSession clears + bounces).
func TestRead_AllDuplicatesInvalid(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testSecretA)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: "stale.one"})
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: "stale.two"})
	if _, err := m.Read(req); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}
