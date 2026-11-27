package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
)

// A held call reads in the owner's words from KIFF: what it
// is, its amount in its unit, why it's held in that unit, and when it stops
// waiting, on the list and on its own, so no app builds them.
func TestAHeldCallSaysItselfInTheOwnersWords(t *testing.T) {
	at := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	sum := mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return at })
	if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-r", TenantID: "t1", Subject: "claude",
		Grants:     []mandate.Grant{{Action: "refund", PerAction: []mandate.PerActionLimit{{Quantity: sum, Max: 4000}}}},
		Aggregates: []mandate.Aggregate{{Quantity: sum, Limit: 30000, Window: mandate.WindowCalendarDay}}}, "tester"); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	expires := at.Add(10 * time.Minute)
	hold := func(id, dom, entity string, cents int64, bs ...mandate.Breach) {
		t.Helper()
		if err := store.SaveException(context.Background(), mandate.Exception{
			ID: id, TenantID: "t1", Subject: "claude", ProposalID: "p-" + id, Action: "refund", EntityID: entity, CreatedAt: at, ExpiresAt: &expires,
			Request: mandate.Request{Domain: dom, Parameters: map[string]int64{"amount_cents": cents}}, Status: mandate.ExceptionHeld,
			Breaches: bs,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	hold("exc-each", "shop-us", "R-3005", 4500, mandate.Breach{MandateID: "mdt-r", Outcome: mandate.OutcomePerActionExceeded, Quantity: sum, Limit: 4000, Draw: 4500})
	hold("exc-total", "shop-us", "R-3008", 3500, mandate.Breach{MandateID: "mdt-r", Outcome: mandate.OutcomeLimitReached, Quantity: sum, Limit: 30000, Used: 28000, Draw: 3500})
	hold("exc-bare", "legacy", "R-1", 4500, mandate.Breach{MandateID: "mdt-r", Outcome: mandate.OutcomePerActionExceeded, Quantity: sum, Limit: 4000, Draw: 4500})

	h := NewMandatesHandler(store, unitDomains{yaml: map[string]string{
		"shop-us": unitDomainYAML("shop-us", "USD", 2),
		"legacy":  unitDomainYAML("legacy", "", 0),
	}}, WithMandatesClock(func() time.Time { return at }), WithMandatesToolTitles(fakeTitles{"refund": "Refund a payment"}))
	mux := http.NewServeMux()
	h.Register(mux)

	type words struct {
		ID         string   `json:"id"`
		Summary    string   `json:"summary"`
		AmountText string   `json:"amount_text"`
		ReasonText string   `json:"reason_text"`
		Details    []string `json:"details"`
		ExpiresAt  string   `json:"expires_at"`
		Amount     *struct {
			Minor int64   `json:"minor"`
			Unit  *string `json:"unit"`
			Scale int     `json:"scale"`
		} `json:"amount"`
	}
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions", nil)
	var list struct{ Exceptions []words }
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("list: %v %s", err, rr.Body.String())
	}
	got := map[string]words{}
	for _, v := range list.Exceptions {
		got[v.ID] = v
		rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions/"+v.ID, nil)
		var one words
		_ = json.Unmarshal(rr.Body.Bytes(), &one)
		if one.Summary != v.Summary || one.ReasonText != v.ReasonText || one.AmountText != v.AmountText {
			t.Errorf("%s: the list says %+v, the hold itself %+v", v.ID, v, one)
		}
	}
	each := got["exc-each"]
	if each.Summary != "Refund a payment R-3005" || each.AmountText != "$45" || each.ReasonText != "$45 is over the $40 limit for one call" {
		t.Errorf("over one call: %+v", each)
	}
	if each.Amount == nil || each.Amount.Minor != 4500 || each.Amount.Unit == nil || *each.Amount.Unit != "USD" || each.Amount.Scale != 2 {
		t.Errorf("amount: %+v", each.Amount)
	}
	if each.ExpiresAt == "" || !slices.Equal(each.Details, []string{}) {
		t.Errorf("expiry %q, details %q", each.ExpiresAt, each.Details)
	}
	if r := got["exc-total"].ReasonText; r != "$35 would go over the $300 total" {
		t.Errorf("over the total: %q", r)
	}
	// No unit declared: a bare number that says so, and KIFF's own reason.
	if b := got["exc-bare"]; b.AmountText != "4,500 (no currency)" || b.ReasonText == "" || b.ReasonText == "$45 is over the $40 limit for one call" {
		t.Errorf("bare: %+v", b)
	}
}

func TestHeldDetailsAndName(t *testing.T) {
	args := json.RawMessage(`{"charge":"ch_1","amount_cents":4500,"kiff_operation_id":"op","refund_all":true,"note":{"a":1}}`)
	if got := heldDetails(args, map[string]int64{"amount_cents": 4500}, nil); !slices.Equal(got, []string{"charge: ch_1", "note:", "  a: 1", "refund_all: true"}) {
		t.Errorf("details: %q", got)
	}
	for _, c := range []struct{ action, title, want string }{
		{"create_refund", "Refund a payment", "Refund a payment"},
		{"stripe_create_refund_repg", "Write Stripe API data", "Write Stripe API data · stripe create refund"},
		{"list_charges", "", "list charges"},
	} {
		if got := heldName(c.action, c.title, ""); got != c.want {
			t.Errorf("heldName(%q, %q) = %q, want %q", c.action, c.title, got, c.want)
		}
	}
}

// A held call's details are never cut: a long argument is written whole,
// and every argument is listed, however many there are. A held browser call's
// steps are what the owner signs (red-team, 27 Nov: a held browse_act showed
// "steps: …", and the owner signed a click they couldn't see).
func TestHeldDetailsAreNeverCut(t *testing.T) {
	body := strings.Repeat("é", 500)
	args := map[string]any{"body": body}
	for i := 0; i < 25; i++ {
		args[fmt.Sprintf("k%02d", i)] = "v"
	}
	raw, _ := json.Marshal(args)
	got := heldDetails(raw, nil, nil)
	if len(got) != 26 || got[0] != "body: "+body || got[25] != "k24: v" {
		t.Fatalf("lines: %d, first %d runes, last %q", len(got), len([]rune(got[0])), got[len(got)-1])
	}
	for _, l := range got {
		if strings.Contains(l, "…") || strings.Contains(l, " more") {
			t.Errorf("a line was cut: %q", l)
		}
	}
}

// KIFF's details are the lines tafs' contracts/held-arguments.json says every
// app writes, case by case (a copy in testdata, kept equal to tafs' own).
func TestHeldDetailsAreTheContractsLines(t *testing.T) {
	raw, err := os.ReadFile("testdata/held-arguments.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Cases []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Lines     []string        `json:"lines"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil || len(vector.Cases) == 0 {
		t.Fatalf("the vector: %v", err)
	}
	for _, c := range vector.Cases {
		if got := heldDetails(c.Arguments, nil, nil); !slices.Equal(got, c.Lines) {
			t.Errorf("%s:\n got %q\nwant %q", c.Name, got, c.Lines)
		}
	}
}

func TestPlainNumber(t *testing.T) {
	for in, want := range map[string]string{
		"50": "50", "1.5e-05": "0.000015", "1E+3": "1000", "1.50": "1.50", "0.5": "0.5", "-2.5e1": "-25",
		"9007199254740993": "9007199254740993", "0": "0", "100e-1": "10.0",
	} {
		if got := plainNumber(in); got != want {
			t.Errorf("plainNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

// Coach's held install says the version it installs, and every hold the page
// it's answered on.
func TestAHeldCallSaysItsInstallAndWhereItsAnswered(t *testing.T) {
	for _, c := range []struct {
		action, args string
		want         map[string]any
	}{
		{"version_install", `{"taf":"taf_rf","n":8}`, map[string]any{"taf": "taf_rf", "n": 8}},
		{"version_install", `{"taf":"taf_rf","n":"8"}`, map[string]any{"taf": "taf_rf", "n": 8}},
		{"version_install", `{"taf":"","n":8}`, nil},
		{"version_install", `{"taf":"taf_rf","n":0}`, nil},
		{"create_refund", `{"taf":"taf_rf","n":8}`, nil},
	} {
		got := heldInstall(c.action, json.RawMessage(c.args))
		if fmt.Sprint(got) != fmt.Sprint(c.want) || (got == nil) != (c.want == nil) {
			t.Errorf("%s %s: %v, want %v", c.action, c.args, got, c.want)
		}
	}
	v := withHeldWords(map[string]any{}, mandate.Exception{ID: "exc 1", Action: "refund"}, "https://app.kiff.dev", wordsIn(langEN))
	if v["answer_url"] != "https://app.kiff.dev/needs-you/exc%201" {
		t.Errorf("answer_url: %v", v["answer_url"])
	}
}

// With the vendor's own name for the tool, a relay's name for it is never
// guessed at: "logs_repg2" reads the vendor's words.
func TestHeldNameUsesTheVendorsToolName(t *testing.T) {
	for _, c := range []struct{ action, title, vendor, want string }{
		{"get_logs_repg2", "Read Acme data", "get_logs", "Read Acme data · get logs"},
		{"oauth2_token_x1y2", "", "oauth2_token", "oauth2 token"},
		{"create_refund_repg", "Refund a payment", "create_refund", "Refund a payment"},
	} {
		if got := heldName(c.action, c.title, c.vendor); got != c.want {
			t.Errorf("heldName(%q, %q, %q) = %q, want %q", c.action, c.title, c.vendor, got, c.want)
		}
	}
}

// A limit's quantity is data: its kind, and a sum's
// parameter.
func TestQuantityOf(t *testing.T) {
	if got := quantityOf(mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}); got["kind"] != "sum" || got["parameter"] != "amount_cents" {
		t.Errorf("sum: %v", got)
	}
	if got := quantityOf(mandate.Quantity{Kind: mandate.KindCount}); got["kind"] != "count" || got["parameter"] != nil {
		t.Errorf("count: %v", got)
	}
	lines := []map[string]any{{"quantity": "sum(amount)"}, {"quantity": "nonsense("}}
	withQuantityOf(lines)
	if lines[0]["quantity_of"] == nil || lines[1]["quantity_of"] != nil {
		t.Errorf("shown: %v", lines)
	}
}

// A call held by a count of calls says it as the Card does: a limit of 0
// means each call asks first; otherwise which call this would be. Never the
// machine's "count limit 0 per calendar_day reached".
func TestAHeldCountSaysItInTheCardsWords(t *testing.T) {
	count := func(limit, used int64, window string) map[string]any {
		b := map[string]any{"outcome": string(mandate.OutcomeLimitReached), "quantity": "count", "limit": limit, "used": used, "draw": int64(1),
			"reason": fmt.Sprintf("count limit %d per %s reached: %d used, this action draws 1", limit, window, used)}
		if window != "" {
			b["window"] = window
		}
		return b
	}
	e := mandate.Exception{Breaches: []mandate.Breach{{Outcome: mandate.OutcomeLimitReached}}}
	for _, c := range []struct {
		b    map[string]any
		want string
	}{
		{count(0, 0, "calendar_day"), "Each call asks you first"},
		{count(20, 20, "calendar_day"), "20 calls a day: this would be the 21st"},
		{count(1, 1, "rolling_1h"), "1 call in any hour: this would be the 2nd"},
		{count(12, 12, "lifetime"), "12 calls in total: this would be the 13th"},
		{count(5, 5, ""), "Over its limit of 5 calls: this would be the 6th"}, // a hold made before the window was kept
	} {
		if got := heldReason(e, []map[string]any{c.b}, heldMoney{}, false, wordsIn(langEN)); got != c.want {
			t.Errorf("%v: %q, want %q", c.b, got, c.want)
		}
	}
}

// A count hold read back from storage, and its breaches as JSON gives them
// (float64, json.Number), still says which call it would be: never "Each
// call asks you first" for a limit that isn't 0.
func TestAStoredCountHoldSaysWhichCall(t *testing.T) {
	at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return at })
	if err := store.Issue(context.Background(), mandate.Mandate{ID: "mdt-c", TenantID: "t1", Subject: "claude",
		Grants: []mandate.Grant{{Action: "refund"}}}, "tester"); err != nil {
		t.Fatal(err)
	}
	b := mandate.Breach{MandateID: "mdt-c", Outcome: mandate.OutcomeLimitReached, Quantity: mandate.Quantity{Kind: mandate.KindCount},
		Limit: 20, Used: 20, Draw: 1, Window: mandate.WindowCalendarDay, Reason: "count limit 20 per calendar_day reached: 20 used, this action draws 1"}
	if err := store.SaveException(context.Background(), mandate.Exception{ID: "exc-c", TenantID: "t1", Subject: "claude", ProposalID: "tc-c",
		Action: "refund", CreatedAt: at, Request: mandate.Request{Domain: "shop"}, Status: mandate.ExceptionHeld, Breaches: []mandate.Breach{b}}); err != nil {
		t.Fatal(err)
	}
	h := NewMandatesHandler(store, unitDomains{yaml: map[string]string{}}, WithMandatesClock(func() time.Time { return at }))
	mux := http.NewServeMux()
	h.Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/v1/me/card-exceptions/exc-c", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), owner()))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var v struct {
		ReasonText string           `json:"reason_text"`
		Breaches   []map[string]any `json:"breaches"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("%d %v %s", rr.Code, err, rr.Body.String())
	}
	if v.ReasonText != "20 calls a day: this would be the 21st" {
		t.Errorf("reason_text %q", v.ReasonText)
	}
	// The breach as JSON gives it back: float64, and json.Number.
	e := mandate.Exception{Breaches: []mandate.Breach{b}}
	if got := heldReason(e, v.Breaches, heldMoney{}, false, wordsIn(langEN)); got != "20 calls a day: this would be the 21st" {
		t.Errorf("float64 breach: %q", got)
	}
	dec := json.NewDecoder(strings.NewReader(`{"outcome":"limit_reached","quantity":"count","limit":20,"used":20,"draw":1,"window":"calendar_day"}`))
	dec.UseNumber()
	var n map[string]any
	if err := dec.Decode(&n); err != nil {
		t.Fatal(err)
	}
	if got := heldReason(e, []map[string]any{n}, heldMoney{}, false, wordsIn(langEN)); got != "20 calls a day: this would be the 21st" {
		t.Errorf("json.Number breach: %q", got)
	}
}
