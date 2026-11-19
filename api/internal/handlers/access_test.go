package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// accessWorld is one account: Ada (admin), Ed (editor), Maria and Ana
// (approvers), Paul (viewer), and two tafs, Refunds, which Ed made, and
// Bills, which Ada made, behind the default route policy as a person's
// session reaches it.
type accessWorld struct {
	t       *testing.T
	h       http.Handler
	members membership.Store
	store   *access.MemoryStore
}

func newAccessWorld(t *testing.T) *accessWorld {
	t.Helper()
	ctx := context.Background()
	members := membership.NewMemoryStore()
	if err := members.Mutate(ctx, "t1", func(tx membership.Tx) error {
		for subject, role := range map[string]membership.Role{"ada": membership.RoleAdmin, "ed": membership.RoleEditor,
			"maria": membership.RoleApprover, "ana": membership.RoleApprover, "paul": membership.RoleViewer} {
			if err := tx.InsertMember(membership.Member{TenantID: "t1", Subject: subject, Role: role, Status: membership.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	agentStore := agents.NewMemoryStore()
	for id, by := range map[string]string{"refunds": "ed", "bills": "ada"} {
		if err := agentStore.Create(ctx, agents.Agent{TenantID: "t1", ID: id, Kind: agents.KindTaf, CreatedAt: time.Now(), CreatedBy: by}); err != nil {
			t.Fatal(err)
		}
	}
	store := access.NewMemoryStore()
	mux := http.NewServeMux()
	ah := handlers.NewAccessHandler(store, members, agentStore, nil)
	ah.Register(mux)
	// Archiving takes an agent's shares with it (RemoveAgentGrants).
	handlers.NewAgentsHandler(agentStore, handlers.WithAgentsAccess(store), handlers.WithAgentsUnshare(ah.RemoveAgentGrants)).Register(mux)
	return &accessWorld{t: t, h: auth.DefaultRoutePolicy().Wrap(mux), members: members, store: store}
}

func person(subject string, role membership.Role) auth.Identity {
	return auth.Identity{Subject: subject, Tenant: "t1", Method: auth.MethodSession, MemberRole: role}
}

func (w *accessWorld) do(id auth.Identity, method, path string, body any) (int, map[string]any) {
	w.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	withFakeIdentity(w.h, id).ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The answer for subject, as a runtime key asks it.
func (w *accessWorld) answer(subject string) map[string]any {
	w.t.Helper()
	key := auth.Identity{Subject: "key-1", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, AgentID: "refunds", Roles: []string{"gateway_agent"}}
	code, out := w.do(key, http.MethodPost, "/v1/agents/refunds/access", map[string]string{"subject": subject})
	if code != http.StatusOK {
		w.t.Fatalf("access for %s: %d %v", subject, code, out)
	}
	return out
}

// An admin makes Support with Maria as its manager; an
// editor shares Refunds with Support. Then Ana, a member, uses Refunds;
// Maria oversees it; Paul, in no group, may do nothing with it; Ed
// manages it without using it; and Ada oversees every agent.
func TestGroupsAndSharingDecideAccess(t *testing.T) {
	w := newAccessWorld(t)
	ada, ed := person("ada", membership.RoleAdmin), person("ed", membership.RoleEditor)
	maria := person("maria", membership.RoleApprover)

	if code, _ := w.do(ed, http.MethodPost, "/v1/groups", map[string]string{"name": "Support"}); code != http.StatusForbidden {
		t.Errorf("an editor made a group: %d", code)
	}
	code, g := w.do(ada, http.MethodPost, "/v1/groups", map[string]string{"name": "Support"})
	if code != http.StatusCreated {
		t.Fatalf("make Support: %d %v", code, g)
	}
	support := g["id"].(string)
	if code, _ := w.do(ada, http.MethodPost, "/v1/groups", map[string]string{"name": " support "}); code != http.StatusConflict {
		t.Errorf("a second Support: %d", code)
	}
	if code, out := w.do(ada, http.MethodPut, "/v1/groups/"+support+"/members/maria", map[string]string{"role": "manager"}); code != http.StatusOK {
		t.Fatalf("Maria as manager: %d %v", code, out)
	}
	// Maria, a manager, adds members among the account's people, and
	// names no manager.
	if code, out := w.do(maria, http.MethodPut, "/v1/groups/"+support+"/members/ana", map[string]string{"role": "member"}); code != http.StatusOK {
		t.Fatalf("Maria adds Ana: %d %v", code, out)
	}
	if code, _ := w.do(maria, http.MethodPut, "/v1/groups/"+support+"/members/paul", map[string]string{"role": "manager"}); code != http.StatusForbidden {
		t.Errorf("a manager named a manager: %d", code)
	}
	if code, _ := w.do(maria, http.MethodPut, "/v1/groups/"+support+"/members/stranger", map[string]string{"role": "member"}); code != http.StatusBadRequest {
		t.Errorf("someone not on the account joined: %d", code)
	}
	if code, _ := w.do(person("ana", membership.RoleApprover), http.MethodPut, "/v1/groups/"+support+"/members/paul", map[string]string{"role": "member"}); code != http.StatusForbidden {
		t.Errorf("a member added someone: %d", code)
	}

	if code, _ := w.do(maria, http.MethodPost, "/v1/agents/refunds/grants", map[string]string{"group": support}); code != http.StatusForbidden {
		t.Errorf("a manager without cards.manage shared a taf: %d", code)
	}
	if code, out := w.do(ed, http.MethodPost, "/v1/agents/refunds/grants", map[string]string{"group": support}); code != http.StatusOK {
		t.Fatalf("Ed shares Refunds with Support: %d %v", code, out)
	}
	if code, _ := w.do(maria, http.MethodDelete, "/v1/groups/nope/members/ana", nil); code != http.StatusNotFound {
		t.Errorf("a member out of a group the account doesn't have: %d", code)
	}
	if code, _ := w.do(ed, http.MethodPost, "/v1/agents/nobody/grants", map[string]string{"group": support}); code != http.StatusNotFound {
		t.Errorf("an agent the account doesn't have was shared: %d", code)
	}

	for subject, want := range map[string][3]bool{
		"ana": {true, false, false}, "maria": {true, true, false}, "paul": {false, false, false},
		"ed": {false, false, true}, "ada": {false, true, true}, "stranger": {false, false, false},
	} {
		a := w.answer(subject)
		if got := [3]bool{a["use"] == true, a["oversee"] == true, a["manage"] == true}; got != want {
			t.Errorf("%s: use, oversee, manage = %v, want %v (%v)", subject, got, want, a)
		}
	}
	if groups := w.answer("ana")["groups"].([]any); len(groups) != 1 || groups[0] != support {
		t.Errorf("Ana's groups for Refunds: %v", groups)
	}

	// Unsharing takes it away; Ana's use goes, Ed still manages.
	if code, _ := w.do(ed, http.MethodDelete, "/v1/agents/refunds/grants?group="+support, nil); code != http.StatusNoContent {
		t.Fatalf("unshare: %d", code)
	}
	if a := w.answer("ana"); a["use"] == true {
		t.Errorf("Ana uses Refunds after it was unshared: %v", a)
	}

	// The trail keeps who did what.
	trail, err := w.members.Audit(context.Background(), "t1", 20)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range trail {
		actions = append(actions, e.Actor+" "+e.Action)
	}
	for _, want := range []string{"ada group.created", "maria group.member_set", "ed grant.added", "ed grant.removed"} {
		if !strings.Contains(strings.Join(actions, "|"), want) {
			t.Errorf("trail lacks %q: %v", want, actions)
		}
	}
}

// An editor changes only the tafs they made: Ed
// shares and unshares Refunds, which he made, but not Bills, which Ada
// made, and KIFF's answer says he manages Refunds only. Ada shares either.
func TestAnEditorChangesOnlyTheTafsTheyMade(t *testing.T) {
	w := newAccessWorld(t)
	ada, ed := person("ada", membership.RoleAdmin), person("ed", membership.RoleEditor)
	if code, out := w.do(ed, http.MethodPost, "/v1/agents/refunds/grants", map[string]string{"subject": "ana"}); code != http.StatusOK {
		t.Fatalf("Ed shares Refunds, his own: %d %v", code, out)
	}
	if code, out := w.do(ed, http.MethodPost, "/v1/agents/bills/grants", map[string]string{"subject": "ana"}); code != http.StatusForbidden {
		t.Errorf("Ed shared Bills, Ada's: %d %v", code, out)
	}
	if code, out := w.do(ada, http.MethodPost, "/v1/agents/bills/grants", map[string]string{"subject": "ed"}); code != http.StatusOK {
		t.Fatalf("Ada shares Bills with Ed: %d %v", code, out)
	}
	if code, _ := w.do(ed, http.MethodDelete, "/v1/agents/bills/grants?subject=ed", nil); code != http.StatusForbidden {
		t.Errorf("Ed unshared Bills: %d", code)
	}
	_, mine := w.do(ed, http.MethodGet, "/v1/me/access", nil)
	got := mine["agents"].(map[string]any)
	if r, b := got["refunds"].(map[string]any), got["bills"].(map[string]any); r["manage"] != true || b["manage"] != false || b["use"] != true {
		t.Errorf("Ed manages Refunds only, and uses Bills shared with him: refunds %v, bills %v", r, b)
	}
}

// Coach's Setup is for the account's admins and editors by default
// (owner, 25 and 26 Oct): they may use it with no grant; anyone else when
// it's shared with them.
func TestSetupIsForAdminsByDefault(t *testing.T) {
	w := newAccessWorld(t)
	ctx := context.Background()
	agentStore := agents.NewMemoryStore()
	for _, id := range []string{"refunds", "coach"} {
		if err := agentStore.Create(ctx, agents.Agent{TenantID: "t1", ID: id, Kind: agents.KindTaf, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	keys := auth.NewMemoryAPIKeyStore()
	if err := keys.Insert(ctx, auth.APIKeyRecord{ID: "k-coach", Tenant: "t1", Subject: "ada", Roles: []string{auth.RoleSetupReader},
		AgentID: "coach", KeyHash: [32]byte{1}, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handlers.NewAccessHandler(w.store, w.members, agentStore, nil).WithKeys(keys).Register(mux)
	w.h = auth.DefaultRoutePolicy().Wrap(mux)
	ask := func(agent, subject string) bool {
		code, out := w.do(auth.Identity{Subject: "key-2", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"admin"}},
			http.MethodPost, "/v1/agents/"+agent+"/access", map[string]string{"subject": subject})
		if code != http.StatusOK {
			t.Fatalf("%s for %s: %d %v", agent, subject, code, out)
		}
		return out["use"] == true
	}
	if !ask("coach", "ada") || !ask("coach", "ed") || ask("coach", "ana") || ask("refunds", "ada") {
		t.Errorf("Setup for an admin and an editor, not an approver; another taf for neither")
	}
	if err := w.store.AddGrant(ctx, access.Grant{TenantID: "t1", Agent: "coach", Subject: "ana"}); err != nil {
		t.Fatal(err)
	}
	if !ask("coach", "ana") {
		t.Errorf("Setup shared with Ana")
	}
	_, mine := w.do(person("ada", membership.RoleAdmin), http.MethodGet, "/v1/me/access", nil)
	if coach := mine["agents"].(map[string]any)["coach"].(map[string]any); coach["use"] != true {
		t.Errorf("Ada's own list: %v", coach)
	}
}

// A person asks only about themselves, and lists their own
// access; an agent the account doesn't have is 404.
func TestAccessAnswerAndMine(t *testing.T) {
	w := newAccessWorld(t)
	ana := person("ana", membership.RoleApprover)
	if err := w.store.AddGrant(context.Background(), access.Grant{TenantID: "t1", Agent: "refunds", Subject: "ana"}); err != nil {
		t.Fatal(err)
	}
	if code, _ := w.do(ana, http.MethodPost, "/v1/agents/refunds/access", map[string]string{"subject": "paul"}); code != http.StatusForbidden {
		t.Errorf("a person asked about someone else: %d", code)
	}
	if code, out := w.do(ana, http.MethodPost, "/v1/agents/refunds/access", map[string]string{"subject": "ana"}); code != http.StatusOK || out["use"] != true {
		t.Errorf("Ana about herself: %d %v", code, out)
	}
	key := auth.Identity{Subject: "key-1", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, AgentID: "refunds", Roles: []string{"gateway_agent"}}
	// An agent's key asks about its own agent, by name or without naming it.
	if code, out := w.do(key, http.MethodPost, "/v1/me/access", map[string]string{"subject": "ana"}); code != http.StatusOK || out["use"] != true {
		t.Errorf("the key's own agent: %d %v", code, out)
	}
	if code, _ := w.do(ana, http.MethodPost, "/v1/me/access", map[string]string{"subject": "ana"}); code != http.StatusForbidden {
		t.Errorf("a person asked as an agent: %d", code)
	}
	if code, _ := w.do(key, http.MethodPost, "/v1/agents/invoices/access", map[string]string{"subject": "ana"}); code != http.StatusForbidden {
		t.Errorf("one agent's key asked about another: %d", code)
	}
	account := auth.Identity{Subject: "key-2", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"admin"}}
	if code, _ := w.do(account, http.MethodPost, "/v1/agents/nobody/access", map[string]string{"subject": "ana"}); code != http.StatusNotFound {
		t.Errorf("an unknown agent: %d", code)
	}
	code, out := w.do(ana, http.MethodGet, "/v1/me/access", nil)
	if code != http.StatusOK {
		t.Fatalf("mine: %d %v", code, out)
	}
	if refunds := out["agents"].(map[string]any)["refunds"].(map[string]any); refunds["use"] != true || refunds["oversee"] == true {
		t.Errorf("Ana's access to Refunds: %v", refunds)
	}
	if code, _ := w.do(key, http.MethodGet, "/v1/me/access", nil); code == http.StatusOK {
		t.Errorf("a key listed a person's access")
	}
}

// Who an agent is shared with is for whoever manages it: an admin, and an
// editor for the tafs they made. A person who only uses it reads no list.
func TestOnlyWhoManagesATafReadsWhoItIsSharedWith(t *testing.T) {
	w := newAccessWorld(t)
	ada, ed := person("ada", membership.RoleAdmin), person("ed", membership.RoleEditor)
	if code, out := w.do(ed, http.MethodPost, "/v1/agents/refunds/grants", map[string]string{"subject": "ana"}); code != http.StatusOK {
		t.Fatalf("Ed shares Refunds, his own: %d %v", code, out)
	}
	if code, out := w.do(ada, http.MethodGet, "/v1/agents/refunds/grants", nil); code != http.StatusOK || len(out["grants"].([]any)) != 1 {
		t.Errorf("Ada reads Refunds' sharing: %d %v", code, out)
	}
	if code, _ := w.do(ed, http.MethodGet, "/v1/agents/refunds/grants", nil); code != http.StatusOK {
		t.Errorf("Ed reads his own taf's sharing: %d", code)
	}
	if code, _ := w.do(ed, http.MethodGet, "/v1/agents/bills/grants", nil); code != http.StatusForbidden {
		t.Errorf("Ed read the sharing of Bills, Ada's: %d", code)
	}
	for _, p := range []auth.Identity{person("ana", membership.RoleViewer), person("maria", membership.RoleApprover)} {
		if code, _ := w.do(p, http.MethodGet, "/v1/agents/refunds/grants", nil); code != http.StatusForbidden {
			t.Errorf("%s, who only uses Refunds, read who it is shared with: %d", p.Subject, code)
		}
	}
}

// Archiving an agent removes its grants, each one in the trail, so a share
// never outlives its agent: no group lists it, and another taf keeps its own.
func TestArchivingAnAgentRemovesItsGrants(t *testing.T) {
	w := newAccessWorld(t)
	ada, ed := person("ada", membership.RoleAdmin), person("ed", membership.RoleEditor)
	code, out := w.do(ada, http.MethodPost, "/v1/groups", map[string]string{"name": "Support"})
	if code != http.StatusCreated {
		t.Fatalf("group: %d %v", code, out)
	}
	support := out["id"].(string)
	for _, share := range []struct {
		who   auth.Identity
		agent string
		body  map[string]string
	}{
		{ed, "refunds", map[string]string{"group": support}},
		{ed, "refunds", map[string]string{"subject": "ana"}},
		{ada, "bills", map[string]string{"group": support}},
	} {
		if code, out := w.do(share.who, http.MethodPost, "/v1/agents/"+share.agent+"/grants", share.body); code != http.StatusOK {
			t.Fatalf("share %s %v: %d %v", share.agent, share.body, code, out)
		}
	}

	if code, out := w.do(ed, http.MethodPost, "/v1/me/agents/refunds/archive", nil); code != http.StatusOK {
		t.Fatalf("archive: %d %v", code, out)
	}

	if g, _ := w.store.Grants(context.Background(), "t1", "refunds"); len(g) != 0 {
		t.Errorf("Refunds keeps grants after it was archived: %v", g)
	}
	_, groups := w.do(ada, http.MethodGet, "/v1/groups", nil)
	for _, g := range groups["groups"].([]any) {
		agents := g.(map[string]any)["agents"].([]any)
		if g.(map[string]any)["id"] == support && (len(agents) != 1 || agents[0] != "bills") {
			t.Errorf("Support's agents after archiving Refunds: %v, want only bills", agents)
		}
	}
	trail, err := w.members.Audit(context.Background(), "t1", 20)
	if err != nil {
		t.Fatal(err)
	}
	removed := 0
	for _, e := range trail {
		if e.Action == handlers.AuditGrantRemoved && e.Actor == "ed" && e.Target == "refunds" {
			removed++
		}
	}
	if removed != 2 {
		t.Errorf("trail: %d grant.removed by Ed on Refunds, want 2", removed)
	}
}

// goneGrants answers ErrNotFound for a grant someone else just removed.
type goneGrants struct {
	*access.MemoryStore
	gone string
}

func (s goneGrants) RemoveGrant(ctx context.Context, g access.Grant) error {
	if err := s.MemoryStore.RemoveGrant(ctx, g); err != nil {
		return err
	}
	if g.Subject == s.gone {
		return access.ErrNotFound
	}
	return nil
}

// A grant already gone when archiving removes it isn't recorded twice: the
// trail keeps only the removals that happened.
func TestArchivingRecordsOnlyRemovalsThatHappened(t *testing.T) {
	ctx := context.Background()
	members := membership.NewMemoryStore()
	if err := members.Mutate(ctx, "t1", func(tx membership.Tx) error {
		return tx.InsertMember(membership.Member{TenantID: "t1", Subject: "ada", Role: membership.RoleAdmin, Status: membership.StatusActive})
	}); err != nil {
		t.Fatal(err)
	}
	agentStore := agents.NewMemoryStore()
	if err := agentStore.Create(ctx, agents.Agent{TenantID: "t1", ID: "refunds", Kind: agents.KindTaf, CreatedAt: time.Now(), CreatedBy: "ada"}); err != nil {
		t.Fatal(err)
	}
	store := goneGrants{MemoryStore: access.NewMemoryStore(), gone: "luis"}
	for _, s := range []string{"ana", "luis"} {
		if err := store.AddGrant(ctx, access.Grant{TenantID: "t1", Agent: "refunds", Subject: s, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	ah := handlers.NewAccessHandler(store, members, agentStore, nil)
	ah.Register(mux)
	handlers.NewAgentsHandler(agentStore, handlers.WithAgentsAccess(store), handlers.WithAgentsUnshare(ah.RemoveAgentGrants)).Register(mux)
	w := &accessWorld{t: t, h: auth.DefaultRoutePolicy().Wrap(mux), members: members}
	if code, out := w.do(person("ada", membership.RoleAdmin), http.MethodPost, "/v1/me/agents/refunds/archive", nil); code != http.StatusOK {
		t.Fatalf("archive: %d %v", code, out)
	}
	trail, _ := members.Audit(ctx, "t1", 20)
	var removed []string
	for _, e := range trail {
		if e.Action == handlers.AuditGrantRemoved {
			removed = append(removed, e.OldValue)
		}
	}
	if len(removed) != 1 || removed[0] != "subject:ana" {
		t.Errorf("the trail's removals: %v, want only subject:ana", removed)
	}
}

// The account's Coach is answered even before its agent has a record, and
// the answer says which agent it is, so no app keeps its id.
func TestMyAccessNamesCoachEvenWithoutItsRecord(t *testing.T) {
	w := newAccessWorld(t)
	ctx := context.Background()
	keys := auth.NewMemoryAPIKeyStore()
	if err := keys.Insert(ctx, auth.APIKeyRecord{ID: "k-coach", Tenant: "t1", Subject: "ada", Roles: []string{auth.RoleSetupReader},
		AgentID: "setup", KeyHash: [32]byte{2}, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handlers.NewAccessHandler(w.store, w.members, agents.NewMemoryStore(), nil).WithKeys(keys).Register(mux)
	w.h = auth.DefaultRoutePolicy().Wrap(mux)
	_, mine := w.do(person("ada", membership.RoleAdmin), http.MethodGet, "/v1/me/access", nil)
	coach, _ := mine["agents"].(map[string]any)["setup"].(map[string]any)
	if mine["setup_agent"] != "setup" || coach == nil || coach["use"] != true {
		t.Fatalf("an admin's access: %v", mine)
	}
	_, theirs := w.do(person("ana", membership.RoleApprover), http.MethodGet, "/v1/me/access", nil)
	if c, _ := theirs["agents"].(map[string]any)["setup"].(map[string]any); c == nil || c["use"] == true {
		t.Fatalf("an approver's access to Coach, not shared: %v", theirs)
	}
}

// Each group, and each member, says what the person asking may do to it, by
// the rules its routes apply: an admin everything; a group's
// manager adds members and removes members but not managers; anyone else
// nothing.
func TestGroupsSayWhatThePersonAskingMayDo(t *testing.T) {
	w := newAccessWorld(t)
	ctx := context.Background()
	if err := w.store.CreateGroup(ctx, access.Group{TenantID: "t1", ID: "grp_s", Name: "Support", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for subject, role := range map[string]access.GroupRole{"maria": access.Manager, "ed": access.Manager, "ana": access.Member} {
		if err := w.store.SetMember(ctx, access.GroupMember{TenantID: "t1", Group: "grp_s", Subject: subject, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	can := func(id auth.Identity) (map[string]any, map[string]bool) {
		t.Helper()
		code, out := w.do(id, http.MethodGet, "/v1/groups", nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d %v", code, out)
		}
		g := out["groups"].([]any)[0].(map[string]any)
		remove := map[string]bool{}
		for _, m := range g["members"].([]any) {
			mm := m.(map[string]any)
			remove[mm["subject"].(string)] = mm["can"].(map[string]any)["remove"] == true
		}
		return g["can"].(map[string]any), remove
	}
	if g, rm := can(person("ada", membership.RoleAdmin)); g["rename"] != true || g["delete"] != true || g["add"] != true || g["set_role"] != true || !rm["maria"] || !rm["ana"] {
		t.Errorf("an admin: %v %v", g, rm)
	}
	if g, rm := can(person("maria", membership.RoleApprover)); g["rename"] == true || g["delete"] == true || g["add"] != true || g["set_role"] == true || rm["ed"] || !rm["ana"] {
		t.Errorf("the group's manager: %v %v", g, rm)
	}
	if g, rm := can(person("paul", membership.RoleViewer)); g["add"] == true || rm["ana"] {
		t.Errorf("someone outside it: %v %v", g, rm)
	}
}

// A share says who it's with and what sharing and unsharing mean, in the
// owner's words, named by the agent's own name.
func TestSharesSayWhatTheyMean(t *testing.T) {
	w := newAccessWorld(t)
	ctx := context.Background()
	if err := w.store.CreateGroup(ctx, access.Group{TenantID: "t1", ID: "grp_s", Name: "Support", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ana", "maria"} {
		if err := w.store.SetMember(ctx, access.GroupMember{TenantID: "t1", Group: "grp_s", Subject: s, Role: access.Member}); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []access.Grant{{TenantID: "t1", Agent: "bills", Group: "grp_s"}, {TenantID: "t1", Agent: "bills", Subject: "paul"}} {
		if err := w.store.AddGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	code, out := w.do(person("ada", membership.RoleAdmin), http.MethodGet, "/v1/agents/bills/grants", nil)
	if code != http.StatusOK {
		t.Fatalf("grants: %d %v", code, out)
	}
	if out["shared_with"] != "Shared with Support and paul" || out["own"] == "" {
		t.Errorf("summary: %v", out)
	}
	byName := map[string]map[string]any{}
	for _, g := range out["grants"].([]any) {
		m := g.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if s := byName["Support"]; s == nil || (s["uses"] != "Support uses bills: ana and maria." && s["uses"] != "Support uses bills: maria and ana.") || s["stops"] != "Support stops using bills. Their chats stay theirs." {
		t.Errorf("the group's share: %v", byName["Support"])
	}
	if p := byName["paul"]; p == nil || p["uses"] != "paul uses bills." {
		t.Errorf("the person's share: %v", byName["paul"])
	}
}

// Coach's key learns whether the person it hands a taf over for may
// manage Cards now, with no session of theirs; any other
// agent's key, and every person, never sees it.
func TestCoachsKeyAsksWhetherThePersonManagesCards(t *testing.T) {
	w := newAccessWorld(t)
	coach := auth.Identity{Subject: "key-c", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, AgentID: "refunds", Roles: []string{auth.RoleSetupReader}}
	for subject, want := range map[string]bool{"ada": true, "ed": true, "maria": false, "paul": false, "nobody": false} {
		code, out := w.do(coach, http.MethodPost, "/v1/me/access", map[string]string{"subject": subject})
		if code != http.StatusOK || out["cards_manage"] != want {
			t.Errorf("%s: %d cards_manage=%v, want %v", subject, code, out["cards_manage"], want)
		}
	}
	other := auth.Identity{Subject: "key-1", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, AgentID: "refunds", Roles: []string{"gateway_agent"}}
	if _, out := w.do(other, http.MethodPost, "/v1/me/access", map[string]string{"subject": "ada"}); out["cards_manage"] != nil {
		t.Errorf("another agent's key saw cards_manage: %v", out)
	}
}
