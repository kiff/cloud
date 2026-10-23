package membership_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/membership"
)

type fixture struct {
	t   *testing.T
	ctx context.Context
	st  *membership.MemoryStore
	svc *membership.Service
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	f.st = membership.NewMemoryStore().WithClock(func() time.Time { return f.now })
	f.svc = membership.NewService(f.st)
	return f
}

func (f *fixture) actor(subject string) membership.Actor {
	return membership.Actor{Subject: subject, AuthMethod: "session"}
}

// join invites subject as role on behalf of admin and accepts.
func (f *fixture) join(tenant, admin, subject string, role membership.Role) membership.Member {
	f.t.Helper()
	_, tok, err := f.svc.CreateInvite(f.ctx, tenant, f.actor(admin), membership.InviteRequest{Email: subject + "@example.com", Role: role})
	if err != nil {
		f.t.Fatalf("invite %s: %v", subject, err)
	}
	m, err := f.svc.AcceptInvite(f.ctx, tok, f.actor(subject))
	if err != nil {
		f.t.Fatalf("accept %s: %v", subject, err)
	}
	return m
}

func TestFounderIsAdminAndOthersAreNotMembers(t *testing.T) {
	f := newFixture(t)
	m, err := f.svc.EnsureFounder(f.ctx, "t1", "alice")
	if err != nil || m.Role != membership.RoleAdmin || !m.Active() {
		t.Fatalf("founder = %+v, %v; want an active admin", m, err)
	}
	if again, err := f.svc.EnsureFounder(f.ctx, "t1", "alice"); err != nil || again.ID != m.ID {
		t.Fatalf("EnsureFounder is not idempotent: %+v, %v", again, err)
	}
	if _, err := f.svc.EnsureFounder(f.ctx, "t1", "mallory"); !errors.Is(err, membership.ErrNotMember) {
		t.Fatalf("a second subject on a tenant with members: err = %v, want ErrNotMember (M1)", err)
	}
}

func TestInviteGrantsNothingUntilAcceptedAndIsSingleUse(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.EnsureFounder(f.ctx, "t1", "alice")
	inv, tok, err := f.svc.CreateInvite(f.ctx, "t1", f.actor("alice"), membership.InviteRequest{Email: "bob@example.com", Role: membership.RoleApprover})
	if err != nil {
		t.Fatal(err)
	}
	if inv.TokenHash != membership.HashInviteToken(tok) {
		t.Fatal("the stored hash does not match the token")
	}
	if got := inv.ExpiresAt.Sub(inv.CreatedAt); got != 90*24*time.Hour {
		t.Fatalf("default lifetime = %v, want 90 days", got)
	}
	if _, err := f.st.Live(f.ctx, "t1", "bob"); !errors.Is(err, membership.ErrNotFound) {
		t.Fatal("an unaccepted invite granted a membership (M5)")
	}
	m, err := f.svc.AcceptInvite(f.ctx, tok, f.actor("bob"))
	if err != nil || m.Role != membership.RoleApprover || m.InvitedBy != "alice" {
		t.Fatalf("accept = %+v, %v", m, err)
	}
	if _, err := f.svc.AcceptInvite(f.ctx, tok, f.actor("carol")); !errors.Is(err, membership.ErrInviteNotPending) {
		t.Fatalf("second accept: err = %v, want ErrInviteNotPending (M6)", err)
	}
}

func TestInviteNeverChangesAnExistingRole(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.EnsureFounder(f.ctx, "t1", "alice")
	f.join("t1", "alice", "bob", membership.RoleViewer)
	_, tok, err := f.svc.CreateInvite(f.ctx, "t1", f.actor("alice"), membership.InviteRequest{Email: "bob@example.com", Role: membership.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AcceptInvite(f.ctx, tok, f.actor("bob")); !errors.Is(err, membership.ErrAlreadyMember) {
		t.Fatalf("err = %v, want ErrAlreadyMember (M7)", err)
	}
	if m, _ := f.st.Live(f.ctx, "t1", "bob"); m.Role != membership.RoleViewer {
		t.Fatalf("bob's role = %s, want viewer unchanged", m.Role)
	}
	// Suspended members are reactivated, not re-invited.
	if _, err := f.svc.Suspend(f.ctx, "t1", f.actor("alice"), "bob"); err != nil {
		t.Fatal(err)
	}
	_, tok, _ = f.svc.CreateInvite(f.ctx, "t1", f.actor("alice"), membership.InviteRequest{Email: "bob@example.com", Role: membership.RoleViewer})
	if _, err := f.svc.AcceptInvite(f.ctx, tok, f.actor("bob")); !errors.Is(err, membership.ErrAlreadyMember) {
		t.Fatalf("suspended re-accept: err = %v, want ErrAlreadyMember", err)
	}
}

func TestRemovedMemberRejoinsWithANewRow(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.EnsureFounder(f.ctx, "t1", "alice")
	first := f.join("t1", "alice", "bob", membership.RoleEditor)
	if _, err := f.svc.Remove(f.ctx, "t1", f.actor("alice"), "bob"); err != nil {
		t.Fatal(err)
	}
	if latest, err := f.st.Latest(f.ctx, "t1", "bob"); err != nil || latest.Status != membership.StatusRemoved {
		t.Fatalf("latest after removal = %+v, %v", latest, err)
	}
	second := f.join("t1", "alice", "bob", membership.RoleViewer)
	if second.ID == first.ID {
		t.Fatal("rejoining reused the removed membership row; tenures must stay separate")
	}
}

func TestInviteLifetimes(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.EnsureFounder(f.ctx, "t1", "alice")
	inv, _, err := f.svc.CreateInvite(f.ctx, "t1", f.actor("alice"), membership.InviteRequest{Email: "a@x.io", Role: membership.RoleAdmin})
	if err != nil || inv.ExpiresAt.Sub(inv.CreatedAt) != 72*time.Hour {
		t.Fatalf("admin default = %v, %v; want 72h", inv.ExpiresAt.Sub(inv.CreatedAt), err)
	}
	for _, c := range []struct {
		role membership.Role
		life time.Duration
		ok   bool
	}{
		{membership.RoleAdmin, 73 * time.Hour, false},
		{membership.RoleAdmin, time.Hour, true},
		{membership.RoleViewer, 30 * time.Minute, false},
		{membership.RoleViewer, 91 * 24 * time.Hour, false},
		{membership.RoleApprover, 7 * 24 * time.Hour, true},
	} {
		_, _, err := f.svc.CreateInvite(f.ctx, "t1", f.actor("alice"), membership.InviteRequest{Email: "x@x.io", Role: c.role, Lifetime: c.life})
		if (err == nil) != c.ok {
			t.Errorf("%s for %v: err = %v, want ok=%v", c.role, c.life, err, c.ok)
		}
	}
	// An expired invite cannot be accepted.
	_, tok, _ := f.svc.CreateInvite(f.ctx, "t1", f.actor("alice"), membership.InviteRequest{Email: "late@x.io", Role: membership.RoleViewer, Lifetime: time.Hour})
	f.now = f.now.Add(2 * time.Hour)
	if _, err := f.svc.AcceptInvite(f.ctx, tok, f.actor("late")); !errors.Is(err, membership.ErrInviteNotPending) {
		t.Fatalf("expired accept: err = %v", err)
	}
}

func TestLastAdminCannotBeLost(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.EnsureFounder(f.ctx, "t1", "alice")
	f.join("t1", "alice", "bob", membership.RoleEditor)
	if _, err := f.svc.Suspend(f.ctx, "t1", f.actor("alice"), "alice"); !errors.Is(err, membership.ErrLastAdmin) {
		t.Fatalf("suspend last admin: %v", err)
	}
	if _, err := f.svc.Remove(f.ctx, "t1", f.actor("alice"), "alice"); !errors.Is(err, membership.ErrLastAdmin) {
		t.Fatalf("remove last admin: %v", err)
	}
	if _, _, err := f.svc.ChangeRole(f.ctx, "t1", f.actor("alice"), "alice", membership.RoleViewer); !errors.Is(err, membership.ErrLastAdmin) {
		t.Fatalf("demote last admin: %v", err)
	}
	// With a second admin, stepping down is allowed.
	if _, _, err := f.svc.ChangeRole(f.ctx, "t1", f.actor("alice"), "bob", membership.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.ChangeRole(f.ctx, "t1", f.actor("alice"), "alice", membership.RoleViewer); err != nil {
		t.Fatalf("step down with another admin: %v", err)
	}
}

func TestNoOneChangesTheirOwnRoleExceptSteppingDown(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.EnsureFounder(f.ctx, "t1", "alice")
	f.join("t1", "alice", "bob", membership.RoleEditor)
	// The service refuses an editor promoting themselves even before the
	// capability check would.
	if _, _, err := f.svc.ChangeRole(f.ctx, "t1", f.actor("bob"), "bob", membership.RoleApprover); !errors.Is(err, membership.ErrSelfRoleChange) {
		t.Fatalf("self change: %v, want ErrSelfRoleChange (M14)", err)
	}
}

func TestSeparationOfDutiesNeedsAndKeepsAnApprover(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.EnsureFounder(f.ctx, "t1", "alice")
	if _, err := f.svc.SetSeparationOfDuties(f.ctx, "t1", f.actor("alice"), true); !errors.Is(err, membership.ErrNoApprover) {
		t.Fatalf("enable without approver: %v", err)
	}
	f.join("t1", "alice", "carol", membership.RoleApprover)
	if _, err := f.svc.SetSeparationOfDuties(f.ctx, "t1", f.actor("alice"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Suspend(f.ctx, "t1", f.actor("alice"), "carol"); !errors.Is(err, membership.ErrLastApprover) {
		t.Fatalf("suspend last approver: %v", err)
	}
	if _, _, err := f.svc.ChangeRole(f.ctx, "t1", f.actor("alice"), "carol", membership.RoleViewer); !errors.Is(err, membership.ErrLastApprover) {
		t.Fatalf("demote last approver: %v", err)
	}
	if _, err := f.svc.SetSeparationOfDuties(f.ctx, "t1", f.actor("alice"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Suspend(f.ctx, "t1", f.actor("alice"), "carol"); err != nil {
		t.Fatalf("suspend approver with SoD off: %v", err)
	}
}

func TestEveryChangeIsAudited(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.EnsureFounder(f.ctx, "t1", "alice")
	f.join("t1", "alice", "bob", membership.RoleEditor)
	_, _, _ = f.svc.ChangeRole(f.ctx, "t1", f.actor("alice"), "bob", membership.RoleApprover)
	_, _ = f.svc.Suspend(f.ctx, "t1", f.actor("alice"), "bob")
	_, _ = f.svc.Reactivate(f.ctx, "t1", f.actor("alice"), "bob")
	_, _ = f.svc.SetSeparationOfDuties(f.ctx, "t1", f.actor("alice"), true)
	evs, _ := f.st.Audit(f.ctx, "t1", 0)
	var got []string
	for i := len(evs) - 1; i >= 0; i-- {
		got = append(got, evs[i].Action)
	}
	want := []string{membership.AuditFounded, membership.AuditInvited, membership.AuditInviteAccepted,
		membership.AuditRoleChanged, membership.AuditSuspended, membership.AuditReactivated, membership.AuditSoDEnabled}
	if len(got) != len(want) {
		t.Fatalf("audit = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("audit = %v, want %v", got, want)
		}
	}
	// A refused change writes nothing (M8: the change and its row go together).
	before := len(evs)
	_, _ = f.svc.Remove(f.ctx, "t1", f.actor("alice"), "alice")
	if evs, _ = f.st.Audit(f.ctx, "t1", 0); len(evs) != before {
		t.Fatal("a refused change left an audit row")
	}
}

func TestCapabilityMatrix(t *testing.T) {
	type row struct {
		c                               membership.Capability
		admin, editor, approver, viewer bool
	}
	for _, r := range []row{
		{membership.CapRead, true, true, true, true},
		{membership.CapCardsManage, true, true, false, false},
		{membership.CapDomainsManage, true, true, false, false},
		{membership.CapToolsConnect, true, true, false, false},
		{membership.CapHoldsAnswer, true, true, true, false},
		{membership.CapKeysManage, true, false, false, false},
		{membership.CapBilling, true, false, false, false},
		{membership.CapMembersManage, true, false, false, false},
		{membership.CapGovernance, true, false, false, false},
		{membership.CapExport, true, false, false, false},
		{membership.CapDelete, true, false, false, false},
	} {
		for role, want := range map[membership.Role]bool{
			membership.RoleAdmin: r.admin, membership.RoleEditor: r.editor,
			membership.RoleApprover: r.approver, membership.RoleViewer: r.viewer,
		} {
			if got := membership.Can(role, r.c); got != want {
				t.Errorf("Can(%s, %s) = %v, want %v", role, r.c, got, want)
			}
		}
	}
	for _, c := range []struct {
		role membership.Role
		sod  bool
		want bool
	}{
		{membership.RoleAdmin, false, true}, {membership.RoleAdmin, true, false},
		{membership.RoleEditor, false, true}, {membership.RoleEditor, true, false},
		{membership.RoleApprover, true, true}, {membership.RoleViewer, false, false},
	} {
		if got := membership.AnswersHolds(c.role, c.sod); got != c.want {
			t.Errorf("AnswersHolds(%s, sod=%v) = %v, want %v", c.role, c.sod, got, c.want)
		}
	}
}
