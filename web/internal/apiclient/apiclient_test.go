package apiclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// fakeAPI returns a httptest.Server whose responses are scripted
// per-path. Each test wires the routes it needs.
func fakeAPI(t *testing.T, routes map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for pat, h := range routes {
		mux.HandleFunc(pat, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newSessionClient talks to srv through srv's own transport. The tests run in
// parallel, and httptest.Server.Close drops http.DefaultTransport's idle
// connections: through the shared transport, one test's cleanup could break
// another's request ("CloseIdleConnections called").
func newSessionClient(t *testing.T, srv *httptest.Server) *apiclient.Client {
	t.Helper()
	c, err := apiclient.NewSessionAwareWithTransport(srv.URL, 0, srv.Client().Transport)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	return c
}

func TestGetMeBearer_HappyPath(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/me": func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer abc" {
				t.Errorf("Authorization = %q, want Bearer abc", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(apiclient.Me{
				Subject: "subj-1", TenantID: "ten-1", TenantSlug: "demo",
			})
		},
	})
	c := newSessionClient(t, srv)
	got, err := c.GetMeBearer(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetMeBearer: %v", err)
	}
	if got.Subject != "subj-1" || got.TenantID != "ten-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestGetMeBearer_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/me": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", http.StatusUnauthorized)
		},
	})
	c := newSessionClient(t, srv)
	if _, err := c.GetMeBearer(context.Background(), "abc"); !errors.Is(err, apiclient.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestGetMeBearer_EmptyBearerErrors(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, nil)
	c := newSessionClient(t, srv)
	if _, err := c.GetMeBearer(context.Background(), ""); err == nil {
		t.Fatalf("expected error on empty bearer")
	}
}

func TestListTemplatesBearer_DecodesPayload(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/templates": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"templates":[{"slug":"cafe-ops","name":"Cafe","description":"d","sample_event":{"id":"evt"}}]}`))
		},
	})
	c := newSessionClient(t, srv)
	tpls, err := c.ListTemplatesBearer(context.Background(), "abc")
	if err != nil {
		t.Fatalf("ListTemplatesBearer: %v", err)
	}
	if len(tpls) != 1 || tpls[0].Slug != "cafe-ops" {
		t.Fatalf("got %+v", tpls)
	}
}

func TestApplyTemplateBearer_PostsJSONBody(t *testing.T) {
	t.Parallel()
	var captured map[string]string
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/me/domain/template": func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &captured)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"slug":"cafe-ops","name":"Cafe","tenant":"ten-1","version":1}`))
		},
	})
	c := newSessionClient(t, srv)
	resp, err := c.ApplyTemplateBearer(context.Background(), "abc", "cafe-ops")
	if err != nil {
		t.Fatalf("ApplyTemplateBearer: %v", err)
	}
	if captured["slug"] != "cafe-ops" {
		t.Fatalf("server saw %+v", captured)
	}
	if resp.Version != 1 {
		t.Fatalf("version = %d, want 1", resp.Version)
	}
}

func TestCreateKeyBearer_DecodesPlaintext(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/keys": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"k1","tenant":"t1","subject":"s","label":"x","plaintext":"kiff_live_xyz"}`))
		},
	})
	c := newSessionClient(t, srv)
	got, err := c.CreateKeyBearer(context.Background(), "abc", "x", nil)
	if err != nil {
		t.Fatalf("CreateKeyBearer: %v", err)
	}
	if got.Plaintext != "kiff_live_xyz" {
		t.Fatalf("plaintext = %q", got.Plaintext)
	}
}

func TestListReceiptsBearer_RespectsLimit(t *testing.T) {
	t.Parallel()
	var rawQuery string
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/receipts": func(w http.ResponseWriter, r *http.Request) {
			rawQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"receipts":[],"limit":3}`))
		},
	})
	c := newSessionClient(t, srv)
	if _, err := c.ListReceiptsBearer(context.Background(), "abc", 3); err != nil {
		t.Fatalf("ListReceiptsBearer: %v", err)
	}
	if rawQuery != "limit=3" {
		t.Fatalf("query = %q, want limit=3", rawQuery)
	}
}

func TestGetControlDecisionsBearer_BuildsFrozenRoute(t *testing.T) {
	t.Parallel()
	var rawQuery string
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/me/controls/ctrl-1/decisions": func(w http.ResponseWriter, r *http.Request) {
			rawQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"control":{"id":"ctrl-1","domain":"refund","entity":"Order","action":"REFUND_ORDER","environment":"prod","workflow":"refunds","status":"active","mode":"enforce"},
				"window":{"from":"2026-06-01T00:00:00Z","to":"2026-06-02T00:00:00Z"},
				"decisions":{"total":1,"counts":{"allowed":1,"blocked":0,"approval_required":0,"limit_exceeded":0,"invalid":0},"truncated":false,"rows":[]}
			}`))
		},
	})
	c := newSessionClient(t, srv)
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	got, err := c.GetControlDecisionsBearer(context.Background(), "abc", "ctrl-1", apiclient.ControlDecisionsOptions{
		From: from, To: to, Outcomes: []string{"blocked"}, Limit: 5,
	})
	if err != nil {
		t.Fatalf("GetControlDecisionsBearer: %v", err)
	}
	for _, want := range []string{
		"from=2026-06-01T00%3A00%3A00Z",
		"to=2026-06-02T00%3A00%3A00Z",
		"outcome=blocked",
		"limit=5",
	} {
		if !strings.Contains(rawQuery, want) {
			t.Fatalf("query %q missing %q", rawQuery, want)
		}
	}
	if got.Control.Action != "REFUND_ORDER" || got.Control.Mode != "enforce" || got.Decisions.Total != 1 {
		t.Fatalf("decoded wrong shape: %+v", got)
	}
}

func TestRevokeKeyBearer_DeletesKey(t *testing.T) {
	t.Parallel()
	var gotAuth string
	var gotID string
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"DELETE /v1/keys/{id}": func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotID = r.PathValue("id")
			w.WriteHeader(http.StatusNoContent)
		},
	})
	c := newSessionClient(t, srv)
	if err := c.RevokeKeyBearer(context.Background(), "abc", "key-123"); err != nil {
		t.Fatalf("RevokeKeyBearer: %v", err)
	}
	if gotAuth != "Bearer abc" {
		t.Fatalf("Authorization = %q, want Bearer abc", gotAuth)
	}
	if gotID != "key-123" {
		t.Fatalf("id = %q, want key-123", gotID)
	}
}

func TestIngestEventBearer_ForwardsBody(t *testing.T) {
	t.Parallel()
	var captured string
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/events/raw": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			captured = string(b)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"event":{"id":"evt-1"}}`))
		},
	})
	c := newSessionClient(t, srv)
	body, err := c.IngestEventBearer(context.Background(), "abc", []byte(`{"id":"evt-1","type":"X"}`))
	if err != nil {
		t.Fatalf("IngestEventBearer: %v", err)
	}
	if !strings.Contains(captured, `"id":"evt-1"`) {
		t.Fatalf("server didn't see body; captured=%q", captured)
	}
	if !strings.Contains(string(body), `"event"`) {
		t.Fatalf("response missing event: %q", body)
	}
}

func TestGrantApprovalBearer_SendsActor(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/approvals/{id}/grant": func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &captured)
			w.WriteHeader(http.StatusOK)
		},
	})
	c := newSessionClient(t, srv)
	if err := c.GrantApprovalBearer(context.Background(), "abc", "appr-1", "actor-1", "lgtm"); err != nil {
		t.Fatalf("GrantApprovalBearer: %v", err)
	}
	actor, _ := captured["actor"].(map[string]any)
	if actor["id"] != "actor-1" {
		t.Fatalf("actor.id = %v, want actor-1", actor["id"])
	}
	if captured["reason"] != "lgtm" {
		t.Fatalf("reason = %v", captured["reason"])
	}
}

func TestNewSessionAware_RejectsEmptyBaseURL(t *testing.T) {
	t.Parallel()
	if _, err := apiclient.NewSessionAware("", 0); err == nil {
		t.Fatalf("expected error on empty BaseURL")
	}
}
