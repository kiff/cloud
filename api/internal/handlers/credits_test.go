package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// fakeLedger is the run ledger as the handler sees it: it keeps each run's
// first entry, and remembers what each read was asked.
type fakeLedger struct {
	tafs  []string
	spent int64
	// setupSpent and noPlanSpent are what the allowance reads of Setup's
	// runs: the period's, and those while the account had no plan.
	setupSpent, noPlanSpent int64
	period                  struct{ from, to time.Time }
	kept                    map[string]meter.RunEntry
	totals                  meter.RunTotals
	runs                    []meter.RunEntry
	next                    *meter.RunPage
	asked                   struct{ from, to time.Time }
	// totalsAsked are the periods totals were read for, in turn: the read's
	// own, then the balance's, which is always the current one.
	totalsAsked [][2]time.Time
	scope       *meter.TaskScope
	runtime     string
	page        *meter.RunPage
}

func (f *fakeLedger) KeepRun(_ context.Context, r meter.RunReport, from, to time.Time) (meter.RunEntry, bool, error) {
	f.period.from, f.period.to = from, to
	if f.kept == nil {
		f.kept = map[string]meter.RunEntry{}
	}
	if k, ok := f.kept[r.Run]; ok {
		return k, false, nil
	}
	e := meter.ClassifyRun(r, f.spent)
	f.kept[r.Run] = e
	return e, true, nil
}

func (f *fakeLedger) RunTotals(_ context.Context, _ string, from, to time.Time) (meter.RunTotals, error) {
	f.asked.from, f.asked.to = from, to
	f.totalsAsked = append(f.totalsAsked, [2]time.Time{from, to})
	return f.totals, nil
}

func (f *fakeLedger) Runs(_ context.Context, _ string, runtime string, scope *meter.TaskScope, from, to time.Time, page *meter.RunPage, _ int) ([]meter.RunEntry, *meter.RunPage, error) {
	f.asked.from, f.asked.to, f.scope, f.runtime, f.page = from, to, scope, runtime, page
	return f.runs, f.next, nil
}

func (f *fakeLedger) Tafs(context.Context, string) ([]string, error) { return f.tafs, nil }

func (f *fakeLedger) SetupSpent(context.Context, string, time.Time, time.Time) (int64, error) {
	return f.setupSpent, nil
}

func (f *fakeLedger) NoPlanSetupSpent(context.Context, string) (int64, error) {
	return f.noPlanSpent, nil
}

type creditsRig struct {
	t       *testing.T
	ledger  *fakeLedger
	tenants *tenant.MemoryStore
	packs   *packs.Memory
	alerts  *alertsSeen
	mux     *http.ServeMux
	id      string
	now     time.Time
}

// newCreditsRig is an account on Solo.
func newCreditsRig(t *testing.T) *creditsRig {
	return newCreditsRigOn(t, map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple"})
}

// newCreditsRigOn is an account with meta: its plan, seats and limit.
func newCreditsRigOn(t *testing.T, meta map[string]any) *creditsRig {
	t.Helper()
	r := &creditsRig{t: t, ledger: &fakeLedger{tafs: []string{"agt-refunds"}}, tenants: tenant.NewMemoryStore(), packs: packs.NewMemory(),
		alerts: &alertsSeen{}, mux: http.NewServeMux(), id: tenant.NewID(), now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	if err := r.tenants.Create(context.Background(), tenant.Tenant{ID: r.id, Slug: "acme", Status: tenant.StatusActive, Metadata: meta}); err != nil {
		t.Fatal(err)
	}
	names := agents.NewMemoryStore()
	if err := names.Create(context.Background(), agents.Agent{TenantID: r.id, ID: "agt-refunds", Kind: agents.KindTaf, Name: "Refunds"}); err != nil {
		t.Fatal(err)
	}
	catalog, err := packs.ParseCatalog([]byte(`{"solo": {"apple_product": "bot.tafs.solo.monthly"}, "packs": [
		{"id": "c-small", "name": "Small", "credits": 100, "apple_product": "bot.tafs.c.small"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	NewCreditsHandler(r.tenants, r.ledger, slog.Default()).WithClock(func() time.Time { return r.now }).WithAgentNames(names).
		WithCreditPacks(r.packs).WithCatalog(catalog).WithRequests(r.packs, r.alerts).Register(r.mux)
	return r
}

func (r *creditsRig) taf(agent string) auth.Identity {
	return auth.Identity{Subject: "k", Tenant: r.id, APIKey: true, AgentID: agent, Method: auth.MethodAPIKey, Roles: []string{auth.RoleGatewayAgent}}
}

func (r *creditsRig) setup() auth.Identity {
	return auth.Identity{Subject: "k2", Tenant: r.id, APIKey: true, AgentID: "agt-coach", Method: auth.MethodAPIKey, Roles: []string{auth.RoleSetupReader}}
}

func (r *creditsRig) admin() auth.Identity {
	return auth.Identity{Subject: "alice", Tenant: r.id, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}
}

const aRun = `{"agent":"taf-3f9a1c0b7d2e4a6f8b91","trigger":"owner","requester":{"kind":"member","id":"ada"},
	"started":"2026-11-07T08:00:00Z","ended":"2026-11-07T08:00:41Z","cost_micro_usd":30000,"turns":4,
	"status":"COMPLETED","stop":"answered","failed":false,"refused":1,"kept_until":"2026-11-14T08:00:41Z"}`

// A taf's runtime reports its run with the taf's key: kept once, with its
// credits, whose run it was, and the agents it names; sent again, it
// changes nothing and answers the same.
func TestATafsRunIsKeptWithItsCredits(t *testing.T) {
	r := newCreditsRig(t)
	for i := 0; i < 2; i++ {
		if rr := serve(r.mux, r.taf("agt-refunds"), http.MethodPut, "/v1/me/runs/4f0c2a9e/usage", aRun, nil); rr.Code != http.StatusNoContent {
			t.Fatalf("report %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	e := r.ledger.kept["4f0c2a9e"]
	if len(r.ledger.kept) != 1 || e.Tenant != r.id || e.Agent != "agt-refunds" || e.RuntimeAgent != "taf-3f9a1c0b7d2e4a6f8b91" || e.Setup ||
		e.Trigger != "owner" || e.RequesterKind != "member" || e.AskedBy != "ada" || e.CostMicroUSD != 30_000 || e.Turns != 4 ||
		e.Status != "COMPLETED" || e.Refused != 1 || !e.Counted || e.Credits != 12 || e.KeptUntil == nil {
		t.Fatalf("kept %+v", e)
	}
	// A door's sender isn't a person of the account: the run is no one's.
	door := strings.Replace(aRun, `"kind":"member","id":"ada"`, `"kind":"door","id":"+34600000000"`, 1)
	if rr := serve(r.mux, r.taf("agt-refunds"), http.MethodPut, "/v1/me/runs/r-door/usage", door, nil); rr.Code != http.StatusNoContent ||
		r.ledger.kept["r-door"].AskedBy != "" || r.ledger.kept["r-door"].RequesterKind != "door" {
		t.Fatalf("a door's run: %d, %+v", rr.Code, r.ledger.kept["r-door"])
	}
}

// Setup's key reports Coach's runs, free within its fair use and counted
// past it, whatever its agent's kind.
func TestCoachsRunsAreFreeWithinItsFairUse(t *testing.T) {
	r := newCreditsRig(t)
	if rr := serve(r.mux, r.setup(), http.MethodPut, "/v1/me/runs/c1/usage", aRun, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("Coach's run: %d %s", rr.Code, rr.Body.String())
	}
	if e := r.ledger.kept["c1"]; !e.Setup || e.Counted || e.Free != meter.FreeSetup {
		t.Fatalf("within its fair use: %+v", e)
	}
	r.ledger.spent = meter.SetupAllowanceMicroUSD
	if rr := serve(r.mux, r.setup(), http.MethodPut, "/v1/me/runs/c2/usage", aRun, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("Coach's run: %d %s", rr.Code, rr.Body.String())
	}
	if e := r.ledger.kept["c2"]; !e.Setup || !e.Counted || e.Credits != 12 {
		t.Fatalf("past its fair use: %+v", e)
	}
}

// Only an agent's key reports, only a taf's or Setup's, and only a report
// that says what a run did.
func TestARunReportIsRefusedWhenItIsntOne(t *testing.T) {
	r := newCreditsRig(t)
	for _, c := range []struct {
		name string
		id   auth.Identity
		path string
		body string
		code int
	}{
		{"a person", r.admin(), "/v1/me/runs/r1/usage", aRun, http.StatusForbidden},
		{"an account key", auth.Identity{Subject: "acct", Tenant: r.id, APIKey: true, Roles: []string{"admin"}}, "/v1/me/runs/r1/usage", aRun, http.StatusForbidden},
		{"one of the account's own agents", r.taf("agt-own"), "/v1/me/runs/r1/usage", aRun, http.StatusForbidden},
		{"a run id with a slash's worth of junk", r.taf("agt-refunds"), "/v1/me/runs/r%20x/usage", aRun, http.StatusBadRequest},
		{"a run id too long", r.taf("agt-refunds"), "/v1/me/runs/" + strings.Repeat("a", 129) + "/usage", aRun, http.StatusBadRequest},
		{"not JSON", r.taf("agt-refunds"), "/v1/me/runs/r1/usage", `{`, http.StatusBadRequest},
		{"no agent", r.taf("agt-refunds"), "/v1/me/runs/r1/usage", strings.Replace(aRun, `"agent":"taf-3f9a1c0b7d2e4a6f8b91",`, "", 1), http.StatusBadRequest},
		{"an end before its start", r.taf("agt-refunds"), "/v1/me/runs/r1/usage", strings.Replace(aRun, `"ended":"2026-11-07T08:00:41Z"`, `"ended":"2026-11-07T07:00:00Z"`, 1), http.StatusBadRequest},
		{"no end", r.taf("agt-refunds"), "/v1/me/runs/r1/usage", strings.Replace(aRun, `"ended":"2026-11-07T08:00:41Z",`, "", 1), http.StatusBadRequest},
		{"a negative cost", r.taf("agt-refunds"), "/v1/me/runs/r1/usage", strings.Replace(aRun, `30000`, `-1`, 1), http.StatusBadRequest},
		{"a run still at work", r.taf("agt-refunds"), "/v1/me/runs/r1/usage", strings.Replace(aRun, `"COMPLETED"`, `"RUNNING"`, 1), http.StatusBadRequest},
		{"a word too long", r.taf("agt-refunds"), "/v1/me/runs/r1/usage", strings.Replace(aRun, `"trigger":"owner"`, `"trigger":"`+strings.Repeat("x", 257)+`"`, 1), http.StatusBadRequest},
	} {
		if rr := serve(r.mux, c.id, http.MethodPut, c.path, c.body, nil); rr.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, rr.Code, rr.Body.String(), c.code)
		}
	}
	if len(r.ledger.kept) != 0 {
		t.Fatalf("kept %v", r.ledger.kept)
	}
	// A person is told only an agent's key reports, before anything is read.
	if rr := serve(r.mux, r.admin(), http.MethodPut, "/v1/me/runs/r1/usage", aRun, nil); !strings.Contains(rr.Body.String(), "an agent's key reports") {
		t.Fatalf("a person: %d %s", rr.Code, rr.Body.String())
	}
}

// The period's credits: the current one, the one before, or the one
// starting at a time; each agent named; the free runs by why, each present.
func TestThePeriodsCredits(t *testing.T) {
	r := newCreditsRig(t)
	r.ledger.totals = meter.RunTotals{Credits: 135, Runs: 4, Setup: meter.AgentRuns{Credits: 46, Runs: 1},
		ByAgent: []meter.AgentRuns{{Agent: "agt-refunds", RuntimeAgent: "taf-3f9a1c0b7d2e4a6f8b91", Credits: 66, Runs: 2}, {Agent: "agt-gone", RuntimeAgent: "taf-gone", Credits: 23, Runs: 1}},
		Free:    map[string]int{meter.FreeTrial: 3}}
	get := func(path string) creditsResponse {
		t.Helper()
		rr := serve(r.mux, r.admin(), http.MethodGet, path, "", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
		}
		var out creditsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	cur := get("/v1/me/usage/credits")
	month := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) // an account with no plan has the calendar month
	if !cur.Current || !cur.Period.Start.Equal(month) || !cur.Period.End.Equal(month.AddDate(0, 1, 0)) || cur.Credits != 135 || cur.Runs != 4 ||
		cur.Setup.Credits != 46 || len(cur.ByAgent) != 2 || cur.ByAgent[0].Name != "Refunds" || cur.ByAgent[0].RuntimeAgent != "taf-3f9a1c0b7d2e4a6f8b91" ||
		cur.ByAgent[1].Name != "" || cur.Free[meter.FreeTrial] != 3 || len(cur.Free) != 3 {
		t.Fatalf("this period: %+v", cur)
	}
	if prev := get("/v1/me/usage/credits?period=previous"); prev.Current || !prev.Period.Start.Equal(month.AddDate(0, -1, 0)) || !prev.Period.End.Equal(month) {
		t.Fatalf("the period before: %+v", prev.Period)
	}
	// The current period is read once, its totals the balance's too; the
	// period before is read, then the current one for the balance.
	if a := r.ledger.totalsAsked; len(a) != 3 || a[0] != [2]time.Time{month, month.AddDate(0, 1, 0)} ||
		a[1] != [2]time.Time{month.AddDate(0, -1, 0), month} || a[2] != a[0] {
		t.Fatalf("the ledger was asked %v", a)
	}
	if at := get("/v1/me/usage/credits?period=2026-09-01T00:00:00Z"); at.Current || !at.Period.Start.Equal(month.AddDate(0, -2, 0)) {
		t.Fatalf("September: %+v", at.Period)
	}
	if rr := serve(r.mux, r.admin(), http.MethodGet, "/v1/me/usage/credits?period=last-week", "", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("a period that isn't one: %d", rr.Code)
	}
}

// The period's runs, newest first, one agent's when asked, a page at a
// time, read as the tasks are; whose run it was shows only as the
// reader's own.
func TestThePeriodsRunsPages(t *testing.T) {
	r := newCreditsRig(t)
	ended := time.Date(2026, 11, 7, 8, 0, 41, 0, time.UTC)
	r.ledger.runs = []meter.RunEntry{
		{RunReport: meter.RunReport{Run: "r2", Agent: "agt-refunds", RuntimeAgent: "taf-1", Trigger: "owner", RequesterKind: "member", AskedBy: "alice", Ended: ended, Refused: 1}, Counted: true, Credits: 6},
		{RunReport: meter.RunReport{Run: "r1", Agent: "agt-refunds", RuntimeAgent: "taf-1", Trigger: "owner", RequesterKind: "member", AskedBy: "ada", Ended: ended.Add(-time.Hour)}, Free: meter.FreeTrial},
	}
	r.ledger.next = &meter.RunPage{At: ended.Add(-time.Hour), Run: "r1"}
	rr := serve(r.mux, r.admin(), http.MethodGet, "/v1/me/usage/credits/runs?runtime_agent=taf-1", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("runs: %d %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Items []creditsRun `json:"items"`
		Next  string       `json:"next"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if r.ledger.runtime != "taf-1" || r.ledger.scope != nil || len(out.Items) != 2 || !out.Items[0].Mine || out.Items[1].Mine ||
		out.Items[0].Credits != 6 || out.Items[0].Refused != 1 || out.Items[1].Free != meter.FreeTrial || out.Next == "" {
		t.Fatalf("runs %+v, next %q, asked for %q with %+v", out.Items, out.Next, r.ledger.runtime, r.ledger.scope)
	}
	if strings.Contains(rr.Body.String(), `"ada"`) {
		t.Fatalf("another person's subject is shown: %s", rr.Body.String())
	}
	// The next page goes on from where this one stopped.
	if rr := serve(r.mux, r.admin(), http.MethodGet, "/v1/me/usage/credits/runs?cursor="+out.Next, "", nil); rr.Code != http.StatusOK ||
		r.ledger.page == nil || r.ledger.page.Run != "r1" || !r.ledger.page.At.Equal(ended.Add(-time.Hour)) {
		t.Fatalf("the next page: %d, from %+v", rr.Code, r.ledger.page)
	}
	if rr := serve(r.mux, r.admin(), http.MethodGet, "/v1/me/usage/credits/runs?cursor=nope", "", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("a cursor that isn't one: %d", rr.Code)
	}
}

// From the app, through tafs cloud: both reads are a phone session's,
// unsigned, as the tasks are.
func TestCreditsFromThePhone(t *testing.T) {
	r := newCreditsRig(t)
	policed := auth.DefaultRoutePolicy().Wrap(r.mux)
	phone := newPhoneRig(t).on(r.admin())
	for _, path := range []string{"/v1/me/usage/credits", "/v1/me/usage/credits/runs"} {
		if rr := serve(policed, phone, http.MethodGet, path, "", nil); rr.Code != http.StatusOK {
			t.Errorf("GET %s from the phone: %d %s", path, rr.Code, rr.Body.String())
		}
	}
}

// Coach's fair use is the period the run ended in, never the one its report
// arrived in: a run that ended just before the period rolled over, reported
// after, is measured against the old one, which its row is filed under.
func TestCoachsFairUseIsThePeriodItsRunEndedIn(t *testing.T) {
	r := newCreditsRig(t)
	r.now = time.Date(2026, 12, 1, 0, 30, 0, 0, time.UTC)
	late := strings.NewReplacer(`"started":"2026-11-07T08:00:00Z"`, `"started":"2026-11-30T23:50:00Z"`,
		`"ended":"2026-11-07T08:00:41Z"`, `"ended":"2026-11-30T23:58:00Z"`).Replace(aRun)
	if rr := serve(r.mux, r.setup(), http.MethodPut, "/v1/me/runs/c-late/usage", late, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("Coach's late report: %d %s", rr.Code, rr.Body.String())
	}
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !r.ledger.period.from.Equal(nov) || !r.ledger.period.to.Equal(nov.AddDate(0, 1, 0)) {
		t.Fatalf("measured against [%v, %v), want November", r.ledger.period.from, r.ledger.period.to)
	}
}
