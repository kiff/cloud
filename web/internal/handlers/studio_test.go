package handlers_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// studio_test.go covers the Studio MVP authoring surface
// (MVP-2). The api side's draft store is exercised through a
// fake apps/api that mirrors an earlier change's four routes;
// the web handler drives that fake through the real templ
// render path.

// studioFakeAPI is the test-time apps/api stand-in. It backs the
// draft routes with an in-process map so the tests can assert
// the round-trip property: every PUT lands on the same row a
// subsequent GET returns.
type studioFakeAPI struct {
	mu sync.Mutex

	// drafts keyed on tenant id. Only one tenant in the fake.
	draftYAML      string
	draftUpdatedAt int64
	activeYAML     string
	activeVersion  int

	// activateBlock, when true, makes activate return 422.
	activateBlock bool

	// candidatesJSON is the raw body served by GET
	// /v1/guard/candidates. Empty → an empty runtimes set.
	candidatesJSON string

	// domainYAML is the multi-domain store keyed by slug, for the
	// domain-aware Build tests: GET /v1/me/domains (list), GET
	// /v1/me/domains/{slug}/yaml (pre-fill), PUT /v1/me/domains/{slug}
	// (in-place save). nil → the tenant has no additional domains.
	domainYAML map[string]string

	// observed records every method+path the fake handled so
	// tests can assert flows.
	observed []string

	// roles for /v1/me.
	meRoles []string
}

func (f *studioFakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.observed = append(f.observed, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"subject":             "alice",
			"email":               "alice@example.com",
			"tenant_id":           "ten-test",
			"tenant_slug":         "alice-team",
			"tenant_display_name": "Alice's Team",
			"api_key":             false,
			"roles":               f.meRoles,
		})
	})
	mux.HandleFunc("GET /v1/me/domain", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.activeYAML == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Echo a minimal summary; the studio handler builds a
		// Blueprint from these fields when no draft exists.
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"domain":  "refund-flow",
			"entity":  "Order",
			"version": f.activeVersion,
			"events":  []string{"ORDER_REQUESTED"},
			"states":  []string{"REQUESTED", "REFUNDED"},
			"actions": []map[string]any{{
				"name":           "ISSUE_REFUND",
				"allowed_states": []string{"REQUESTED"},
				"risk":           "high",
				"approval":       "required",
				"executor":       "cloud.proxy",
			}},
		})
	})
	mux.HandleFunc("GET /v1/me/domain/draft", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.draftYAML == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"yaml":       f.draftYAML,
			"updated_at": f.draftUpdatedAt,
		})
	})
	mux.HandleFunc("PUT /v1/me/domain/draft", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.draftYAML = string(body)
		f.draftUpdatedAt = time.Now().Unix()
		f.mu.Unlock()
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"yaml":       string(body),
			"updated_at": time.Now().Unix(),
		})
	})
	mux.HandleFunc("DELETE /v1/me/domain/draft", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.draftYAML = ""
		f.draftUpdatedAt = 0
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	// PUT /v1/me/domain validates + activates a completed kiff.yaml in
	// one shot (the block-free completion path). An incomplete contract
	// (no real state machine — "states: []" or a leftover TODO) fails
	// validation with 422, mirroring the api's ParseAndBuild gate; a
	// complete one promotes to active.
	mux.HandleFunc("PUT /v1/me/domain", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		yaml := string(body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.Contains(yaml, "states: []") || strings.Contains(yaml, "TODO") {
			writeJSONForTest(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  "domain validation failed",
				"issues": []map[string]any{{"path": "states", "message": "a state machine is required before activation"}},
			})
			return
		}
		f.activeYAML = yaml
		f.activeVersion++
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"domain":  "refund-flow",
			"entity":  "Order",
			"version": f.activeVersion,
		})
	})
	mux.HandleFunc("POST /v1/me/domain/draft/activate", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.draftYAML == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.activateBlock {
			writeJSONForTest(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  "draft fails validation",
				"issues": []map[string]any{{"path": "actions[0]", "message": "executor key is required"}},
			})
			return
		}
		// Promote: copy draft to active, bump version, delete draft.
		f.activeYAML = f.draftYAML
		f.activeVersion++
		f.draftYAML = ""
		f.draftUpdatedAt = 0
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"domain":  "refund-flow",
			"entity":  "Order",
			"version": f.activeVersion,
		})
	})

	// Multi-domain routes for the domain-aware Build.
	mux.HandleFunc("GET /v1/me/domains", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		domains := []map[string]any{}
		for slug := range f.domainYAML {
			domains = append(domains, map[string]any{
				"name": slug, "entity": "Service", "version": 1,
				"status": "active", "action_count": 1,
			})
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"domains": domains})
	})
	mux.HandleFunc("GET /v1/me/domains/{slug}/yaml", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		yaml, ok := f.domainYAML[r.PathValue("slug")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = io.WriteString(w, yaml)
	})
	mux.HandleFunc("PUT /v1/me/domains/{slug}", func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		body, _ := io.ReadAll(r.Body)
		yaml := string(body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.domainYAML[slug]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(yaml, "states: []") || strings.Contains(yaml, "TODO") {
			writeJSONForTest(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  "domain validation failed",
				"issues": []map[string]any{{"path": "states", "message": "a state machine is required"}},
			})
			return
		}
		f.domainYAML[slug] = yaml
		writeJSONForTest(w, http.StatusOK, map[string]any{"domain": slug, "entity": "Service", "version": 2})
	})

	mux.HandleFunc("GET /v1/guard/candidates", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		body := f.candidatesJSON
		f.mu.Unlock()
		if strings.TrimSpace(body) == "" {
			body = `{"source":"kiff-guard runtime","runtimes":[]}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})

	t.Cleanup(srv.Close)
	return srv
}

// newStudioMux wires a session-gated mux pointing at the fake.
func newStudioMux(t *testing.T, srv *httptest.Server, mgr *session.Manager) http.Handler {
	t.Helper()
	cli, err := apiclient.NewSessionAware(srv.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("apiclient.NewSessionAware: %v", err)
	}
	h := handlers.NewStudioHandler(cli, mgr, handlers.StaticBearerSource(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	inner := http.NewServeMux()
	h.Register(inner)
	return handlers.RequireSession(mgr)(inner)
}

// TestStudio_AnonymousRedirectsToSignIn confirms the route
// inherits the dashboard mux's auth boundary.
func TestStudio_AnonymousRedirectsToSignIn(t *testing.T) {
	t.Parallel()
	api := &studioFakeAPI{}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newStudioMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/build", nil)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", rec.Code)
	}
}

// TestStudio_FirstVisitRendersPlaceholderDraft confirms the no-draft /
// no-active-domain case lands on the placeholder stage 1 default. strip
// is a bridge orientation header, not a ?stage view-filter: in the live
// path it emits no ?stage links, the legacy Detect/Map/Policy labels are
// gone, and a ?stage param has no effect on what renders — the full
// contract shows at the draft's real stage regardless.
func TestStudio_BridgeStrip_DoesNotFilterOrLink(t *testing.T) {
	t.Parallel()

	// A Stage 3 draft (entity + states + actions with risk).
	fullDraft := `
domain: duplicate-payment
entity: Invoice
events:
  - INVOICE_CREATED
  - INVOICE_PAID
states:
  - PENDING
  - PAID
transitions:
  - on: INVOICE_CREATED
    from: ""
    to: PENDING
  - on: INVOICE_PAID
    from: PENDING
    to: PAID
actions:
  - name: PAY_INVOICE
    allowed_states: [PENDING]
    required_parameters: [amount_cents]
    required_permissions: [duplicate-payment.pay_invoice]
    risk: high
    approval: never
    executor: cloud.proxy
permissions:
  roles:
    tenant_owner:
      - duplicate-payment.pay_invoice
`

	api := &studioFakeAPI{draftYAML: strings.TrimSpace(fullDraft), draftUpdatedAt: 1}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newStudioMux(t, srv, mgr)

	get := func(stage string) string {
		path := "/dashboard/build"
		if stage != "" {
			path += "?stage=" + stage
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	// Default render: bridge labels present, full contract (risk) shown,
	// legacy ladder labels gone, no ?stage links emitted.
	def := get("")
	for _, want := range []string{"Derive", "Scaffold", "Review", "risk"} {
		if !strings.Contains(def, want) {
			t.Errorf("default render missing %q", want)
		}
	}
	for _, gone := range []string{"Detect", `href="/dashboard/build?stage=`} {
		if strings.Contains(def, gone) {
			t.Errorf("default render should not contain %q (strip no longer filters/links)", gone)
		}
	}

	// ?stage=1 must have no effect — the risk fields still render.
	if stage1 := get("1"); !strings.Contains(stage1, "risk") {
		t.Error("?stage=1 should not filter the view — risk must still show")
	}

	// Navigation never mutates the draft.
	api.mu.Lock()
	defer api.mu.Unlock()
	for _, o := range api.observed {
		if strings.HasPrefix(o, "PUT ") {
			t.Errorf("strip navigation must not issue a PUT; observed %s", o)
		}
	}
}
