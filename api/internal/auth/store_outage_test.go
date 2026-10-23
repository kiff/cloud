package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// downKeys is a key store whose database can't be reached.
type downKeys struct{ *MemoryAPIKeyStore }

func (downKeys) GetByHash(context.Context, [APIKeyHashSize]byte) (APIKeyRecord, error) {
	return APIKeyRecord{}, errors.New("failed to connect to `user=kiff_admin database=kiff`: dial error")
}

// downSessions is a session store whose database can't be reached.
type downSessions struct{ DeveloperSessionStore }

func (downSessions) GetByHash(context.Context, [32]byte, time.Time) (DeveloperSession, error) {
	return DeveloperSession{}, errors.New("failed to connect to `user=kiff_admin database=kiff`: dial error")
}

// A store that can't be read answers 503, so a client retries instead of
// taking a valid key for a bad one (the 22 Oct resize); an unknown or
// revoked key is still a 401.
func TestAStoreOutageIsUnavailableNotUnauthorized(t *testing.T) {
	store := NewMemoryAPIKeyStore()
	planted := plantKey(t, store, "tenant-a", "agent-b", nil)
	unknown, err := NewAPIKey("tenant-a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewDevSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	call := func(mw *Middleware, bearer string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		mw.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
		return rec.Code
	}
	up := NewMiddleware(NewStubProvider(NewMapResolver()), store, NewMapResolver())
	down := NewMiddleware(NewStubProvider(NewMapResolver()), downKeys{store}, NewMapResolver(),
		WithDeveloperSessions(downSessions{NewMemoryDeveloperSessionStore()}))
	for name, c := range map[string]struct {
		mw     *Middleware
		bearer string
		want   int
	}{
		"a live key":                {up, planted.Plaintext, http.StatusOK},
		"an unknown key":            {up, unknown.Plaintext, http.StatusUnauthorized},
		"a key, the store down":     {down, planted.Plaintext, http.StatusServiceUnavailable},
		"a session, the store down": {down, session.Plaintext, http.StatusServiceUnavailable},
	} {
		if got := call(c.mw, c.bearer); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if err := store.Revoke(context.Background(), planted.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := call(up, planted.Plaintext); got != http.StatusUnauthorized {
		t.Errorf("a revoked key: %d, want 401", got)
	}
}
