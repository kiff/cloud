package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/owneremail"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// opsSupportFixture builds an OpsHandler with the support store wired
// (WithOpsSupport) and seeds two tickets from two different tenants so
// the tests can assert the operator sees across tenants. Returns the
// mux, the store, and the two ticket ids (t1 from tenant A, t2 from
// tenant B).
func opsSupportFixture(t *testing.T) (*http.ServeMux, *support.MemoryStore, string, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	store := support.NewMemoryStore()
	t1 := tenant.NewID()
	t2 := tenant.NewID()
	if err := store.CreateTicket(ctx, support.Ticket{
		ID: t1, TenantID: "tenant-a", AuthorSubject: "user-a",
		Title: "Cannot deploy", Body: "deploy fails", Category: "bug",
		Status: support.StatusOpen, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("create t1: %v", err)
	}
	if err := store.CreateTicket(ctx, support.Ticket{
		ID: t2, TenantID: "tenant-b", AuthorSubject: "user-b",
		Title: "Billing question", Body: "how does metering work", Category: "question",
		Status: support.StatusOpen, CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour),
	}); err != nil {
		t.Fatalf("create t2: %v", err)
	}

	h := NewOpsHandler([]string{opsSubject}, tenant.NewMemoryStore(), usage.NewMemoryStore(), receipt.NewMemoryStore(),
		nil, nil, nil, WithOpsSupport(store, ""))
	if h == nil {
		t.Fatal("nil handler")
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, store, t1, t2
}

func TestOpsSupport_ListAllCrossTenant(t *testing.T) {
	t.Parallel()
	mux, _, _, t2 := opsSupportFixture(t)
	rec := doOps(mux, "/v1/ops/support", opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Tickets []support.Ticket `json:"tickets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Tickets) != 2 {
		t.Fatalf("tickets: got %d, want 2 (cross-tenant)", len(got.Tickets))
	}
	// Most-recently-updated first: t2 (-1h) before t1 (-2h).
	if got.Tickets[0].ID != t2 {
		t.Errorf("expected t2 first, got %s", got.Tickets[0].ID)
	}
}

func TestOpsSupport_ListStatusFilter(t *testing.T) {
	t.Parallel()
	mux, store, t1, _ := opsSupportFixture(t)
	if err := store.SetStatus(context.Background(), t1, support.StatusInProgress); err != nil {
		t.Fatalf("set status: %v", err)
	}
	rec := doOps(mux, "/v1/ops/support?status=in_progress", opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered list: got %d", rec.Code)
	}
	var got struct {
		Tickets []support.Ticket `json:"tickets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Tickets) != 1 || got.Tickets[0].ID != t1 {
		t.Fatalf("expected only t1 in_progress, got %+v", got.Tickets)
	}
}

func TestOpsSupport_ListInvalidStatusFilter(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := opsSupportFixture(t)
	rec := doOps(mux, "/v1/ops/support?status=bogus", opsSubject)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus filter: got %d, want 400", rec.Code)
	}
}

func TestOpsSupport_Detail(t *testing.T) {
	t.Parallel()
	mux, store, t1, _ := opsSupportFixture(t)
	if err := store.AddMessage(context.Background(), support.Message{
		ID: tenant.NewID(), TicketID: t1, AuthorKind: support.AuthorUser,
		AuthorSubject: "user-a", Body: "more detail", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("add message: %v", err)
	}
	rec := doOps(mux, "/v1/ops/support/"+t1, opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: got %d body=%s", rec.Code, rec.Body.String())
	}
	var got ticketDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ticket.ID != t1 || got.Ticket.Title != "Cannot deploy" {
		t.Errorf("ticket: %+v", got.Ticket)
	}
	if len(got.Messages) != 1 || got.Messages[0].Body != "more detail" {
		t.Errorf("messages: %+v", got.Messages)
	}
}

func TestOpsSupport_DetailUnknownIs404(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := opsSupportFixture(t)
	rec := doOps(mux, "/v1/ops/support/"+tenant.NewID(), opsSubject)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ticket: got %d, want 404", rec.Code)
	}
}

func TestOpsSupport_ReplySetsAnswered(t *testing.T) {
	t.Parallel()
	mux, store, t1, _ := opsSupportFixture(t)
	rec := doOpsPost(mux, "/v1/ops/support/"+t1+"/reply", opsSubject, `{"body":"here is the fix"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply: got %d body=%s", rec.Code, rec.Body.String())
	}
	var m support.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.AuthorKind != support.AuthorOperator || m.AuthorSubject != opsSubject {
		t.Errorf("reply attribution wrong: %+v", m)
	}
	// Ticket moved to answered.
	got, err := store.GetByID(context.Background(), t1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != support.StatusAnswered {
		t.Errorf("status: got %s, want answered", got.Status)
	}
	// Message is on the thread.
	msgs, _ := store.ListMessages(context.Background(), t1)
	if len(msgs) != 1 || msgs[0].Body != "here is the fix" {
		t.Errorf("thread: %+v", msgs)
	}
}

func TestOpsSupport_ReplyEmptyBody400(t *testing.T) {
	t.Parallel()
	mux, _, t1, _ := opsSupportFixture(t)
	rec := doOpsPost(mux, "/v1/ops/support/"+t1+"/reply", opsSubject, `{"body":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body: got %d, want 400", rec.Code)
	}
}

func TestOpsSupport_ReplyUnknownTicket404(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := opsSupportFixture(t)
	rec := doOpsPost(mux, "/v1/ops/support/"+tenant.NewID()+"/reply", opsSubject, `{"body":"hello"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ticket reply: got %d, want 404", rec.Code)
	}
}

func TestOpsSupport_SetStatus(t *testing.T) {
	t.Parallel()
	mux, store, t1, _ := opsSupportFixture(t)
	rec := doOpsPost(mux, "/v1/ops/support/"+t1+"/status", opsSubject, `{"status":"closed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set status: got %d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetByID(context.Background(), t1)
	if got.Status != support.StatusClosed {
		t.Errorf("status: got %s, want closed", got.Status)
	}
}

func TestOpsSupport_SetStatusInvalid400(t *testing.T) {
	t.Parallel()
	mux, _, t1, _ := opsSupportFixture(t)
	rec := doOpsPost(mux, "/v1/ops/support/"+t1+"/status", opsSubject, `{"status":"nonsense"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status: got %d, want 400", rec.Code)
	}
}

func TestOpsSupport_SetStatusUnknownTicket404(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := opsSupportFixture(t)
	rec := doOpsPost(mux, "/v1/ops/support/"+tenant.NewID()+"/status", opsSubject, `{"status":"closed"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ticket status: got %d, want 404", rec.Code)
	}
}

func TestOpsSupport_NonOperatorGets404(t *testing.T) {
	t.Parallel()
	mux, _, t1, _ := opsSupportFixture(t)
	paths := []string{"/v1/ops/support", "/v1/ops/support/" + t1}
	for _, p := range paths {
		if rec := doOps(mux, p, tenantSubject); rec.Code != http.StatusNotFound {
			t.Errorf("%s as tenant: got %d, want 404", p, rec.Code)
		}
		if rec := doOps(mux, p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s anon: got %d, want 404", p, rec.Code)
		}
	}
	// Mutations too.
	if rec := doOpsPost(mux, "/v1/ops/support/"+t1+"/reply", tenantSubject, `{"body":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("reply as tenant: got %d, want 404", rec.Code)
	}
	if rec := doOpsPost(mux, "/v1/ops/support/"+t1+"/status", tenantSubject, `{"status":"closed"}`); rec.Code != http.StatusNotFound {
		t.Errorf("status as tenant: got %d, want 404", rec.Code)
	}
}

func TestOpsSupport_RoutesUnmountedWithoutStore(t *testing.T) {
	t.Parallel()
	// No WithOpsSupport → support routes not registered, so an
	// operator hitting them gets 404 (unknown path).
	mux, _, _ := opsTestFixture(t)
	if rec := doOps(mux, "/v1/ops/support", opsSubject); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (route not mounted)", rec.Code)
	}
}

// opsSupportNotifyFixture wires the support store together with a
// recording notifier and a synchronous dispatch so the close-the-loop
// email is deterministic. Seeds one ticket whose submitter email is
// set. Returns the mux, the notifier, and the ticket id.
func opsSupportNotifyFixture(t *testing.T, authorEmail string) (*http.ServeMux, *recordingNotifier, string) {
	t.Helper()
	store := support.NewMemoryStore()
	id := tenant.NewID()
	now := time.Now().UTC()
	if err := store.CreateTicket(context.Background(), support.Ticket{
		ID: id, TenantID: "tenant-a", AuthorSubject: "user-a", AuthorEmail: authorEmail,
		Title: "Help", Body: "please", Category: "bug",
		Status: support.StatusOpen, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	notifier := &recordingNotifier{}
	resolver := owneremail.NewResolver(fakeSubjectLookup{subject: "s"}, fakeEmailLookup{email: "x@example.com"})
	h := NewOpsHandler([]string{opsSubject}, tenant.NewMemoryStore(), usage.NewMemoryStore(), receipt.NewMemoryStore(),
		nil, nil, nil,
		WithOpsSupport(store, "https://app.kiff.dev"),
		WithOpsNotifications(resolver, notifier, nil))
	h.dispatch = func(f func()) { f() } // synchronous
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, notifier, id
}

func TestOpsSupport_ReplyNotifiesSubmitter(t *testing.T) {
	t.Parallel()
	mux, notifier, id := opsSupportNotifyFixture(t, "user@example.com")
	rec := doOpsPost(mux, "/v1/ops/support/"+id+"/reply", opsSubject, `{"body":"the answer"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply: got %d", rec.Code)
	}
	if len(notifier.support) != 1 || notifier.support[0] != "user@example.com" {
		t.Fatalf("expected one email to submitter, got %v", notifier.support)
	}
	want := "https://app.kiff.dev/dashboard/support/" + id
	if len(notifier.supportURLs) != 1 || notifier.supportURLs[0] != want {
		t.Fatalf("ticket link: got %v, want %q", notifier.supportURLs, want)
	}
}

func TestOpsSupport_CloseNotifiesSubmitter(t *testing.T) {
	t.Parallel()
	mux, notifier, id := opsSupportNotifyFixture(t, "user@example.com")
	rec := doOpsPost(mux, "/v1/ops/support/"+id+"/status", opsSubject, `{"status":"closed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("close: got %d", rec.Code)
	}
	if len(notifier.support) != 1 || notifier.support[0] != "user@example.com" {
		t.Fatalf("expected one close email, got %v", notifier.support)
	}
}

func TestOpsSupport_InProgressDoesNotNotify(t *testing.T) {
	t.Parallel()
	mux, notifier, id := opsSupportNotifyFixture(t, "user@example.com")
	rec := doOpsPost(mux, "/v1/ops/support/"+id+"/status", opsSubject, `{"status":"in_progress"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("in_progress: got %d", rec.Code)
	}
	if len(notifier.support) != 0 {
		t.Fatalf("in_progress must not email the submitter, got %v", notifier.support)
	}
}

func TestOpsSupport_NoEmailWhenSubmitterEmailMissing(t *testing.T) {
	t.Parallel()
	mux, notifier, id := opsSupportNotifyFixture(t, "") // no captured email
	rec := doOpsPost(mux, "/v1/ops/support/"+id+"/reply", opsSubject, `{"body":"answer"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply: got %d", rec.Code)
	}
	if len(notifier.support) != 0 {
		t.Fatalf("no email should be sent without a submitter address, got %v", notifier.support)
	}
}

func TestOpsSupport_NotificationFailureDoesNotFailReply(t *testing.T) {
	t.Parallel()
	mux, notifier, id := opsSupportNotifyFixture(t, "user@example.com")
	notifier.fail = true
	rec := doOpsPost(mux, "/v1/ops/support/"+id+"/reply", opsSubject, `{"body":"answer"}`)
	// The reply still succeeds even though the email send failed.
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply must succeed despite email failure: got %d", rec.Code)
	}
	if len(notifier.support) != 1 {
		t.Fatalf("send was attempted once, got %v", notifier.support)
	}
}
