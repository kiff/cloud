package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/attention"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

type fakeAttentionStore struct{ actErr error }

func (f fakeAttentionStore) List(context.Context, string, string, int, int, bool) (attention.Feed, error) {
	return attention.Feed{}, nil
}
func (f fakeAttentionStore) MarkSeen(context.Context, string, string, map[string]int64) error {
	return nil
}
func (f fakeAttentionStore) Act(context.Context, string, string, string, string, int64) error {
	return f.actErr
}

// A lost claim is a 409 the reviewer can act on; an outage is a 503, never
// dressed up as a conflict.
func TestAttentionActStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"won", nil, http.StatusNoContent},
		{"lost race", attention.ErrConflict, http.StatusConflict},
		{"store down", errors.New("connection refused"), http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			handlers.NewAttentionHandler(fakeAttentionStore{actErr: tc.err}).Register(mux)
			req := httptest.NewRequest(http.MethodPost, "/v1/me/attention/approval:a1/claim", strings.NewReader(`{"revision":1}`))
			req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: "bob", Tenant: "t1", Method: auth.MethodSession}))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// kindAttentionStore records which kind the list asked for.
type kindAttentionStore struct {
	fakeAttentionStore
	asked *[]string
}

func (k kindAttentionStore) ListKind(_ context.Context, _, _ string, _, _ int, _ bool, kind string) (attention.Feed, error) {
	*k.asked = append(*k.asked, kind)
	return attention.Feed{}, nil
}

// Needs you lists domain approvals and blocked calls each on their
// own, as it does Card warnings; any other kind is refused.
func TestAttentionListByKind(t *testing.T) {
	var asked []string
	mux := http.NewServeMux()
	handlers.NewAttentionHandler(kindAttentionStore{asked: &asked}).Register(mux)
	get := func(kind string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/me/attention?kind="+kind, nil)
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: "bob", Tenant: "t1", Method: auth.MethodSession}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, kind := range []string{"card_warning", "approval", "blocked"} {
		if code := get(kind); code != http.StatusOK {
			t.Errorf("kind %s = %d", kind, code)
		}
	}
	if code := get("bogus"); code != http.StatusBadRequest {
		t.Errorf("unknown kind = %d", code)
	}
	if strings.Join(asked, ",") != "card_warning,approval,blocked" {
		t.Errorf("asked the store for %v", asked)
	}
}
