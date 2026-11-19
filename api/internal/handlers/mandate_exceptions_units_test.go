package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// unitDomains serves a domain's YAML by its name, and fails for the
// names in down, as a domain read that errors.
type unitDomains struct {
	stubDomainSource
	yaml map[string]string
	down map[string]bool
}

func (s unitDomains) LoadByDomain(_ context.Context, tenantID, sel string) (domain.TenantDomain, error) {
	if s.down[sel] {
		return domain.TenantDomain{}, errors.New("domain read failed")
	}
	y, ok := s.yaml[sel]
	if !ok {
		return domain.TenantDomain{}, domain.ErrDomainNotFound
	}
	return domain.TenantDomain{TenantID: tenantID, YAML: []byte(y), Version: 1, DomainID: sel}, nil
}

// unitDomainYAML is a domain whose refund sums amount_cents in unit at
// scale (no unit when unit is empty).
func unitDomainYAML(name, unit string, scale int) string {
	u := ""
	if unit != "" {
		u = "        unit: " + unit + "\n        scale: " + string(rune('0'+scale)) + "\n"
	}
	return `domain: ` + name + `
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
  - name: refund
    allowed_states: [requested]
    required_parameters: [amount_cents]
    parameters:
      - name: amount_cents
        type: int
        min: 0
` + u + `    required_permissions: [tools.refund]
    risk: medium
    approval: never
    executor: cloud.proxy
permissions:
  roles:
    gateway_agent: [tools.refund]
`
}

type unitBreach struct {
	MandateID   string `json:"mandate_id"`
	Quantity    string `json:"quantity"`
	Unit        string `json:"unit"`
	Scale       *int   `json:"scale"`
	Unavailable bool   `json:"unit_unavailable"`
	Ambiguous   bool   `json:"unit_ambiguous"`
}

type unitView struct {
	ID               string                     `json:"id"`
	Breaches         []unitBreach               `json:"breaches"`
	ParameterUnits   map[string]json.RawMessage `json:"parameter_units"`
	UnitsUnavailable bool                       `json:"units_unavailable"`
}

// unitViews lists the holds and reads each one's detail: the two must
// agree, whatever order the list is in.
func unitViews(t *testing.T, mux *http.ServeMux) map[string]unitView {
	t.Helper()
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions", nil)
	var list struct {
		Exceptions []unitView `json:"exceptions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("list: %v %s", err, rr.Body.String())
	}
	out := map[string]unitView{}
	for _, v := range list.Exceptions {
		rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions/"+v.ID, nil)
		var one unitView
		if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil {
			t.Fatalf("detail %s: %v %s", v.ID, err, rr.Body.String())
		}
		a, _ := json.Marshal(v)
		b, _ := json.Marshal(one)
		if string(a) != string(b) {
			t.Errorf("%s: list %s, detail %s", v.ID, a, b)
		}
		out[v.ID] = v
	}
	return out
}

// The person answering a hold sees what its amounts mean.
func TestExceptionBreachesCarryTheAmountUnit(t *testing.T) {
	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	sum := mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}
	count := mandate.Quantity{Kind: mandate.KindCount}
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return at })
	// Unscoped: the Card covers every domain.
	if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-any", TenantID: "t1", Subject: "claude",
		Grants: []mandate.Grant{{Action: "refund"}},
		Aggregates: []mandate.Aggregate{
			{Quantity: sum, Limit: 2000, Window: mandate.WindowCalendarDay},
			{Quantity: count, Limit: 10, Window: mandate.WindowCalendarDay},
		}}, "tester"); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	hold := func(id, dom string, bs ...mandate.Breach) {
		t.Helper()
		if err := store.SaveException(context.Background(), mandate.Exception{
			ID: id, TenantID: "t1", Subject: "claude", ProposalID: "tc-" + id, Action: "refund", CreatedAt: at,
			Request: mandate.Request{Domain: dom, Parameters: map[string]int64{"amount_cents": 2550}}, Status: mandate.ExceptionHeld,
			Breaches: bs,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	over := mandate.Breach{MandateID: "mdt-any", Outcome: mandate.OutcomeLimitReached, Quantity: sum, Limit: 2000, Used: 1800, Draw: 2550}
	full := mandate.Breach{MandateID: "mdt-any", Outcome: mandate.OutcomeLimitReached, Quantity: count, Limit: 10, Used: 10, Draw: 1}
	gone := mandate.Breach{MandateID: "mdt-gone", Outcome: mandate.OutcomeLimitReached, Quantity: sum, Limit: 100, Draw: 2550}
	hold("exc-eur", "shop-eu", over, full, gone)
	hold("exc-usd", "shop-us", over)
	hold("exc-count", "shop-us", full) // held only by its call count
	hold("exc-bare", "legacy", over)
	hold("exc-down", "flaky", over)

	h := NewMandatesHandler(store, unitDomains{
		yaml: map[string]string{
			"shop-eu": unitDomainYAML("shop-eu", "EUR", 2),
			"shop-us": unitDomainYAML("shop-us", "USD", 2),
			"legacy":  unitDomainYAML("legacy", "", 0),
		},
		down: map[string]bool{"flaky": true},
	}, WithMandatesClock(func() time.Time { return at }))
	mux := http.NewServeMux()
	h.Register(mux)
	got := unitViews(t, mux)

	unit := func(id string, i int, want string) {
		t.Helper()
		b := got[id].Breaches[i]
		if b.Unit != want || (want != "" && (b.Scale == nil || *b.Scale != 2)) || b.Unavailable {
			t.Errorf("%s breach %d = %+v, want unit %q", id, i, b, want)
		}
	}
	// The same unscoped Card reads in each hold's own domain's unit.
	unit("exc-eur", 0, "EUR")
	unit("exc-usd", 0, "USD")
	// A count has no unit.
	unit("exc-eur", 1, "")
	// A Card that cannot be read is said, not guessed.
	if b := got["exc-eur"].Breaches[2]; !b.Unavailable || b.Unit != "" {
		t.Errorf("unknown Card breach = %+v, want unit_unavailable", b)
	}
	// A hold only its count caused still says what its amount means.
	if c := got["exc-count"]; string(c.ParameterUnits["amount_cents"]) != `{"scale":2,"unit":"USD"}` || c.UnitsUnavailable {
		t.Errorf("count-only hold = %+v, want amount_cents in USD", c)
	}
	// A domain that declares no unit: a confirmed bare number.
	if b := got["exc-bare"]; b.Breaches[0].Unit != "" || b.Breaches[0].Unavailable || b.UnitsUnavailable || b.ParameterUnits != nil {
		t.Errorf("unitless domain = %+v, want bare", b)
	}
	// A domain that cannot be read: unavailable, never bare.
	if b := got["exc-down"]; !b.Breaches[0].Unavailable || !b.UnitsUnavailable || b.Breaches[0].Unit != "" {
		t.Errorf("failed domain read = %+v, want unit_unavailable and units_unavailable", b)
	}
	if strings.Contains(strings.ToLower(got["exc-usd"].Breaches[0].Unit), "eur") {
		t.Error("the USD hold took the EUR domain's unit")
	}
	// A unitless domain's breach is bare, not ambiguous.
	if b := got["exc-bare"].Breaches[0]; b.Ambiguous {
		t.Errorf("unitless breach = %+v, want bare", b)
	}
}

// a review at 504802c5: a Card granting two actions whose
// amount_cents mean different things (USD, EUR) has a total in no one
// unit. Its breach says so (unit_ambiguous), never bare, so it is not
// read as euros; the held action keeps its own USD.
func TestExceptionBreachOnAMixedUnitCardIsAmbiguous(t *testing.T) {
	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	sum := mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return at })
	if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-mixed", TenantID: "t1", Subject: "claude", Domain: "shop",
		Grants:     []mandate.Grant{{Action: "refund_us"}, {Action: "refund_eu"}},
		Aggregates: []mandate.Aggregate{{Quantity: sum, Limit: 2000, Window: mandate.WindowCalendarDay}}}, "tester"); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	if err := store.SaveException(context.Background(), mandate.Exception{
		ID: "exc-mixed", TenantID: "t1", Subject: "claude", ProposalID: "tc-mixed", Action: "refund_us", CreatedAt: at,
		Request: mandate.Request{Domain: "shop", Parameters: map[string]int64{"amount_cents": 2550}}, Status: mandate.ExceptionHeld,
		Breaches: []mandate.Breach{{MandateID: "mdt-mixed", Outcome: mandate.OutcomeLimitReached, Quantity: sum, Limit: 2000, Used: 1800, Draw: 2550}},
	}); err != nil {
		t.Fatalf("seed exception: %v", err)
	}
	action := func(name, unit string) string {
		return `  - name: ` + name + `
    allowed_states: [requested]
    required_parameters: [amount_cents]
    parameters:
      - name: amount_cents
        type: int
        min: 0
        unit: ` + unit + `
        scale: 2
    required_permissions: [tools.refund]
    risk: medium
    approval: never
    executor: cloud.proxy
`
	}
	yaml := `domain: shop
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
` + action("refund_us", "USD") + action("refund_eu", "EUR") + `permissions:
  roles:
    gateway_agent: [tools.refund]
`
	h := NewMandatesHandler(store, unitDomains{yaml: map[string]string{"shop": yaml}}, WithMandatesClock(func() time.Time { return at }))
	mux := http.NewServeMux()
	h.Register(mux)
	v := unitViews(t, mux)["exc-mixed"]
	if b := v.Breaches[0]; !b.Ambiguous || b.Unit != "" || b.Unavailable {
		t.Errorf("mixed-unit Card breach = %+v, want unit_ambiguous and no unit", b)
	}
	if string(v.ParameterUnits["amount_cents"]) != `{"scale":2,"unit":"USD"}` || v.UnitsUnavailable {
		t.Errorf("held refund_us = %+v, want its amount in USD", v)
	}
}

// A held call says its tool's title, in the list and alone, so the owner
// reads "Refund a payment", not the tool's KIFF name.
func TestHeldCallsSayTheToolsTitle(t *testing.T) {
	at := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return at })
	if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-any", TenantID: "t1", Subject: "claude",
		Grants: []mandate.Grant{{Action: "refund"}}}, "tester"); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	if err := store.SaveException(context.Background(), mandate.Exception{
		ID: "exc-1", TenantID: "t1", Subject: "claude", ProposalID: "tc-1", Action: "refund", CreatedAt: at,
		Request: mandate.Request{Parameters: map[string]int64{"amount_cents": 2550}}, Status: mandate.ExceptionHeld,
	}); err != nil {
		t.Fatalf("seed hold: %v", err)
	}
	h := NewMandatesHandler(store, stubDomainSource{yaml: refundCentsYAML}, WithMandatesClock(func() time.Time { return at }),
		WithMandatesToolTitles(fakeTitles{"refund": "Refund a payment"}))
	mux := http.NewServeMux()
	h.Register(mux)
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions", nil)
	var list struct{ Exceptions []map[string]any }
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Exceptions) != 1 || list.Exceptions[0]["title"] != "Refund a payment" {
		t.Fatalf("the list: %s", rr.Body.String())
	}
	rr = do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions/exc-1", nil)
	var one map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &one)
	if one["title"] != "Refund a payment" {
		t.Fatalf("the detail: %s", rr.Body.String())
	}
}
