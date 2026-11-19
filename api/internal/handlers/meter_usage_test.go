package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// fakeMeter answers with fixed totals and tasks.
type fakeMeter struct {
	tasks, actions int
	byAgent        map[string]int
	free           map[string]int
	freeByAgent    map[string]map[string]int // nil: the tasks' free cases are no agent's
	tafs           []string
	agents         map[string]bool // the account's agents, for ?agent=
	list           []meter.Task
	asked          [2]time.Time
	byPerson       []meter.PersonCount
	// taskScope is the scope the last Tasks read was asked with.
	taskScope *meter.TaskScope
}

func (f *fakeMeter) ByPerson(_ context.Context, _ string, from, to time.Time) ([]meter.PersonCount, error) {
	f.asked = [2]time.Time{from, to}
	return f.byPerson, nil
}

func (f *fakeMeter) Totals(_ context.Context, _ string, from, to time.Time) (map[meter.Unit]meter.Totals, error) {
	f.asked = [2]time.Time{from, to}
	free := func() map[string]int {
		m := map[string]int{}
		for _, k := range meter.FreeKinds {
			m[k] = f.free[k]
		}
		return m
	}
	return map[meter.Unit]meter.Totals{
		meter.UnitTask:   {Counted: f.tasks, ByAgent: f.byAgent, Free: free(), FreeByAgent: f.freeByAgent},
		meter.UnitAction: {Counted: f.actions, ByAgent: map[string]int{}, Free: free()},
	}, nil
}

func (f *fakeMeter) Tafs(context.Context, string) ([]string, error) { return f.tafs, nil }

func (f *fakeMeter) IsAgent(_ context.Context, _, id string) (bool, error) { return f.agents[id], nil }

func (f *fakeMeter) Tasks(_ context.Context, _, agent string, scope *meter.TaskScope, _, _ time.Time, page *meter.TaskPage, limit int) ([]meter.Task, *meter.TaskPage, error) {
	f.taskScope = scope
	if agent != "" {
		var mine []meter.Task
		for _, t := range f.list {
			if t.Agent == agent {
				mine = append(mine, t)
			}
		}
		return mine, nil, nil
	}
	start := 0
	if page != nil {
		for i, t := range f.list {
			if t.Record == page.Key {
				start = i + 1
			}
		}
	}
	end := min(start+limit, len(f.list))
	var next *meter.TaskPage
	if end < len(f.list) {
		next = &meter.TaskPage{At: f.list[end-1].At, Key: f.list[end-1].Record}
	}
	return f.list[start:end], next, nil
}

type usageRig struct {
	t       *testing.T
	tenants *tenant.MemoryStore
	meter   *fakeMeter
	mux     *http.ServeMux
	id      string
	now     time.Time
	names   *agents.MemoryStore
	catalog packs.Catalog
}

func newUsageRig(t *testing.T, meta map[string]any) *usageRig {
	t.Helper()
	r := &usageRig{t: t, tenants: tenant.NewMemoryStore(), meter: &fakeMeter{byAgent: map[string]int{}, free: map[string]int{}},
		mux: http.NewServeMux(), id: tenant.NewID(), now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	if err := r.tenants.Create(context.Background(), tenant.Tenant{ID: r.id, Slug: "acme", Status: tenant.StatusActive}); err != nil {
		t.Fatal(err)
	}
	for k, v := range meta {
		if err := r.tenants.SetMetadata(context.Background(), r.id, k, v); err != nil {
			t.Fatal(err)
		}
	}
	r.names = agents.NewMemoryStore()
	NewMeterUsageHandler(r.tenants, r.meter, slog.Default()).WithClock(func() time.Time { return r.now }).WithAgentNames(r.names).Register(r.mux)
	return r
}

func (r *usageRig) admin() auth.Identity {
	return auth.Identity{Subject: "alice", Tenant: r.id, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}
}

func (r *usageRig) get(path string) map[string]any {
	r.t.Helper()
	rr := serve(r.mux, r.admin(), http.MethodGet, path, "", nil)
	if rr.Code != http.StatusOK {
		r.t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func num(v any) int { f, _ := v.(float64); return int(f) }

// The trial's period and its actions; its tasks count nothing, and it
// reads as not started: there's no trial any more.
func TestUsageReadOnTheTrial(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialEndsAt: "2026-10-25T09:12:00Z"})
	r.meter.tasks, r.meter.actions = 14, 40
	r.meter.byAgent = map[string]int{"taf-refunds": 9, "taf-chargebacks": 5}
	r.meter.free = map[string]int{"refused": 3, "read": 120, "trial": 4}
	out := r.get("/v1/me/usage")

	if out["plan"] != "trial" || num(out["seats"]) != 1 {
		t.Fatalf("plan: %v, seats %v", out["plan"], out["seats"])
	}
	p := out["period"].(map[string]any)
	if p["start"] != "2026-10-18T09:12:00Z" || p["end"] != "2026-10-25T09:12:00Z" {
		t.Fatalf("the trial's 7 days: %v", p)
	}
	// Tasks are counted no more, and a trial reads as not started.
	tasks := out["tasks"].(map[string]any)
	if num(tasks["counted"]) != 0 || num(tasks["included"]) != 0 || tasks["left"] != nil || tasks["ceiling_reached"] != false || len(tasks["by_agent"].([]any)) != 0 {
		t.Fatalf("tasks: %v", tasks)
	}
	actions := out["actions"].(map[string]any)
	if num(actions["counted"]) != 40 || num(actions["included"]) != 500 || actions["ceiling"] != nil {
		t.Fatalf("actions have no ceiling: %v", actions)
	}
	trial := out["trial"].(map[string]any)
	if trial["ends_at"] != nil || num(trial["days_left"]) != 0 || num(trial["tasks_left"]) != 0 || num(trial["actions_left"]) != 0 {
		t.Fatalf("trial: %v", trial)
	}
}

// Team (Company) pools 500 actions a seat, from one seat, and counts no
// tasks; off the trial there is no trial object. Solo has no actions.
func TestUsageReadOnTeamPoolsPerPerson(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(3)})
	out := r.get("/v1/me/usage")
	if num(out["seats"]) != 3 || num(out["tasks"].(map[string]any)["included"]) != 0 || num(out["actions"].(map[string]any)["included"]) != 1500 || out["trial"] != nil {
		t.Fatalf("team of 3: %v", out)
	}
	// The month, when Stripe's period isn't known.
	if p := out["period"].(map[string]any); p["start"] != "2026-10-01T00:00:00Z" || p["end"] != "2026-11-01T00:00:00Z" {
		t.Fatalf("period: %v", p)
	}
	one := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(1)}).get("/v1/me/usage")
	if num(one["seats"]) != 1 || num(one["actions"].(map[string]any)["included"]) != 500 {
		t.Fatalf("team of 1: %v", one)
	}
	solo := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "solo"}).get("/v1/me/usage")
	if num(solo["actions"].(map[string]any)["included"]) != 0 {
		t.Fatalf("solo's actions: %v", solo["actions"])
	}
}

// The ceiling is retired: setting one is gone, whoever asks.
func TestTaskCeilingRetired(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "solo"})
	rr := serve(r.mux, r.admin(), http.MethodPut, "/v1/me/usage/ceiling", `{"tasks": 40}`, nil)
	if rr.Code != http.StatusGone || !strings.Contains(rr.Body.String(), "add a pack") {
		t.Fatalf("ceiling: %d %s", rr.Code, rr.Body.String())
	}
	if got := r.get("/v1/me/usage")["tasks"].(map[string]any); num(got["ceiling"]) != 0 || got["left"] != nil {
		t.Fatalf("read: %v", got)
	}
}

// From the app: the reads are a phone's session's, unsigned; the retired
// ceiling isn't among a phone's routes any more.
func TestUsageFromThePhone(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "solo"})
	policed := auth.DefaultRoutePolicy().Wrap(r.mux)
	p := newPhoneRig(t)
	phone := p.on(r.admin())
	for _, path := range []string{"/v1/me/usage", "/v1/me/usage/tasks"} {
		if rr := serve(policed, phone, http.MethodGet, path, "", nil); rr.Code != http.StatusOK {
			t.Errorf("GET %s from the phone: %d %s", path, rr.Code, rr.Body.String())
		}
	}
	body := `{"tasks": 40}`
	sig := map[string]string{auth.DeviceSignatureHeader: p.signedRequest(r.id, http.MethodPut, "/v1/me/usage/ceiling", body, time.Now())}
	if rr := serve(policed, phone, http.MethodPut, "/v1/me/usage/ceiling", body, sig); rr.Code != http.StatusForbidden {
		t.Fatalf("signed, from the phone: %d %s", rr.Code, rr.Body.String())
	}
}

// Each counted task by its Record id, newest first, a page at a time.
func TestUsageTasksPages(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "solo"})
	at := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	for i := 0; i < tasksPerPage+1; i++ {
		r.meter.list = append(r.meter.list, meter.Task{Record: "rec_" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Agent: "taf-refunds", Tool: "refund_order", At: at.Add(-time.Duration(i) * time.Minute)})
	}
	out := r.get("/v1/me/usage/tasks")
	if items := out["items"].([]any); len(items) != tasksPerPage || items[0].(map[string]any)["record"] != "rec_aa" || out["next"] == nil {
		t.Fatalf("first page: %d items, next %v", len(items), out["next"])
	}
	out = r.get("/v1/me/usage/tasks?cursor=" + out["next"].(string))
	if items := out["items"].([]any); len(items) != 1 || out["next"] != nil {
		t.Fatalf("second page: %v", out)
	}
	if rr := serve(r.mux, r.admin(), http.MethodGet, "/v1/me/usage/tasks?cursor=nope", "", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("a bad cursor: %d", rr.Code)
	}
}

// Tasks are counted no more: whatever the meter holds, the read's tasks
// name no taf and no free case.
func TestUsageReadPerTaf(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "solo"})
	r.meter.tasks = 9
	r.meter.byAgent = map[string]int{"taf-refunds": 9}
	r.meter.free = map[string]int{"refused": 1, "read": 86, "setup": 6}
	r.meter.tafs = []string{"taf-chargebacks", "taf-refunds"}
	tasks := r.get("/v1/me/usage")["tasks"].(map[string]any)
	if len(tasks["by_agent"].([]any)) != 0 || len(tasks["free"].(map[string]any)) != 0 || num(tasks["counted"]) != 0 {
		t.Fatalf("tasks: %v", tasks)
	}
}

// One taf's counted tasks (?agent=), each named the way the Record names
// its tool; an agent that isn't the account's is not found.
func TestUsageTasksPerTafWithTitles(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "solo"})
	mux := http.NewServeMux()
	NewMeterUsageHandler(r.tenants, r.meter, slog.Default()).WithToolTitles(fakeTitles{"refund_order": "Refund an order"}).
		WithClock(func() time.Time { return r.now }).Register(mux)
	r.mux = mux
	at := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	r.meter.list = []meter.Task{
		{Record: "tc-1", Agent: "taf-refunds", Tool: "refund_order", At: at},
		{Record: "tc-2", Agent: "taf-chargebacks", Tool: "close_dispute", At: at.Add(-time.Minute)},
		// Recorded with its title: it stands, whatever the gateway has now.
		{Record: "tc-3", Agent: "taf-chargebacks", Tool: "gone_tool", ToolTitle: "Close a dispute", At: at.Add(-2 * time.Minute)},
	}
	r.meter.agents = map[string]bool{"taf-refunds": true, "taf-chargebacks": true}
	items := r.get("/v1/me/usage/tasks?agent=taf-refunds")["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("one taf's: %v", items)
	}
	if it := items[0].(map[string]any); it["record"] != "tc-1" || it["tool"] != "refund_order" || it["tool_title"] != "Refund an order" {
		t.Fatalf("the task: %v", it)
	}
	// A tool without a title has an empty one; the app shows the tool.
	all := r.get("/v1/me/usage/tasks")["items"].([]any)
	if len(all) != 3 || all[1].(map[string]any)["tool_title"] != "" || all[2].(map[string]any)["tool_title"] != "Close a dispute" {
		t.Fatalf("every taf's: %v", all)
	}
	if rr := serve(r.mux, r.admin(), http.MethodGet, "/v1/me/usage/tasks?agent=someone-else", "", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("another account's agent: %d %s", rr.Code, rr.Body.String())
	}
}

// The owner's go-on past the AI fair-use bound (kiff/tafs#193): signed
// from the phone, for this period only, and in the usage read.
func TestAIGoOn(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "solo"})
	if got := r.get("/v1/me/usage")["ai"].(map[string]any); num(got["go_ons"]) != 0 {
		t.Fatalf("before: %v", got)
	}
	policed := auth.DefaultRoutePolicy().Wrap(r.mux)
	p := newPhoneRig(t)
	phone := p.on(r.admin())
	body := `{"go_on": true}`
	if rr := serve(policed, phone, http.MethodPut, "/v1/me/usage/ai", body, nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "signature_required") {
		t.Fatalf("unsigned: %d %s", rr.Code, rr.Body.String())
	}
	// Each signed tap adds one; a replay adds nothing.
	for i, at := range []time.Time{time.Now(), time.Now().Add(time.Second)} {
		sig := map[string]string{auth.DeviceSignatureHeader: p.signedRequest(r.id, http.MethodPut, "/v1/me/usage/ai", body, at)}
		if rr := serve(policed, phone, http.MethodPut, "/v1/me/usage/ai", body, sig); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), fmt.Sprintf(`"go_ons":%d`, i+1)) {
			t.Fatalf("tap %d: %d %s", i+1, rr.Code, rr.Body.String())
		}
		if i == 1 {
			if rr := serve(policed, phone, http.MethodPut, "/v1/me/usage/ai", body, sig); rr.Code != http.StatusConflict {
				t.Fatalf("a replay: %d %s", rr.Code, rr.Body.String())
			}
		}
	}
	if got := r.get("/v1/me/usage")["ai"].(map[string]any); num(got["go_ons"]) != 2 {
		t.Fatalf("after two taps: %v", got)
	}
	// The next period starts at 0.
	r.now = r.now.AddDate(0, 1, 0)
	if got := r.get("/v1/me/usage")["ai"].(map[string]any); num(got["go_ons"]) != 0 {
		t.Fatalf("next period: %v", got)
	}
	if rr := serve(r.mux, r.admin(), http.MethodPut, "/v1/me/usage/ai", `{"go_on": false}`, nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("go_on false: %d", rr.Code)
	}
}

// Usage by person and group: the billing admin reads each
// person's and each group's counts and what no person's run made; anyone
// else reads only their own; an agent's key reads none.
func TestUsageByPersonAndGroup(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "team"})
	r.meter.byPerson = []meter.PersonCount{{Subject: "ana", Tasks: 9, Actions: 30}, {Subject: "luis", Tasks: 4, Actions: 2}, {Subject: "", Tasks: 6, Actions: 1}}
	groups := access.NewMemoryStore()
	ctx := context.Background()
	support := access.Group{TenantID: r.id, ID: "grp_support", Name: "Support", CreatedAt: r.now, CreatedBy: "alice"}
	if err := groups.CreateGroup(ctx, support); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"ana", "luis"} {
		if err := groups.SetMember(ctx, access.GroupMember{TenantID: r.id, Group: support.ID, Subject: who, Role: access.Member, AddedAt: r.now, AddedBy: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	NewMeterUsageHandler(r.tenants, r.meter, slog.Default()).WithClock(func() time.Time { return r.now }).WithGroups(groups).Register(mux)

	rr := serve(mux, r.admin(), http.MethodGet, "/v1/me/usage/people", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", rr.Code, rr.Body.String())
	}
	var out peopleResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.People) != 2 || out.People[0] != (personUsage{Subject: "ana", Tasks: 9, Actions: 30}) || out.NoPerson == nil || out.NoPerson.Tasks != 6 {
		t.Fatalf("admin read %+v", out)
	}
	if len(out.Groups) != 1 || out.Groups[0] != (groupUsage{ID: support.ID, Name: "Support", People: 2, Tasks: 13, Actions: 32}) {
		t.Fatalf("groups %+v", out.Groups)
	}

	luis := auth.Identity{Subject: "luis", Tenant: r.id, MemberRole: membership.RoleEditor, Roles: membership.IdentityRoles(membership.RoleEditor)}
	rr = serve(mux, luis, http.MethodGet, "/v1/me/usage/people", "", nil)
	out = peopleResponse{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("editor: %d %s", rr.Code, rr.Body.String())
	}
	if len(out.People) != 1 || out.People[0].Subject != "luis" || out.NoPerson != nil || out.Groups != nil {
		t.Fatalf("an editor reads only their own: %+v", out)
	}

	agent := auth.Identity{Subject: "k", Tenant: r.id, APIKey: true, AgentID: "taf-refunds"}
	if rr := serve(mux, agent, http.MethodGet, "/v1/me/usage/people", "", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("an agent's key: %d", rr.Code)
	}
}

type usagePeopleStub []membership.Member

func (s usagePeopleStub) List(context.Context, string) ([]membership.Member, error) { return s, nil }

// The billing admin's usage by person names everyone on the account, those
// whose tafs got nothing done at 0, never anyone removed; and, while the
// period started before KIFF recorded who asked, says since when.
func TestUsageByPersonListsEveryone(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "team"})
	r.meter.byPerson = []meter.PersonCount{{Subject: "ana", Tasks: 9, Actions: 30}, {Subject: "", Tasks: 13}}
	people := usagePeopleStub{
		{TenantID: r.id, Subject: "ana", Status: membership.StatusActive},
		{TenantID: r.id, Subject: "rosa", Status: membership.StatusActive},
		{TenantID: r.id, Subject: "sam", Status: membership.StatusSuspended},
		{TenantID: r.id, Subject: "pat", Status: membership.StatusRemoved},
	}
	read := func(now time.Time) peopleResponse {
		t.Helper()
		mux := http.NewServeMux()
		NewMeterUsageHandler(r.tenants, r.meter, slog.Default()).WithClock(func() time.Time { return now }).WithPeople(people).Register(mux)
		rr := serve(mux, r.admin(), http.MethodGet, "/v1/me/usage/people", "", nil)
		var out peopleResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || rr.Code != http.StatusOK {
			t.Fatalf("admin: %d %s", rr.Code, rr.Body.String())
		}
		return out
	}
	out := read(PeopleCountedSince.Add(24 * time.Hour))
	want := []personUsage{{Subject: "ana", Tasks: 9, Actions: 30}, {Subject: "rosa"}, {Subject: "sam"}}
	if len(out.People) != len(want) {
		t.Fatalf("people %+v", out.People)
	}
	for i := range want {
		if out.People[i] != want[i] {
			t.Fatalf("people %+v", out.People)
		}
	}
	if out.PeopleSince == nil || !out.PeopleSince.Equal(PeopleCountedSince) {
		t.Fatalf("a period that began before people were recorded says since when: %+v", out.PeopleSince)
	}
	if out := read(PeopleCountedSince.AddDate(0, 2, 0)); out.PeopleSince != nil {
		t.Fatalf("a later period says nothing: %+v", out.PeopleSince)
	}
}

// An account that moved from App Store Solo to Team: Stripe
// bills Team, and the read says Solo still renews with Apple until its date,
// with Apple's page to cancel it. Once that date passes, nothing is said.
func TestUsageSaysAppleSoloStillRenews(t *testing.T) {
	until := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeyBillingSource: "stripe",
		appstore.MetaExpiresAt: until.Format(time.RFC3339)})
	b := r.get("/v1/me/usage")["billing"].(map[string]any)
	if b["source"] != "stripe" || b["apple_solo_until"] != until.Format(time.RFC3339) || b["apple_manage_url"] != AppleManageURL {
		t.Fatalf("billing: %v", b)
	}
	// Only the billing admin is told: anyone else can't cancel it.
	editor := auth.Identity{Subject: "luis", Tenant: r.id, MemberRole: membership.RoleEditor, Roles: membership.IdentityRoles(membership.RoleEditor)}
	rr := serve(r.mux, editor, http.MethodGet, "/v1/me/usage", "", nil)
	var asEditor map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &asEditor); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("editor: %d %s", rr.Code, rr.Body.String())
	}
	if b := asEditor["billing"].(map[string]any); b["apple_solo_until"] != nil {
		t.Fatalf("an editor is told: %v", b)
	}
	// Once its renewal is off with Apple, it no longer renews: nothing to say.
	if err := r.tenants.SetMetadata(context.Background(), r.id, appstore.MetaAutoRenew, "off"); err != nil {
		t.Fatal(err)
	}
	if b := r.get("/v1/me/usage")["billing"].(map[string]any); b["apple_solo_until"] != nil {
		t.Fatalf("renewal off: %v", b)
	}
	if err := r.tenants.SetMetadata(context.Background(), r.id, appstore.MetaAutoRenew, "on"); err != nil {
		t.Fatal(err)
	}
	r.now = until.Add(time.Hour)
	if b := r.get("/v1/me/usage")["billing"].(map[string]any); b["apple_solo_until"] != nil || b["apple_manage_url"] != nil {
		t.Fatalf("after Solo's date: %v", b)
	}
}

// Usage names each counted task's agent by its record: Coach reads
// "Coach", not its id.
func TestUsageNamesItsAgents(t *testing.T) {
	r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "solo"})
	ctx := context.Background()
	for id, name := range map[string]string{"setup": "Coach", "taf-refunds": "Refunds 4", "taf-x": ""} {
		if err := r.names.Ensure(ctx, agents.Agent{TenantID: r.id, ID: id, CreatedAt: r.now, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	r.meter.list = []meter.Task{{Record: "rec_a", Agent: "setup", Tool: "task_start", At: r.now.Add(-time.Minute)}}
	items := r.get("/v1/me/usage/tasks")["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["agent_name"] != "Coach" {
		t.Fatalf("a counted task's agent: %v", items)
	}
}
