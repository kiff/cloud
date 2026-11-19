package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// deviceAPI fakes the API's device routes.
type deviceAPI struct {
	mu        sync.Mutex
	linkAgent string
	sent      map[string]string
	denied    bool
}

func (f *deviceAPI) client(t *testing.T) *apiclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]string
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/oauth/device/BCDF-GHJK":
			_ = json.NewEncoder(w).Encode(map[string]any{"user_code": "BCDF-GHJK", "client_name": "kiff-cli",
				"can_consent": true, "agent_id": f.linkAgent})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/oauth/device/"):
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/v1/me/agents":
			_, _ = w.Write([]byte(`{"agents":[{"id":"support","sources":["card"]}]}`))
		case r.URL.Path == "/v1/me/mandates":
			_, _ = w.Write([]byte(`{"mandates":[{"id":"support-card","subject":"support","active":true,"grants":[{"action":"get_order"}]}]}`))
		case r.URL.Path == "/v1/oauth/device/approve":
			f.sent = body
			_, _ = w.Write([]byte(`{"status":"approved"}`))
		case r.URL.Path == "/v1/oauth/device/deny":
			f.denied = true
			_, _ = w.Write([]byte(`{"status":"denied"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func deviceMux(t *testing.T, f *deviceAPI) *http.ServeMux {
	mux := http.NewServeMux()
	NewOAuthDeviceHandler(f.client(t), nil, nil, nil).Register(mux)
	return mux
}

func TestOAuthDevice_AsksForTheCode(t *testing.T) {
	t.Parallel()
	body := consentDo(deviceMux(t, &deviceAPI{}), "GET", "/oauth/device", nil).Body.String()
	mustContain(t, body, "Connect an agent that can only ask", `name="code"`, "Only enter a code you started yourself")
	body = consentDo(deviceMux(t, &deviceAPI{}), "GET", "/oauth/device?code=ZZZZ-ZZZZ", nil).Body.String()
	mustContain(t, body, "unknown, already used or expired")
}

func TestOAuthDevice_LinkIsApproveOnlyWithTheCode(t *testing.T) {
	t.Parallel()
	f := &deviceAPI{linkAgent: "support"}
	mux := deviceMux(t, f)
	body := consentDo(mux, "GET", "/oauth/device?code=BCDF-GHJK", nil).Body.String()
	mustContain(t, body, "kiff-cli wants to act as", "support", "BCDF-GHJK", "the code your own terminal shows",
		`action="/oauth/device"`, `name="user_code" value="BCDF-GHJK"`, ">Allow<",
		`<span class="consent-card-name">support-card</span>`, "Over the Card: refused")
	if strings.Contains(body, "you go back to") {
		t.Error("the device page names a return address")
	}
	rec := consentDo(mux, "POST", "/oauth/device", url.Values{"user_code": {"BCDF-GHJK"}, "action": {"approve"}})
	if !strings.Contains(rec.Body.String(), "Connected") || f.sent["user_code"] != "BCDF-GHJK" {
		t.Fatalf("approve: %s sent=%v", rec.Body.String(), f.sent)
	}
}

func TestOAuthDevice_GenericNewAgentAndDeny(t *testing.T) {
	t.Parallel()
	f := &deviceAPI{}
	mux := deviceMux(t, f)
	body := consentDo(mux, "GET", "/oauth/device?code=BCDF-GHJK", nil).Body.String()
	mustContain(t, body, `value="new" checked`, `name="new_name" value="kiff-cli"`)
	consentDo(mux, "POST", "/oauth/device", url.Values{"user_code": {"BCDF-GHJK"}, "action": {"approve"}, "choice": {"new"}, "new_name": {"cli-agent"}})
	if f.sent["new_agent"] != "cli-agent" {
		t.Fatalf("new agent: %v", f.sent)
	}
	rec := consentDo(mux, "POST", "/oauth/device", url.Values{"user_code": {"BCDF-GHJK"}, "action": {"deny"}})
	if !f.denied || !strings.Contains(rec.Body.String(), "Declined") {
		t.Fatalf("deny: %v %s", f.denied, rec.Body.String())
	}
}
