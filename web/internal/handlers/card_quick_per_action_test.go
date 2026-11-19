package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// Reviews on: a quick update of an existing Card changes its daily
// limit and reissues every grant unchanged: each state scope and each
// per-action limit, including a second, tighter grant for the same
// action.
func TestQuickReissueKeepsEveryGrant(t *testing.T) {
	for _, grants := range [][]apiclient.MandateGrant{
		{{Action: "REFUND_ORDER", EntityStates: []string{"PAID"},
			PerActionMax: []apiclient.MandatePerAction{{Quantity: "sum(amount_cents)", Max: 50000}}}},
		{
			{Action: "REFUND_ORDER", EntityStates: []string{"DISPUTED"}, PerActionMax: []apiclient.MandatePerAction{{Quantity: "sum(amount_cents)", Max: 50000}}},
			{Action: "REFUND_ORDER", EntityStates: []string{"PAID"}, PerActionMax: []apiclient.MandatePerAction{{Quantity: "sum(amount_cents)", Max: 10000}}},
		},
	} {
		quickReissueKeeps(t, grants)
	}
}

func quickReissueKeeps(t *testing.T, grants []apiclient.MandateGrant) {
	t.Helper()
	card := apiclient.Mandate{ID: "refund-agent-refunds", Subject: "refund-agent", Domain: "card-refund",
		Grants:     grants,
		Aggregates: []apiclient.MandateAggregate{{Quantity: "sum(amount_cents)", Limit: 100000, Window: "calendar_day"}}}
	posted := make(chan apiclient.IssueMandateRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/me/domains":
			_ = json.NewEncoder(w).Encode(map[string]any{"domains": []map[string]string{{"name": "card-refund"}}})
		case r.Method == "GET" && r.URL.Path == "/v1/me/mandates":
			_ = json.NewEncoder(w).Encode(map[string]any{"mandates": []apiclient.Mandate{card}})
		case r.Method == "POST" && r.URL.Path == "/v1/me/mandates":
			var req apiclient.IssueMandateRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			posted <- req
			_ = json.NewEncoder(w).Encode(map[string]string{"id": req.ID, "subject": req.Subject})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	api, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	mux := http.NewServeMux()
	handlers.NewDashboardHandler(api, mgr, handlers.StaticBearerSource(), nil).Register(mux)
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "b", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rec := postQuick(handlers.RequireSession(mgr)(mux), issue.Result().Cookies()[0], url.Values{"task": {"refunds"}, "subject": {"refund-agent"}, "limit": {"2000"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case got := <-posted:
		if got.ID != card.ID {
			t.Fatalf("expected the existing Card to be updated, got %q", got.ID)
		}
		if !reflect.DeepEqual(got.Grants, grants) {
			t.Fatalf("quick update changed the grants:\nbefore %+v\nafter  %+v", grants, got.Grants)
		}
	default:
		t.Fatal("no issue request")
	}
}
