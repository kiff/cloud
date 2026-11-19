package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

func prefilled(t *testing.T, card apiclient.Mandate) pages.CardNewView {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"mandates": []apiclient.Mandate{card}})
	}))
	t.Cleanup(srv.Close)
	api, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := NewDashboardHandler(api, nil, StaticBearerSource(), nil)
	var view pages.CardNewView
	h.prefillFromCard(httptest.NewRequest("GET", "/cards/new?card="+card.ID, nil), session.Session{Bearer: "b"}, card.ID, &view)
	if view.Editing != card.ID {
		t.Fatalf("prefill failed: %+v", view)
	}
	return view
}

func capOn(q string, max int64) []apiclient.MandatePerAction {
	return []apiclient.MandatePerAction{{Quantity: q, Max: max}}
}

// Reviews on: editing only the ceiling must reissue exactly the grants
// the Card has. Any merge or flattening can loosen a limit: with REFUND
// granted at 10000 in PAID and 50000 in DISPUTED, flattening to one cap
// lets a 20000 PAID refund through that was refused before.
func TestCeilingEditReissuesTheGrantsExactly(t *testing.T) {
	cases := []struct {
		name   string
		grants []apiclient.MandateGrant
	}{
		{"different caps per action", []apiclient.MandateGrant{
			{Action: "AUTO_REFUND", PerActionMax: capOn("sum(amount)", 50000)},
			{Action: "ISSUE_CREDIT", PerActionMax: capOn("sum(amount)", 10000)},
		}},
		{"several parameters on one action", []apiclient.MandateGrant{
			{Action: "AUTO_REFUND", PerActionMax: []apiclient.MandatePerAction{{Quantity: "sum(amount)", Max: 50000}, {Quantity: "sum(items)", Max: 5}}},
		}},
		{"a parameter other than the ceiling's", []apiclient.MandateGrant{
			{Action: "AUTO_REFUND", PerActionMax: capOn("sum(fee)", 500)},
		}},
		{"one action capped, one not", []apiclient.MandateGrant{
			{Action: "AUTO_REFUND", PerActionMax: capOn("sum(amount)", 50000)},
			{Action: "ISSUE_CREDIT"},
		}},
		{"repeated action, tighter first", []apiclient.MandateGrant{
			{Action: "AUTO_REFUND", EntityStates: []string{"PAID"}, PerActionMax: capOn("sum(amount)", 10000)},
			{Action: "AUTO_REFUND", EntityStates: []string{"DISPUTED"}, PerActionMax: capOn("sum(amount)", 50000)},
		}},
		{"repeated action, tighter second", []apiclient.MandateGrant{
			{Action: "AUTO_REFUND", EntityStates: []string{"DISPUTED"}, PerActionMax: capOn("sum(amount)", 50000)},
			{Action: "AUTO_REFUND", EntityStates: []string{"PAID"}, PerActionMax: capOn("sum(amount)", 10000)},
		}},
		{"repeated action, same cap, different states", []apiclient.MandateGrant{
			{Action: "AUTO_REFUND", EntityStates: []string{"PAID"}, PerActionMax: capOn("sum(amount)", 50000)},
			{Action: "AUTO_REFUND", EntityStates: []string{"DISPUTED"}, PerActionMax: capOn("sum(amount)", 50000)},
		}},
		{"repeated action without caps", []apiclient.MandateGrant{
			{Action: "AUTO_REFUND", EntityStates: []string{"PAID"}},
			{Action: "AUTO_REFUND", EntityStates: []string{"DISPUTED"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := apiclient.Mandate{ID: "card", Subject: "agent", Domain: "refund-control", Grants: tc.grants,
				Aggregates: []apiclient.MandateAggregate{{Quantity: "sum(amount)", Limit: 500000, Window: "calendar_day"}}}
			view := prefilled(t, card)
			if view.Form.PerActionMax != "" || view.Form.PreservedGrants == "" || len(view.ActionsForForm()) != 0 {
				t.Fatalf("grants the form cannot represent must be preserved and shown read-only: %+v", view.Form)
			}
			// The form posts no actions of its own for a preserved Card.
			view.Form.Actions = nil
			view.Form.States = nil
			view.Form.Limit = "600000"
			got, err := buildIssueMandateRequest(view.Form, view.SelectedDomain)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Grants, tc.grants) {
				t.Fatalf("grants changed by a ceiling edit:\nbefore %+v\nafter  %+v", tc.grants, got.Grants)
			}
			if got.Aggregates[0].Limit != 600000 {
				t.Fatalf("the ceiling edit itself was lost: %+v", got.Aggregates)
			}

			both := view.Form
			both.PerActionMax = "100"
			if _, err := buildIssueMandateRequest(both, view.SelectedDomain); err == nil {
				t.Error("setting the per-action field on top of preserved grants must be refused")
			}
		})
	}
}

// A Card the form can say exactly is still edited through it.
func TestUniformPerActionStaysEditable(t *testing.T) {
	card := apiclient.Mandate{ID: "card", Subject: "agent", Domain: "refund-control",
		Grants: []apiclient.MandateGrant{
			{Action: "AUTO_REFUND", PerActionMax: capOn("sum(amount)", 50000)},
			{Action: "ISSUE_CREDIT", PerActionMax: capOn("sum(amount)", 50000)},
		},
		Aggregates: []apiclient.MandateAggregate{{Quantity: "sum(amount)", Limit: 500000, Window: "calendar_day"}}}
	view := prefilled(t, card)
	if view.Form.PerActionMax != "50000" || view.Form.PreservedGrants != "" {
		t.Fatalf("uniform caps should prefill the field: %+v", view.Form)
	}
	view.Form.PerActionMax = "20000"
	got, err := buildIssueMandateRequest(view.Form, view.SelectedDomain)
	if err != nil || got.Grants[0].PerActionMax[0].Max != 20000 || got.Grants[1].PerActionMax[0].Max != 20000 {
		t.Fatalf("edit: %+v %v", got.Grants, err)
	}
}
