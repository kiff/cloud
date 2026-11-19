package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// Reading another person's work goes through KIFF: only someone who
// oversees the agent gets a read grant, KIFF records the read with its
// purpose before signing it, the person whose work it is sees the read,
// and only the agent's own key resolves the grant.
func TestReadingAnothersWorkIsRecordedFirst(t *testing.T) {
	r := newGrantRig(t)
	mux := http.NewServeMux()
	(&handlers.Oversight{Grants: r.rg, On: true}).Register(mux)
	do := func(id auth.Identity, method, path string, body any) (int, map[string]any) {
		t.Helper()
		id.Tenant = r.tnt
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, id).ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	person := func(s string, role membership.Role) auth.Identity {
		return auth.Identity{Subject: s, Method: auth.MethodSession, MemberRole: role, Roles: membership.IdentityRoles(role)}
	}
	ada, ana, bo := person("ada", membership.RoleAdmin), person("ana", membership.RoleApprover), person("bo", membership.RoleApprover)
	reads := func(id auth.Identity, path string) []any {
		t.Helper()
		code, out := do(id, http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %v", path, code, out)
		}
		list, _ := out["reads"].([]any)
		return list
	}

	// Ana uses Refunds but oversees it not: no grant, and nothing recorded.
	if code, out := do(ana, http.MethodPost, "/v1/oversight/reads", map[string]string{"agent": "refund-agent", "owner": "bo", "purpose": "fix"}); code != http.StatusForbidden || out["need"] != "oversee" {
		t.Fatalf("Ana reading Bo's work: %d %v", code, out)
	}
	for name, body := range map[string]map[string]string{
		"no purpose":       {"agent": "refund-agent", "owner": "ana"},
		"another purpose":  {"agent": "refund-agent", "owner": "ana", "purpose": "performance"},
		"their own work":   {"agent": "refund-agent", "owner": "ada", "purpose": "fix"},
		"a long note":      {"agent": "refund-agent", "owner": "ana", "purpose": "fix", "note": strings.Repeat("a", 281)},
		"no one by the id": {"agent": "refund-agent", "owner": "zed", "purpose": "fix"},
		"no agent":         {"agent": "nope", "owner": "ana", "purpose": "fix"},
	} {
		if code, out := do(ada, http.MethodPost, "/v1/oversight/reads", body); code < 400 || code >= 500 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if got := reads(ana, "/v1/me/oversight-reads"); len(got) != 0 {
		t.Fatalf("a refused read was recorded: %v", got)
	}

	// Ada, an admin, oversees every agent: her read of Ana's chat is
	// recorded with its purpose, and Ana sees it.
	code, out := do(ada, http.MethodPost, "/v1/oversight/reads", map[string]string{"agent": "refund-agent", "owner": "ana", "session": "s1", "purpose": "fix", "note": "the refund to Ivo failed"})
	if code != http.StatusOK || out["grant"] == nil {
		t.Fatalf("Ada's read: %d %v", code, out)
	}
	grant := out["grant"].(string)
	mine := reads(ana, "/v1/me/oversight-reads")
	if len(mine) != 1 {
		t.Fatalf("Ana's reads: %v", mine)
	}
	if rd := mine[0].(map[string]any); rd["reader"] != "ada" || rd["agent"] != "refund-agent" || rd["session"] != "s1" || rd["purpose"] != "fix" || rd["note"] != "the refund to Ivo failed" {
		t.Errorf("the read Ana sees: %v", rd)
	}
	if got := reads(bo, "/v1/me/oversight-reads"); len(got) != 0 {
		t.Errorf("Bo sees a read of someone else's work: %v", got)
	}
	if got := reads(ada, "/v1/oversight/reads?agent=refund-agent"); len(got) != 1 {
		t.Errorf("the account's reads: %v", got)
	}
	if code, _ := do(bo, http.MethodGet, "/v1/oversight/reads", nil); code != http.StatusForbidden {
		t.Errorf("an approver read the account's reads: %d", code)
	}

	// The agent's own key resolves the grant; another agent's can't, and
	// a run grant isn't a read grant.
	recorded := mine[0].(map[string]any)["id"]
	if code, out := do(agentKey, http.MethodPost, "/v1/me/read-grants/resolve", map[string]string{"grant": grant}); code != http.StatusOK || out["reader"] != "ada" || out["owner"] != "ana" || out["session"] != "s1" || out["read"] != recorded {
		t.Errorf("resolved by the agent's key: %d %v", code, out)
	}
	other := auth.Identity{Subject: "key-payouts", Method: auth.MethodAPIKey, APIKey: true, AgentID: "payouts", Roles: []string{"gateway_agent"}}
	if code, out := do(other, http.MethodPost, "/v1/me/read-grants/resolve", map[string]string{"grant": grant}); code != http.StatusForbidden || out["error"] != "read_grant" {
		t.Errorf("resolved by another agent's key: %d %v", code, out)
	}
	if code, out := do(agentKey, http.MethodPost, "/v1/me/read-grants/resolve", map[string]string{"grant": r.grant(t, "refund-agent", "ana")}); code != http.StatusForbidden || out["error"] != "read_grant" {
		t.Errorf("a run grant resolved as a read grant: %d %v", code, out)
	}
	if code, _ := do(ada, http.MethodPost, "/v1/me/read-grants/resolve", map[string]string{"grant": grant}); code != http.StatusForbidden {
		t.Errorf("a person resolved a read grant: %d", code)
	}
}

// A manager reads only their own group's work: not another group's on the
// same agent, nor an admin's, nor someone granted the agent alone. Taken
// out of the group, they lose a read already granted, as they do when the
// person whose work it is leaves.
func TestAManagerReadsOnlyTheirGroupsWork(t *testing.T) {
	r := newGrantRig(t)
	ctx := context.Background()
	if err := r.members.Mutate(ctx, r.tnt, func(tx membership.Tx) error {
		for _, s := range []string{"maria", "luis", "sophie", "paul"} {
			if err := tx.InsertMember(membership.Member{TenantID: r.tnt, Subject: s, Role: membership.RoleApprover, Status: membership.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Support (Maria manages, Luis) and Finance (Sophie manages, Paul) both use Refunds;
	// Ana uses it on her own.
	for _, g := range []access.Group{{TenantID: r.tnt, ID: "support", Name: "Support"}, {TenantID: r.tnt, ID: "finance", Name: "Finance"}} {
		if err := r.grants.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		if err := r.grants.AddGrant(ctx, access.Grant{TenantID: r.tnt, Agent: "refund-agent", Group: g.ID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []access.GroupMember{
		{TenantID: r.tnt, Group: "support", Subject: "maria", Role: access.Manager}, {TenantID: r.tnt, Group: "support", Subject: "luis", Role: access.Member},
		{TenantID: r.tnt, Group: "finance", Subject: "sophie", Role: access.Manager}, {TenantID: r.tnt, Group: "finance", Subject: "paul", Role: access.Member},
	} {
		if err := r.grants.SetMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	(&handlers.Oversight{Grants: r.rg, On: true}).Register(mux)
	do := func(id auth.Identity, method, path string, body any) (int, map[string]any) {
		t.Helper()
		id.Tenant = r.tnt
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, id).ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	maria := auth.Identity{Subject: "maria", Method: auth.MethodSession, MemberRole: membership.RoleApprover}
	read := func(owner string) (int, map[string]any) {
		return do(maria, http.MethodPost, "/v1/oversight/reads", map[string]string{"agent": "refund-agent", "owner": owner, "purpose": "follow_up"})
	}
	for _, owner := range []string{"paul", "sophie", "ana", "ada"} {
		if code, out := read(owner); code != http.StatusForbidden || out["need"] != "oversee" {
			t.Errorf("Maria reading %s's work: %d %v", owner, code, out)
		}
	}
	if got, _ := r.grants.Reads(ctx, r.tnt, "", "", 0); len(got) != 0 {
		t.Fatalf("a refused read was recorded: %v", got)
	}
	code, out := read("luis")
	if code != http.StatusOK {
		t.Fatalf("Maria reading Luis's work: %d %v", code, out)
	}
	grant := out["grant"].(string)
	resolve := func() (int, map[string]any) {
		return do(agentKey, http.MethodPost, "/v1/me/read-grants/resolve", map[string]string{"grant": grant})
	}
	if code, out := resolve(); code != http.StatusOK || out["owner"] != "luis" {
		t.Fatalf("resolving Maria's read: %d %v", code, out)
	}
	// Taken out of Support, Maria loses the read she was granted.
	if err := r.grants.SetMember(ctx, access.GroupMember{TenantID: r.tnt, Group: "support", Subject: "maria", Role: access.Member}); err != nil {
		t.Fatal(err)
	}
	if code, out := resolve(); code != http.StatusForbidden || out["need"] != "oversee" {
		t.Errorf("Maria's read once she no longer manages Support: %d %v", code, out)
	}
}

// MayRead: an admin reads anyone's; a manager, their own group's on a
// group the agent is granted to; no one else, and no one inactive.
func TestMayRead(t *testing.T) {
	active := func(role membership.Role) *membership.Member {
		return &membership.Member{Subject: "x", Role: role, Status: membership.StatusActive}
	}
	manages := []access.GroupMember{{Group: "support", Role: access.Manager}, {Group: "ops", Role: access.Manager}}
	inSupport, inOps := []access.GroupMember{{Group: "support", Role: access.Member}}, []access.GroupMember{{Group: "ops", Role: access.Member}}
	grants := []access.Grant{{Group: "support"}, {Subject: "ana"}}
	for name, c := range map[string]struct {
		reader   *membership.Member
		readerIn []access.GroupMember
		ownerIn  []access.GroupMember
		want     bool
	}{
		"an admin":                         {active(membership.RoleAdmin), nil, nil, true},
		"the manager of their group":       {active(membership.RoleApprover), manages, inSupport, true},
		"a manager of a group not granted": {active(membership.RoleApprover), manages, inOps, false},
		"a member of the group":            {active(membership.RoleApprover), inSupport, inSupport, false},
		"someone granted the agent alone":  {active(membership.RoleApprover), manages, nil, false},
		"a suspended admin":                {&membership.Member{Role: membership.RoleAdmin, Status: membership.StatusSuspended}, nil, nil, false},
		"no one":                           {nil, manages, inSupport, false},
	} {
		if got := access.MayRead(c.reader, c.readerIn, c.ownerIn, grants); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// Reading another person's work is off on every account: an admin, the
// owner included, gets oversight_off and nothing is recorded, and a read
// grant already signed no longer resolves for the agent's runtime.
func TestNoOneReadsAnothersWork(t *testing.T) {
	r := newGrantRig(t)
	on, off := http.NewServeMux(), http.NewServeMux()
	(&handlers.Oversight{Grants: r.rg, On: true}).Register(on)
	(&handlers.Oversight{Grants: r.rg}).Register(off)
	do := func(mux *http.ServeMux, id auth.Identity, method, path string, body any) (int, map[string]any) {
		t.Helper()
		id.Tenant = r.tnt
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, id).ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	ada := auth.Identity{Subject: "ada", Method: auth.MethodSession, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}
	ana := auth.Identity{Subject: "ana", Method: auth.MethodSession, MemberRole: membership.RoleApprover, Roles: membership.IdentityRoles(membership.RoleApprover)}
	read := map[string]string{"agent": "refund-agent", "owner": "ana", "purpose": "fix"}

	// A grant signed while reads were possible, to show it stops resolving.
	code, out := do(on, ada, http.MethodPost, "/v1/oversight/reads", read)
	if code != http.StatusOK {
		t.Fatalf("a read with reads on: %d %v", code, out)
	}
	grant, _ := out["grant"].(string)

	if code, out := do(off, ada, http.MethodPost, "/v1/oversight/reads", read); code != http.StatusForbidden || out["error"] != "oversight_off" {
		t.Fatalf("an admin reading Ana's work: %d %v, want oversight_off", code, out)
	}
	_, mine := do(off, ana, http.MethodGet, "/v1/me/oversight-reads", nil)
	if list, _ := mine["reads"].([]any); len(list) != 1 {
		t.Fatalf("Ana's reads: %v, want only the one made while reads were on", mine["reads"])
	}
	if code, out := do(off, agentKey, http.MethodPost, "/v1/me/read-grants/resolve", map[string]string{"grant": grant}); code != http.StatusForbidden || out["error"] != "oversight_off" {
		t.Fatalf("resolving a grant signed before: %d %v, want oversight_off", code, out)
	}
}
