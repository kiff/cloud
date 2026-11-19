package handlers

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/owneremail"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// recordingNotifier records the recipients it was asked to email. Note
// the interface carries ONLY the recipient — there is no parameter
// through which an internal operator reason could reach the customer.
type recordingNotifier struct {
	mu          sync.Mutex
	suspended   []string
	unsuspended []string
	support     []string // recipients notified of a support update
	supportURLs []string // the ticket links included in those emails
	held        []notify.HeldAction
	heldTo      []string
	fail        bool
}

func (n *recordingNotifier) ActionHeld(_ context.Context, to string, h notify.HeldAction) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail {
		return errors.New("resend down")
	}
	n.held = append(n.held, h)
	n.heldTo = append(n.heldTo, to)
	return nil
}

func (n *recordingNotifier) TenantSuspended(_ context.Context, to string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.suspended = append(n.suspended, to)
	if n.fail {
		return errors.New("resend down")
	}
	return nil
}

func (n *recordingNotifier) TenantUnsuspended(_ context.Context, to string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.unsuspended = append(n.unsuspended, to)
	if n.fail {
		return errors.New("resend down")
	}
	return nil
}

func (n *recordingNotifier) AllowanceThresholdReached(context.Context, string, int, int, int) error {
	return nil
}

func (n *recordingNotifier) BudgetReached(context.Context, string, int, int) error { return nil }

func (n *recordingNotifier) SupportTicketUpdated(_ context.Context, to, ticketURL string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.support = append(n.support, to)
	n.supportURLs = append(n.supportURLs, ticketURL)
	if n.fail {
		return errors.New("resend down")
	}
	return nil
}

type fakeSubjectLookup struct {
	subject string
	err     error
}

func (f fakeSubjectLookup) SubjectForTenant(context.Context, string) (string, error) {
	return f.subject, f.err
}

type fakeEmailLookup struct {
	email string
	err   error
}

func (f fakeEmailLookup) EmailForSubject(context.Context, string) (string, error) {
	return f.email, f.err
}

// opsNotifyFixture wires the control plane + customer notifications
// with a synchronous dispatch so the notification outcome is
// deterministic. subj/email configure the resolver fakes.
func opsNotifyFixture(t *testing.T, subj fakeSubjectLookup, email fakeEmailLookup, notifier *recordingNotifier) (*http.ServeMux, *tenant.MemoryStore, *operatoraudit.MemoryStore, string) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	_ = tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "acme", Status: tenant.StatusActive})
	audit := operatoraudit.NewMemoryStore()
	resolver := owneremail.NewResolver(subj, email)
	h := NewOpsHandler([]string{opsSubject}, tenants, usage.NewMemoryStore(), receipt.NewMemoryStore(),
		nil, nil, nil,
		WithOpsControlPlane(audit, &recordingResetter{}),
		WithOpsNotifications(resolver, notifier, nil))
	h.dispatch = func(f func()) { f() } // synchronous for deterministic tests
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, tenants, audit, id
}

func TestOps_Suspend_SendsCustomerEmail(t *testing.T) {
	t.Parallel()
	notifier := &recordingNotifier{}
	mux, tenants, _, id := opsNotifyFixture(t,
		fakeSubjectLookup{subject: "user_abc"}, fakeEmailLookup{email: "owner@example.com"}, notifier)

	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, `{"reason":"internal: 40% blocked"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend: %d", rec.Code)
	}
	if len(notifier.suspended) != 1 || notifier.suspended[0] != "owner@example.com" {
		t.Fatalf("expected one suspend email to owner@example.com, got %v", notifier.suspended)
	}
	// State committed.
	tt, _ := tenants.Get(context.Background(), id)
	if tt.Status != tenant.StatusSuspended {
		t.Fatalf("status: %s", tt.Status)
	}
}

func TestOps_Unsuspend_SendsCustomerEmail(t *testing.T) {
	t.Parallel()
	notifier := &recordingNotifier{}
	mux, _, _, id := opsNotifyFixture(t,
		fakeSubjectLookup{subject: "user_abc"}, fakeEmailLookup{email: "owner@example.com"}, notifier)
	_ = doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, "")
	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/unsuspend", opsSubject, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unsuspend: %d", rec.Code)
	}
	if len(notifier.unsuspended) != 1 || notifier.unsuspended[0] != "owner@example.com" {
		t.Fatalf("expected one unsuspend email, got %v", notifier.unsuspended)
	}
}

func TestOps_Suspend_NotificationFailureDoesNotRollback(t *testing.T) {
	t.Parallel()
	notifier := &recordingNotifier{fail: true}
	mux, tenants, audit, id := opsNotifyFixture(t,
		fakeSubjectLookup{subject: "user_abc"}, fakeEmailLookup{email: "owner@example.com"}, notifier)

	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, "")
	// The action still succeeds even though the email failed.
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend should still 200 on notify failure, got %d", rec.Code)
	}
	tt, _ := tenants.Get(context.Background(), id)
	if tt.Status != tenant.StatusSuspended {
		t.Fatal("suspend rolled back on notify failure")
	}
	if entries, _ := audit.List(context.Background(), id, 10); len(entries) != 1 {
		t.Fatalf("audit row must persist; got %d", len(entries))
	}
}

func TestOps_Suspend_UnresolvableEmailHandledCleanly(t *testing.T) {
	t.Parallel()
	notifier := &recordingNotifier{}
	// No subject mapping → email unresolvable.
	mux, tenants, _, id := opsNotifyFixture(t,
		fakeSubjectLookup{err: tenant.ErrSubjectNotMapped}, fakeEmailLookup{email: "x@example.com"}, notifier)

	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend should 200 even when email unresolvable, got %d", rec.Code)
	}
	// No email attempted (resolver failed before send).
	if len(notifier.suspended) != 0 {
		t.Fatalf("no email should be sent when unresolvable, got %v", notifier.suspended)
	}
	// State still committed.
	tt, _ := tenants.Get(context.Background(), id)
	if tt.Status != tenant.StatusSuspended {
		t.Fatal("suspend must still apply")
	}
}
