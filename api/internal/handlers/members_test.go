package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/tenant"
)

type memberNotifier struct {
	mu      sync.Mutex
	invites []string // to
	sent    []notify.MemberInvite
	notices []string // to|subject
	bodies  []string
}

func (n *memberNotifier) MemberInvited(_ context.Context, to string, inv notify.MemberInvite) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.invites = append(n.invites, to)
	n.sent = append(n.sent, inv)
	return nil
}

func (n *memberNotifier) AccountNotice(_ context.Context, to string, a notify.AccountNotice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notices = append(n.notices, to+"|"+a.Subject)
	n.bodies = append(n.bodies, a.Body)
	return nil
}

type emailsBySubject struct{}

func (emailsBySubject) EmailForSubject(_ context.Context, subject string) (string, error) {
	return subject + "@x.io", nil
}

type membersFixture struct {
	t        *testing.T
	mux      *http.ServeMux
	personal *http.ServeMux
	svc      *membership.Service
	keys     *auth.MemoryAPIKeyStore
	sessions *auth.MemoryDeveloperSessionStore
	notifier *memberNotifier
	stepUp   *auth.StepUp
	h        *MembersHandler
}

func newMembersFixture(t *testing.T) *membersFixture {
	t.Helper()
	f := &membersFixture{t: t, svc: membership.NewService(membership.NewMemoryStore()), keys: auth.NewMemoryAPIKeyStore(),
		sessions: auth.NewMemoryDeveloperSessionStore(), notifier: &memberNotifier{}}
	f.stepUp = &auth.StepUp{} // no fallback: only a fresh fva passes
	if _, err := f.svc.EnsureFounder(context.Background(), "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	h := NewMembersHandler(f.svc, nil,
		WithMembersStepUp(f.stepUp), WithMembersCredentials(f.keys, f.sessions),
		WithMembersEmails(emailsBySubject{}), WithMembersNotifier(f.notifier), WithMembersAppBaseURL("https://app.kiff.dev"),
		WithMembersDispatch(func(fn func()) { fn() }))
	f.h = h
	f.mux, f.personal = http.NewServeMux(), http.NewServeMux()
	h.Register(f.mux)
	h.RegisterPersonal(f.personal)
	return f
}

// session is a person on t1 with role; recent says whether their sign-in
// is fresh enough for step-up.
func session(subject string, role membership.Role, recent bool) auth.Identity {
	id := auth.Identity{Subject: subject, Tenant: "t1", Method: auth.MethodSession, MemberRole: role, Roles: membership.IdentityRoles(role)}
	age := 2 * time.Minute
	if !recent {
		age = time.Hour
	}
	id.FactorAge = &age
	return id
}

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return m
}

// invite creates an invite as alice and returns the token from the link.
func (f *membersFixture) invite(email, role string) string {
	f.t.Helper()
	rr := do(f.t, f.mux, session("alice", membership.RoleAdmin, true), http.MethodPost, "/v1/tenant/invites",
		map[string]any{"email": email, "role": role})
	if rr.Code != http.StatusCreated {
		f.t.Fatalf("invite: %d %s", rr.Code, rr.Body.String())
	}
	link := decode(f.t, rr.Body.String())["accept_url"].(string)
	return link[strings.LastIndex(link, "/")+1:]
}

func TestInviteAndAcceptOverHTTP(t *testing.T) {
	f := newMembersFixture(t)
	tok := f.invite("bob@x.io", "editor")
	if len(f.notifier.invites) != 1 || f.notifier.invites[0] != "bob@x.io" {
		t.Fatalf("invitation email: %v", f.notifier.invites)
	}
	bob := auth.Identity{Subject: "bob", Method: auth.MethodSession}
	rr := do(t, f.personal, bob, http.MethodPost, "/v1/invites/preview", map[string]any{"token": tok})
	if rr.Code != http.StatusOK || decode(t, rr.Body.String())["role"] != "editor" {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	// Preview grants nothing (M5).
	if _, err := f.svc.Store.Live(context.Background(), "t1", "bob"); err == nil {
		t.Fatal("preview created a membership")
	}
	rr = do(t, f.personal, bob, http.MethodPost, "/v1/invites/accept", map[string]any{"token": tok})
	if rr.Code != http.StatusOK || decode(t, rr.Body.String())["tenant_id"] != "t1" {
		t.Fatalf("accept: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, f.personal, bob, http.MethodPost, "/v1/invites/accept", map[string]any{"token": tok}); rr.Code != http.StatusGone {
		t.Fatalf("token reused: %d", rr.Code)
	}
	rr = do(t, f.personal, bob, http.MethodGet, "/v1/me/memberships", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"role":"editor"`) {
		t.Fatalf("memberships: %d %s", rr.Code, rr.Body.String())
	}
}

func TestInvitingAnApproverTellsTheOthers(t *testing.T) {
	f := newMembersFixture(t)
	// alone on the account, alice is not told about her own act.
	f.invite("carol@x.io", "approver")
	if len(f.notifier.notices) != 0 {
		t.Fatalf("notices = %v", f.notifier.notices)
	}
	tok := f.invite("dan@x.io", "approver")
	if _, err := f.svc.AcceptInvite(context.Background(), tok, membership.Actor{Subject: "dan"}); err != nil {
		t.Fatal(err)
	}
	f.notifier.notices = nil
	f.invite("eve@x.io", "admin")
	if len(f.notifier.notices) != 1 || !strings.HasPrefix(f.notifier.notices[0], "dan@x.io|A new admin") {
		t.Fatalf("notices = %v; the existing approver must hear about a new admin (M14)", f.notifier.notices)
	}
}

func TestStepUpGuardsAdminLevelChanges(t *testing.T) {
	f := newMembersFixture(t)
	stale := session("alice", membership.RoleAdmin, false)
	rr := do(t, f.mux, stale, http.MethodPost, "/v1/tenant/invites", map[string]any{"email": "a@x.io", "role": "admin"})
	if rr.Code != http.StatusForbidden || decode(t, rr.Body.String())["error"] != "reauth_required" {
		t.Fatalf("admin invite on a stale sign-in: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, f.mux, stale, http.MethodPost, "/v1/tenant/invites", map[string]any{"email": "v@x.io", "role": "viewer"}); rr.Code != http.StatusCreated {
		t.Fatalf("a viewer invite needs no step-up: %d", rr.Code)
	}
	tok := f.invite("bob@x.io", "editor")
	if _, err := f.svc.AcceptInvite(context.Background(), tok, membership.Actor{Subject: "bob"}); err != nil {
		t.Fatal(err)
	}
	if rr := do(t, f.mux, stale, http.MethodPost, "/v1/tenant/members/bob/remove", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("removal on a stale sign-in: %d", rr.Code)
	}
	if rr := do(t, f.mux, stale, http.MethodPost, "/v1/tenant/members/bob/suspend", nil); rr.Code != http.StatusOK {
		t.Fatalf("suspending an editor needs no step-up: %d %s", rr.Code, rr.Body.String())
	}
	dev := auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodDeveloper, MemberRole: membership.RoleAdmin}
	if rr := do(t, f.mux, dev, http.MethodPut, "/v1/tenant/members/bob/role", map[string]any{"role": "admin"}); rr.Code != http.StatusForbidden {
		t.Fatalf("promotion to admin from a CLI session: %d (no sign-in to repeat)", rr.Code)
	}
}

func TestRemovalRevokesAccountKeysAndCLISessions(t *testing.T) {
	f := newMembersFixture(t)
	ctx := context.Background()
	tok := f.invite("bob@x.io", "admin")
	if _, err := f.svc.AcceptInvite(ctx, tok, membership.Actor{Subject: "bob"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, rec := range []auth.APIKeyRecord{
		{ID: "acct", Tenant: "t1", Subject: "bob", Roles: []string{"tenant_owner"}, CreatedAt: now},
		{ID: "reader", Tenant: "t1", Subject: "bob", Roles: []string{auth.RoleAccountReader}, CreatedAt: now},
		{ID: "agent", Tenant: "t1", Subject: "bob", Roles: []string{"ops_agent"}, AgentID: "a1", CreatedAt: now},
		{ID: "other", Tenant: "t1", Subject: "alice", Roles: []string{"admin"}, CreatedAt: now},
	} {
		rec.KeyHash[0] = rec.ID[0]
		rec.KeyHash[1] = byte(len(rec.ID))
		if err := f.keys.Insert(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.sessions.Insert(ctx, auth.DeveloperSession{ID: "ds1", Subject: "bob", Tenant: "t1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rr := do(t, f.mux, session("alice", membership.RoleAdmin, true), http.MethodPost, "/v1/tenant/members/bob/remove", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rr.Code, rr.Body.String())
	}
	active, _ := f.keys.ListActive(ctx, "t1")
	var left []string
	for _, k := range active {
		left = append(left, k.ID)
	}
	if strings.Join(left, ",") != "agent,other" && strings.Join(left, ",") != "other,agent" {
		t.Fatalf("active keys after removal = %v; the account keys (read-only too) go, the agent key stays", left)
	}
	if ss, _ := f.sessions.ListActiveForSubject(ctx, "bob", now); len(ss) != 0 {
		t.Fatalf("bob's CLI session survived removal: %v", ss)
	}
	evs, _ := f.svc.Store.Audit(ctx, "t1", 1)
	if len(evs) != 1 || evs[0].Action != membership.AuditKeysRevoked || evs[0].NewValue != "keys=2,developer_sessions=1" {
		t.Fatalf("audit = %+v", evs)
	}
}

func TestLastAdminAndSelfChangesOverHTTP(t *testing.T) {
	f := newMembersFixture(t)
	alice := session("alice", membership.RoleAdmin, true)
	if rr := do(t, f.mux, alice, http.MethodPost, "/v1/tenant/leave", nil); rr.Code != http.StatusConflict ||
		decode(t, rr.Body.String())["error"] != "last_admin" {
		t.Fatalf("last admin leaving: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, f.mux, alice, http.MethodPut, "/v1/tenant/governance", map[string]any{"separation_of_duties": true}); rr.Code != http.StatusConflict ||
		decode(t, rr.Body.String())["error"] != "no_approver" {
		t.Fatalf("SoD without an approver: %d %s", rr.Code, rr.Body.String())
	}
	key := auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"admin"}}
	if rr := do(t, f.mux, key, http.MethodPost, "/v1/tenant/invites", map[string]any{"email": "k@x.io", "role": "viewer"}); rr.Code != http.StatusForbidden {
		t.Fatalf("a key invited a person: %d", rr.Code)
	}
}

func TestTurningSeparationOfDutiesOffTellsApprovers(t *testing.T) {
	f := newMembersFixture(t)
	tok := f.invite("carol@x.io", "approver")
	if _, err := f.svc.AcceptInvite(context.Background(), tok, membership.Actor{Subject: "carol"}); err != nil {
		t.Fatal(err)
	}
	alice := session("alice", membership.RoleAdmin, true)
	if rr := do(t, f.mux, alice, http.MethodPut, "/v1/tenant/governance", map[string]any{"separation_of_duties": true}); rr.Code != http.StatusOK {
		t.Fatalf("on: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, f.mux, session("alice", membership.RoleAdmin, false), http.MethodPut, "/v1/tenant/governance",
		map[string]any{"separation_of_duties": false}); rr.Code != http.StatusForbidden {
		t.Fatalf("off on a stale sign-in: %d", rr.Code)
	}
	f.notifier.notices = nil
	if rr := do(t, f.mux, alice, http.MethodPut, "/v1/tenant/governance", map[string]any{"separation_of_duties": false}); rr.Code != http.StatusOK {
		t.Fatalf("off: %d %s", rr.Code, rr.Body.String())
	}
	if len(f.notifier.notices) != 1 || !strings.HasPrefix(f.notifier.notices[0], "carol@x.io|Separation of duties was turned off") {
		t.Fatalf("notices = %v", f.notifier.notices)
	}
}

// A forwarded link shows: the account that accepted is named when it is
// not the invited address.
func TestAcceptingFromAnotherAddressIsNamed(t *testing.T) {
	f := newMembersFixture(t)
	tok := f.invite("bob@x.io", "viewer")
	f.notifier.notices = nil
	rr := do(t, f.personal, auth.Identity{Subject: "robert", Method: auth.MethodSession}, http.MethodPost, "/v1/invites/accept", map[string]any{"token": tok})
	if rr.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rr.Code, rr.Body.String())
	}
	if len(f.notifier.notices) != 1 || !strings.HasPrefix(f.notifier.notices[0], "alice@x.io|Someone joined") {
		t.Fatalf("notices = %v", f.notifier.notices)
	}
	if !strings.Contains(f.notifier.bodies[0], "accepted by robert@x.io, a different address") {
		t.Fatalf("body = %q", f.notifier.bodies[0])
	}
}

// The approvers' notice for a Card changed under separation of duties
// names the person by email and the Card by the agent that holds it,
// not by internal ids.
func TestCardChangeNoticeIsReadable(t *testing.T) {
	f := newMembersFixture(t)
	tok := f.invite("carol@x.io", "approver")
	if rr := do(t, f.personal, auth.Identity{Subject: "carol", Method: auth.MethodSession}, http.MethodPost, "/v1/invites/accept", map[string]any{"token": tok}); rr.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rr.Code, rr.Body.String())
	}
	f.notifier.notices, f.notifier.bodies = nil, nil
	f.h.NoticeCardChange(context.Background(), "t1", CardChange{Changer: "user_2erin", Agent: "support-agent",
		Action: "REFUND_INVOICE", Entity: "inv_123", Cards: []string{"mnd_7f"}})
	if len(f.notifier.notices) != 1 || !strings.HasPrefix(f.notifier.notices[0], "carol@x.io|A Card was changed") {
		t.Fatalf("notices = %v", f.notifier.notices)
	}
	want := "user_2erin@x.io changed a Card held by support-agent so the held REFUND_INVOICE on inv_123 could go through."
	if !strings.HasPrefix(f.notifier.bodies[0], want) || !strings.Contains(f.notifier.bodies[0], "Card: mnd_7f.") {
		t.Fatalf("body = %q", f.notifier.bodies[0])
	}
}

// Seats are the people who run things: the trial and
// Solo have one admin or editor, and a second is refused, by invitation or
// by a role change. Approvers and viewers are free, and Team's seats
// follow its people.
func TestSeatsAreThePeopleWhoRunThings(t *testing.T) {
	ctx := context.Background()
	for _, plan := range []string{"solo", "trial", "team"} {
		tenants := tenant.NewMemoryStore()
		id := tenant.NewID()
		if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: plan, Status: tenant.StatusActive,
			Metadata: map[string]any{tenant.MetadataKeyPlan: plan, tenant.MetadataKeyAccountName: "Acme"}}); err != nil {
			t.Fatal(err)
		}
		svc := membership.NewService(membership.NewMemoryStore())
		if _, err := svc.EnsureFounder(ctx, id, "alice"); err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		NewMembersHandler(svc, tenants, WithMembersStepUp(&auth.StepUp{}), WithMembersDispatch(func(fn func()) { fn() })).Register(mux)
		alice := session("alice", membership.RoleAdmin, true)
		alice.Tenant = id
		invite := func(email, role string) int {
			return do(t, mux, alice, http.MethodPost, "/v1/tenant/invites", map[string]any{"email": email, "role": role}).Code
		}
		// Free on every plan.
		if code := invite("ann@x.io", "approver"); code != http.StatusCreated {
			t.Fatalf("%s: an approver: %d", plan, code)
		}
		if code := invite("vic@x.io", "viewer"); code != http.StatusCreated {
			t.Fatalf("%s: a viewer: %d", plan, code)
		}
		code := invite("bob@x.io", "editor")
		if plan == "team" {
			if code != http.StatusCreated {
				t.Fatalf("team: an editor: %d", code)
			}
			continue
		}
		if code != http.StatusConflict {
			t.Fatalf("%s: a second person who runs things was invited: %d", plan, code)
		}
		// A role change that would take a seat is refused too.
		if err := svc.Store.Mutate(ctx, id, func(tx membership.Tx) error {
			return tx.InsertMember(membership.Member{TenantID: id, Subject: "carl", Role: membership.RoleViewer, Status: membership.StatusActive})
		}); err != nil {
			t.Fatal(err)
		}
		rr := do(t, mux, alice, http.MethodPut, "/v1/tenant/members/carl/role", map[string]any{"role": "editor"})
		if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "Your plan has one person who runs things") {
			t.Fatalf("%s: viewer to editor: %d %s", plan, rr.Code, rr.Body.String())
		}
		if rr := do(t, mux, alice, http.MethodPut, "/v1/tenant/members/carl/role", map[string]any{"role": "approver"}); rr.Code != http.StatusOK {
			t.Fatalf("%s: viewer to approver: %d %s", plan, rr.Code, rr.Body.String())
		}
	}
}

// The people invited see the account by its name, never its owner's email:
// an account nobody named is named before anyone is
// invited to it.
func TestAnAccountIsNamedBeforeAnyoneIsInvited(t *testing.T) {
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "unnamed", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyPlan: "team"}}); err != nil {
		t.Fatal(err)
	}
	svc := membership.NewService(membership.NewMemoryStore())
	if _, err := svc.EnsureFounder(ctx, id, "alice"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewMembersHandler(svc, tenants, WithMembersStepUp(&auth.StepUp{}), WithMembersDispatch(func(fn func()) { fn() })).Register(mux)
	alice := session("alice", membership.RoleAdmin, true)
	alice.Tenant = id
	rr := do(t, mux, alice, http.MethodPost, "/v1/tenant/invites", map[string]any{"email": "vic@x.io", "role": "viewer"})
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "account_unnamed") {
		t.Fatalf("an unnamed account's invite: %d %s", rr.Code, rr.Body.String())
	}
	if err := tenants.SetMetadata(ctx, id, tenant.MetadataKeyAccountName, "Acme"); err != nil {
		t.Fatal(err)
	}
	if rr := do(t, mux, alice, http.MethodPost, "/v1/tenant/invites", map[string]any{"email": "vic@x.io", "role": "viewer"}); rr.Code != http.StatusCreated {
		t.Fatalf("once named: %d %s", rr.Code, rr.Body.String())
	}
}

// The person invited never sees the inviter's email:
// not in the invitation email, not on the page that previews it, and not
// once they're on the account.
func TestTheInvitedNeverSeeTheInvitersEmail(t *testing.T) {
	f := newMembersFixture(t)
	tok := f.invite("bob@x.io", "viewer") // alice, whose email is alice@x.io, invites
	if len(f.notifier.sent) != 1 || strings.Contains(f.notifier.sent[0].InvitedBy, "alice") || f.notifier.sent[0].InvitedBy != "An admin" {
		t.Fatalf("invitation email names the inviter as %+v", f.notifier.sent)
	}
	bob := auth.Identity{Subject: "bob", Method: auth.MethodSession}
	rr := do(t, f.personal, bob, http.MethodPost, "/v1/invites/preview", map[string]any{"token": tok})
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "alice") {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, f.personal, bob, http.MethodPost, "/v1/invites/accept", map[string]any{"token": tok}); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "alice") {
		t.Fatalf("accept: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, f.personal, bob, http.MethodGet, "/v1/me/memberships", nil); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "alice") {
		t.Fatalf("memberships: %d %s", rr.Code, rr.Body.String())
	}
}
