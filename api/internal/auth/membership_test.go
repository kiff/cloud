package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/kiff/pkg/kiff/actor"

	"github.com/kiff/cloud/api/internal/membership"
)

type memberFixture struct {
	t        *testing.T
	resolver *MapResolver
	keys     *MemoryAPIKeyStore
	store    *membership.MemoryStore
	svc      *membership.Service
	mw       *Middleware
}

// newMemberFixture: alice signs in first and founds tenant-a; bob's home
// is tenant-b. Subjects not registered here are unknown to the stub.
func newMemberFixture(t *testing.T) *memberFixture {
	t.Helper()
	f := &memberFixture{t: t, resolver: NewMapResolver(), keys: NewMemoryAPIKeyStore(), store: membership.NewMemoryStore()}
	f.resolver.Set("alice", actor.Actor{ID: "alice"}, "tenant-a")
	f.resolver.Set("bob", actor.Actor{ID: "bob"}, "tenant-b")
	f.resolver.Set("carol", actor.Actor{ID: "carol"}, "tenant-c")
	f.svc = membership.NewService(f.store)
	f.mw = NewMiddleware(NewStubProvider(f.resolver), f.keys, f.resolver, WithMemberships(f.svc))
	return f
}

func (f *memberFixture) do(bearer, tenantHeader string) (int, Identity, map[string]string) {
	f.t.Helper()
	down := &authedHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	if tenantHeader != "" {
		req.Header.Set(TenantHeader, tenantHeader)
	}
	f.mw.Wrap(down).ServeHTTP(rec, req)
	var body map[string]string
	if !down.called {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, down.id, body
}

func (f *memberFixture) invite(tenant, admin, subject string, role membership.Role) {
	f.t.Helper()
	_, tok, err := f.svc.CreateInvite(context.Background(), tenant, membership.Actor{Subject: admin},
		membership.InviteRequest{Email: subject + "@x.io", Role: role})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.svc.AcceptInvite(context.Background(), tok, membership.Actor{Subject: subject}); err != nil {
		f.t.Fatal(err)
	}
}

func TestMembershipFirstSignInFoundsAndCarriesTheRole(t *testing.T) {
	f := newMemberFixture(t)
	code, id, _ := f.do("alice", "")
	if code != http.StatusOK || id.Tenant != "tenant-a" || id.MemberRole != membership.RoleAdmin {
		t.Fatalf("first sign-in: %d %+v", code, id)
	}
	if !sameStrings(id.Roles, []string{"admin", "tenant_owner", "operator"}) {
		t.Fatalf("admin roles = %v, want today's owner set", id.Roles)
	}
}

func TestMembershipSelectsTheTenantFromTheHeader(t *testing.T) {
	f := newMemberFixture(t)
	f.do("alice", "") // tenant-a founded by alice
	f.do("bob", "")   // tenant-b founded by bob
	f.invite("tenant-a", "alice", "bob", membership.RoleApprover)

	if code, _, body := f.do("bob", ""); code != http.StatusConflict || body["error"] != "tenant_required" {
		t.Fatalf("two memberships, no header: %d %v", code, body)
	}
	code, id, _ := f.do("bob", "tenant-a")
	if code != http.StatusOK || id.Tenant != "tenant-a" || id.MemberRole != membership.RoleApprover {
		t.Fatalf("bob in tenant-a: %d %+v", code, id)
	}
	if !sameStrings(id.Roles, []string{"approver"}) {
		t.Fatalf("approver roles = %v; the owner widening must not apply (M3)", id.Roles)
	}
	if code, id, _ := f.do("bob", "tenant-b"); code != http.StatusOK || id.MemberRole != membership.RoleAdmin {
		t.Fatalf("bob in tenant-b: %d %+v", code, id)
	}
	// The header is a selector, never an authority (M13).
	if code, _, body := f.do("alice", "tenant-b"); code != http.StatusForbidden || body["error"] != "not_a_member" {
		t.Fatalf("alice naming tenant-b: %d %v", code, body)
	}
}

func TestMembershipSuspensionTakesEffectOnTheNextRequest(t *testing.T) {
	f := newMemberFixture(t)
	f.do("alice", "")
	f.invite("tenant-a", "alice", "carol", membership.RoleEditor)
	// carol's only live membership is tenant-a (her home tenant-c was
	// never founded), so no header is needed.
	if code, id, _ := f.do("carol", ""); code != http.StatusOK || id.Tenant != "tenant-a" {
		t.Fatalf("carol before suspension: %d %+v", code, id)
	}
	if _, err := f.svc.Suspend(context.Background(), "tenant-a", membership.Actor{Subject: "alice"}, "carol"); err != nil {
		t.Fatal(err)
	}
	if code, _, body := f.do("carol", ""); code != http.StatusForbidden || body["error"] != "membership_suspended" {
		t.Fatalf("suspended: %d %v (M4, M12)", code, body)
	}
	if _, err := f.svc.Reactivate(context.Background(), "tenant-a", membership.Actor{Subject: "alice"}, "carol"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.do("carol", ""); code != http.StatusOK {
		t.Fatalf("reactivated: %d", code)
	}
}

func TestRemovedMemberDoesNotFoundANewAccount(t *testing.T) {
	f := newMemberFixture(t)
	f.do("alice", "")
	f.resolver.Set("dave", actor.Actor{ID: "dave"}, "tenant-a") // dave's home is alice's tenant
	f.invite("tenant-a", "alice", "dave", membership.RoleViewer)
	if _, err := f.svc.Remove(context.Background(), "tenant-a", membership.Actor{Subject: "alice"}, "dave"); err != nil {
		t.Fatal(err)
	}
	if code, _, body := f.do("dave", ""); code != http.StatusForbidden || body["error"] != "membership_removed" {
		t.Fatalf("removed: %d %v", code, body)
	}
}

func TestAccountKeyActsWithNoMoreThanItsMintersRole(t *testing.T) {
	f := newMemberFixture(t)
	f.do("alice", "")
	f.invite("tenant-a", "alice", "bob", membership.RoleAdmin)
	mint := func(subject string, roles []string, agent string) string {
		gen, err := NewAPIKey("tenant-a", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := f.keys.Insert(context.Background(), APIKeyRecord{ID: subject + agent + roles[0], Tenant: "tenant-a",
			Subject: subject, Roles: roles, AgentID: agent, KeyHash: gen.Hash, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		return gen.Plaintext
	}
	account := mint("bob", []string{"tenant_owner"}, "")
	runtime := mint("bob", []string{"ops_agent"}, "")
	agentKey := mint("bob", []string{"ops_agent"}, "agent-1")
	operator := mint("ops", []string{"admin"}, "")

	for name, k := range map[string]string{"account": account, "runtime": runtime, "agent": agentKey, "operator": operator} {
		if code, _, _ := f.do(k, ""); code != http.StatusOK {
			t.Fatalf("%s key before demotion: %d", name, code)
		}
	}
	if _, _, err := f.svc.ChangeRole(context.Background(), "tenant-a", membership.Actor{Subject: "alice"}, "bob", membership.RoleEditor); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.do(account, ""); code != http.StatusUnauthorized {
		t.Fatalf("account key of a demoted minter: %d, want 401 (M15)", code)
	}
	for name, k := range map[string]string{"runtime": runtime, "agent": agentKey, "operator": operator} {
		if code, _, _ := f.do(k, ""); code != http.StatusOK {
			t.Fatalf("%s key after demotion: %d; only account keys follow the minter", name, code)
		}
	}
	if _, _, err := f.svc.ChangeRole(context.Background(), "tenant-a", membership.Actor{Subject: "alice"}, "bob", membership.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.do(account, ""); code != http.StatusOK {
		t.Fatalf("account key after re-promotion: %d", code)
	}
}

func TestWrapPersonResolvesNoTenant(t *testing.T) {
	f := newMemberFixture(t)
	f.do("alice", "")
	f.do("bob", "")
	f.invite("tenant-a", "alice", "bob", membership.RoleViewer)
	down := &authedHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/me/memberships", nil)
	req.Header.Set("Authorization", "Bearer bob")
	f.mw.WrapPerson(down).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || down.id.Tenant != "" || len(down.id.Roles) != 0 {
		t.Fatalf("WrapPerson: %d %+v", rec.Code, down.id)
	}
}

func TestRoutePolicy(t *testing.T) {
	p := DefaultRoutePolicy()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	run := func(role membership.Role, apiKey bool, method, path string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		id := Identity{Subject: "s", Tenant: "t", Method: MethodSession, MemberRole: role}
		if apiKey {
			id = Identity{Subject: "k", Tenant: "t", Method: MethodAPIKey, APIKey: true, Roles: []string{"ops_agent"}}
		}
		p.Wrap(ok).ServeHTTP(rec, req.WithContext(WithIdentity(req.Context(), id)))
		return rec.Code
	}
	for _, c := range []struct {
		role         membership.Role
		method, path string
		want         int
	}{
		{membership.RoleViewer, "GET", "/v1/me/mandates", 204},
		{membership.RoleViewer, "POST", "/v1/me/mandates", 403},
		{membership.RoleViewer, "POST", "/v1/me/card-exceptions/x/approve-once", 403},
		{membership.RoleApprover, "POST", "/v1/me/card-exceptions/x/approve-once", 204},
		{membership.RoleApprover, "POST", "/v1/me/card-exceptions/x/change-card", 403},
		{membership.RoleApprover, "POST", "/v1/approvals/a1/grant", 204},
		{membership.RoleApprover, "POST", "/v1/proposals/decide", 403},
		{membership.RoleEditor, "POST", "/v1/me/card-exceptions/x/change-card", 204},
		{membership.RoleEditor, "PUT", "/v1/me/domains/refunds", 204},
		{membership.RoleEditor, "POST", "/v1/keys", 403},
		{membership.RoleEditor, "POST", "/v1/me/billing-portal", 403},
		{membership.RoleEditor, "POST", "/v1/tenant/invites", 403},
		{membership.RoleEditor, "GET", "/v1/me/export", 403},
		{membership.RoleEditor, "GET", "/v1/tenant/audit", 403},
		{membership.RoleAdmin, "POST", "/v1/keys", 204},
		{membership.RoleAdmin, "PUT", "/v1/tenant/governance", 204},
		// A write no rule names is admin-only until someone decides.
		{membership.RoleEditor, "POST", "/v1/me/something-new", 403},
		{membership.RoleAdmin, "POST", "/v1/me/something-new", 204},
		{membership.RoleViewer, "POST", "/v1/me/support/tickets", 204},
	} {
		if got := run(c.role, false, c.method, c.path); got != c.want {
			t.Errorf("%s %s %s = %d, want %d", c.role, c.method, c.path, got, c.want)
		}
	}
	// An account key is not policed by role; its handlers check it.
	acct := Identity{Subject: "k", Tenant: "t", Method: MethodAPIKey, APIKey: true, Roles: []string{"admin"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/me/mandates", nil)
	p.Wrap(ok).ServeHTTP(rec, req.WithContext(WithIdentity(req.Context(), acct)))
	if rec.Code != 204 {
		t.Errorf("account key on POST /v1/me/mandates: %d, want 204", rec.Code)
	}
	// A runtime key reaches only RuntimeKeyRules.
	if got := run("", true, "POST", "/v1/me/mandates"); got != http.StatusForbidden {
		t.Errorf("runtime key on POST /v1/me/mandates: %d, want 403", got)
	}
	if got := run("", true, "POST", "/v1/proposals/decide"); got != 204 {
		t.Errorf("runtime key on POST /v1/proposals/decide: %d, want 204", got)
	}
}

func TestStepUp(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	fresh, stale := 2*time.Minute, 30*time.Minute
	created := func(at time.Time) func(context.Context, string) (time.Time, error) {
		return func(context.Context, string) (time.Time, error) { return at, nil }
	}
	session := func(age *time.Duration) Identity {
		return Identity{Subject: "s", Method: MethodSession, FactorAge: age, Metadata: map[string]string{"clerk_session_id": "sess_1"}}
	}
	for name, c := range map[string]struct {
		s    StepUp
		id   Identity
		pass bool
	}{
		"fresh factor":                   {StepUp{Now: func() time.Time { return now }}, session(&fresh), true},
		"stale factor":                   {StepUp{SessionCreatedAt: created(now), Now: func() time.Time { return now }}, session(&stale), false},
		"no fva, new session":            {StepUp{SessionCreatedAt: created(now.Add(-time.Minute)), Now: func() time.Time { return now }}, session(nil), true},
		"no fva, old session":            {StepUp{SessionCreatedAt: created(now.Add(-time.Hour)), Now: func() time.Time { return now }}, session(nil), false},
		"no fva, lookup fails":           {StepUp{SessionCreatedAt: func(context.Context, string) (time.Time, error) { return time.Time{}, errors.New("down") }}, session(nil), false},
		"no fva, no fallback":            {StepUp{}, session(nil), false},
		"developer session":              {StepUp{TrustAll: true}, Identity{Method: MethodDeveloper}, false},
		"api key":                        {StepUp{TrustAll: true}, Identity{Method: MethodAPIKey, APIKey: true}, false},
		"stub provider trusts a session": {StepUp{TrustAll: true}, session(nil), true},
	} {
		_, err := c.s.Check(context.Background(), c.id)
		if (err == nil) != c.pass {
			t.Errorf("%s: err = %v, want pass=%v", name, err, c.pass)
		}
	}
}

func TestFactorAgeNeedsTheClaimPresent(t *testing.T) {
	tok := func(payload string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".s"
	}
	if got := factorAge(tok(`{"sub":"u"}`), [2]int64{0, 0}); got != nil {
		t.Fatalf("absent fva read as %v; the SDK's zero value must not mean just verified", *got)
	}
	if got := factorAge(tok(`{"fva":[3,-1]}`), [2]int64{3, -1}); got == nil || *got != 3*time.Minute {
		t.Fatalf("fva [3,-1] = %v, want 3m", got)
	}
	if got := factorAge(tok(`{"fva":[-1,-1]}`), [2]int64{-1, -1}); got != nil {
		t.Fatalf("never verified = %v, want nil", *got)
	}
}

// provisioningResolver stands in for LazyResolver: a subject without a
// home tenant gets a fresh one, and the test counts how many were made.
type provisioningResolver struct {
	homes       map[string]string
	provisioned []string
}

func (p *provisioningResolver) Resolve(_ context.Context, id Identity) (actor.Actor, string, error) {
	t, ok := p.homes[id.Subject]
	if !ok {
		t = "fresh-" + id.Subject
		p.homes[id.Subject] = t
		p.provisioned = append(p.provisioned, id.Subject)
	}
	return actor.Actor{ID: "actor-" + id.Subject}, t, nil
}

// Someone who joined by invitation has no home tenant. Removed, they are
// told so; they are not handed a brand-new account.
func TestRemovedInviteeIsNotProvisionedAFreshAccount(t *testing.T) {
	auth := NewMapResolver() // authentication only: the stub needs the subjects registered
	for _, s := range []string{"alice", "erin"} {
		auth.Set(s, actor.Actor{ID: s}, "unused")
	}
	homes := &provisioningResolver{homes: map[string]string{"alice": "tenant-a"}}
	store := membership.NewMemoryStore()
	svc := membership.NewService(store)
	f := &memberFixture{t: t, resolver: auth, keys: NewMemoryAPIKeyStore(), store: store, svc: svc,
		mw: NewMiddleware(NewStubProvider(auth), NewMemoryAPIKeyStore(), homes, WithMemberships(svc))}
	f.do("alice", "")
	f.invite("tenant-a", "alice", "erin", membership.RoleEditor)
	if code, id, _ := f.do("erin", ""); code != http.StatusOK || id.Tenant != "tenant-a" {
		t.Fatalf("erin before removal: %d %+v", code, id)
	}
	if _, err := svc.Remove(context.Background(), "tenant-a", membership.Actor{Subject: "alice"}, "erin"); err != nil {
		t.Fatal(err)
	}
	if code, _, body := f.do("erin", ""); code != http.StatusForbidden || body["error"] != "membership_removed" {
		t.Fatalf("removed invitee: %d %v", code, body)
	}
	if len(homes.provisioned) != 0 {
		t.Fatalf("provisioned %v; a removed invitee must not get a fresh account", homes.provisioned)
	}
}
