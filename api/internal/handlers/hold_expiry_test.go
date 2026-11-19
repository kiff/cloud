package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/owneremail"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

// the cards-for-any-agent design at the API: a Card's hold expiry is set and kept through
// reissues, a held action past its expiry lists as expired and cannot be
// answered, and the decide path refuses it as mandate_owner_expired.

func TestIssueHoldExpiry(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	body := func(hold string) map[string]any {
		b := map[string]any{
			"id": "card-h", "subject": "agent-a", "domain": "refund-control",
			"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
			"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 500000, "window": "calendar_day"}},
		}
		if hold != "" {
			b["hold_expiry"] = hold
		}
		return b
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body("")); rr.Code != http.StatusCreated ||
		!strings.Contains(rr.Body.String(), `"hold_expiry_seconds":600`) {
		t.Fatalf("new card: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body("24h")); rr.Code != http.StatusCreated ||
		!strings.Contains(rr.Body.String(), `"hold_expiry_seconds":86400`) {
		t.Fatalf("24h: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body("")); rr.Code != http.StatusCreated ||
		!strings.Contains(rr.Body.String(), `"hold_expiry_seconds":86400`) {
		t.Fatalf("a reissue without hold_expiry reset it: %d %s", rr.Code, rr.Body.String())
	}
	for _, bad := range []string{"10", "30s", "8d", "200h", "soon"} {
		if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", body(bad)); rr.Code != http.StatusBadRequest {
			t.Errorf("hold_expiry %q: %d", bad, rr.Code)
		}
	}
}

func TestExpiredHoldAtTheAPI(t *testing.T) {
	store := mandate.NewMemoryStore()
	issued := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return issued })
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	h := NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time { return now }))
	mux := http.NewServeMux()
	h.Register(mux)
	ctx := context.Background()
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
	if err := store.Issue(ctx, card, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	req := mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}}
	d, err := a.Authorize(ctx, "t1", "p-1", req)
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}
	list := func(status string) []map[string]any {
		t.Helper()
		rr := do(t, mux, owner(), http.MethodGet, "/v1/me/card-exceptions?status="+status, nil)
		var out struct {
			Exceptions []map[string]any `json:"exceptions"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("list %s: %s", status, rr.Body.String())
		}
		return out.Exceptions
	}
	if held := list("held"); len(held) != 1 || held[0]["expires_at"] == nil {
		t.Fatalf("held before expiry: %v", held)
	}

	// Ten minutes later, before any retry has recorded it.
	now = now.Add(mandate.DefaultHoldExpiry)
	if held := list("held"); len(held) != 0 {
		t.Fatalf("an expired hold still lists as held: %v", held)
	}
	if exp := list("expired"); len(exp) != 1 || exp[0]["status"] != "expired" {
		t.Fatalf("expired: %v", exp)
	}
	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/approve-once", nil)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "expired") {
		t.Fatalf("late approval: %d %s", rr.Code, rr.Body.String())
	}
	if got, err := a.Authorize(ctx, "t1", "p-1", req); err != nil || got.Outcome != mandate.OutcomeOwnerExpired {
		t.Fatalf("retry after expiry: %+v %v", got, err)
	}
}

// The decide path refuses an expired hold as blocked, with its own reason
// and the exception handle marked expired.
func TestGateMapsExpiredHold(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 10, 0, 0, time.UTC)
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeOwnerExpired, ExceptionID: "exc-1", ExceptionExpiresAt: &at}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}
	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if !refused || resp.Outcome != string(outcomeBlocked) || len(resp.Reasons) != 1 || resp.Reasons[0] != "mandate_owner_expired" ||
		resp.Exception == nil || resp.Exception.Status != "expired" || resp.Exception.ExpiresAt == nil {
		t.Fatalf("owner_expired: %+v", resp)
	}
	held := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeOwnerRequired, ExceptionID: "exc-1", ExceptionExpiresAt: &at}}
	h = &ProposalsHandler{mandates: held, logger: testLogger(), tenants: noRuntime{}}
	resp, _ = h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if resp.Exception == nil || resp.Exception.ExpiresAt == nil || !resp.Exception.ExpiresAt.Equal(at) {
		t.Fatalf("held response lacks its expiry: %+v", resp.Exception)
	}
}

type fixedOwner struct {
	email string
	err   error
	calls int
}

func (f *fixedOwner) Email(context.Context, string) (string, error) {
	f.calls++
	return f.email, f.err
}

// the cards-for-any-agent design: the decide path emails the owner when work is first held,
// with an absolute Needs you link, and not on retries of it.
func TestHeldActionEmailsTheOwnerOnce(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 10, 0, 0, time.UTC)
	first := mandate.Decision{Outcome: mandate.OutcomeOwnerRequired, ExceptionID: "exc-1", ExceptionExpiresAt: &at, NewlyHeld: true,
		NextSteps: []string{"return_first"},
		Breaches: []mandate.Breach{{MandateID: "card-r", Outcome: mandate.OutcomePerActionExceeded,
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_eur"}, Limit: 500, Draw: 4700}}}
	stub := &stubMandates{decision: first}
	owner := &fixedOwner{email: "owner@example.com"}
	notifier := &recordingNotifier{}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}
	WithProposalsHoldEmail(owner, notifier, "https://app.kiff.dev/")(h)
	h.dispatch = func(f func()) { f() }

	h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if len(notifier.held) != 1 || notifier.heldTo[0] != "owner@example.com" {
		t.Fatalf("emails = %v to %v", notifier.held, notifier.heldTo)
	}
	got := notifier.held[0]
	if got.ReviewURL != "https://app.kiff.dev/needs-you/exc-1" || got.Agent != "agent-1" || got.Amount != "4700 amount_eur" ||
		!got.ExpiresAt.Equal(at) || len(got.Outside) != 1 || got.Outside[0] != "card-r allows up to 500 amount_eur per action" ||
		len(got.NextSteps) != 1 || got.NextSteps[0] != "return_first" {
		t.Fatalf("email = %+v", got)
	}

	retry := first
	retry.NewlyHeld = false
	stub.decision = retry
	h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if len(notifier.held) != 1 {
		t.Fatalf("a retry emailed again: %d", len(notifier.held))
	}

	// No owner email: skipped cleanly, the decision unchanged.
	stub.decision = first
	owner.err = errors.New("no email")
	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if !refused || resp.Outcome != "owner_required" || len(notifier.held) != 1 {
		t.Fatalf("owner without email: %+v, emails %d", resp, len(notifier.held))
	}
}

// Settings → Notifications: an account that turned held-action emails off
// gets none (its holds still show in Needs you); turned back on, it does.
func TestHeldActionEmailFollowsTheAccountsSetting(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 10, 0, 0, time.UTC)
	held := mandate.Decision{Outcome: mandate.OutcomeOwnerRequired, ExceptionID: "exc-1", ExceptionExpiresAt: &at, NewlyHeld: true,
		Breaches: []mandate.Breach{{MandateID: "card-r", Outcome: mandate.OutcomePerActionExceeded,
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_eur"}, Limit: 500, Draw: 4700}}}
	id := keyBoundTo("agent-1")
	id.Tenant = "01J9Z3Q8M5W7K2T4R6Y8A0C1D2"
	store := tenant.NewMemoryStore()
	if err := store.Create(context.Background(), tenant.Tenant{ID: id.Tenant, Slug: "eval", Status: tenant.StatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMetadata(context.Background(), id.Tenant, notifyPrefsMetaKey, `{"approval":true,"runtime_idle":true,"held_email":false}`); err != nil {
		t.Fatal(err)
	}
	notifier := &recordingNotifier{}
	h := &ProposalsHandler{mandates: &stubMandates{decision: held}, logger: testLogger(), tenants: noRuntime{}}
	WithProposalsHoldEmail(&fixedOwner{email: "owner@example.com"}, notifier, "https://app.kiff.dev/")(h)
	WithProposalsTenantStore(store)(h)
	h.dispatch = func(f func()) { f() }

	resp, refused := h.mandateRefusal(context.Background(), id, propForTest("agent-1"), domain.Spec{}, "PAID")
	if !refused || resp.Outcome != "owner_required" {
		t.Fatalf("the hold itself changed: %+v", resp)
	}
	if len(notifier.held) != 0 {
		t.Fatalf("emailed with held_email off: %v", notifier.heldTo)
	}

	if err := store.SetMetadata(context.Background(), id.Tenant, notifyPrefsMetaKey, `{"held_email":true}`); err != nil {
		t.Fatal(err)
	}
	h.mandateRefusal(context.Background(), id, propForTest("agent-1"), domain.Spec{}, "PAID")
	if len(notifier.held) != 1 {
		t.Fatalf("emails with held_email on = %d", len(notifier.held))
	}
}

// Two people answer the account's holds: each email is in the language
// their app last showed them (personlang), and someone not yet seen reads
// the account's last language.
func TestHeldActionEmailIsInEachRecipientsLanguage(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 10, 0, 0, time.UTC)
	held := mandate.Decision{Outcome: mandate.OutcomeOwnerRequired, ExceptionID: "exc-1", ExceptionExpiresAt: &at, NewlyHeld: true,
		Breaches: []mandate.Breach{{MandateID: "card-r", Outcome: mandate.OutcomePerActionExceeded,
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_eur"}, Limit: 500, Draw: 4700}}}
	id := keyBoundTo("agent-1")
	id.Tenant = "01J9Z3Q8M5W7K2T4R6Y8A0C1D2"
	store := tenant.NewMemoryStore()
	if err := store.Create(context.Background(), tenant.Tenant{ID: id.Tenant, Slug: "eval", Status: tenant.StatusActive}); err != nil {
		t.Fatal(err)
	}
	rec := &personlang.Recorder{Tenants: store, Go: func(f func()) { f() }}
	rec.Note(id.Tenant, "ana", words.DE)
	rec.Note(id.Tenant, "bo", words.FR)
	notifier := &recordingNotifier{}
	h := &ProposalsHandler{mandates: &stubMandates{decision: held}, logger: testLogger(), tenants: noRuntime{}}
	owners := &subjectOwners{recipients: []owneremail.Recipient{{Email: "ana@example.com", Subject: "ana"}, {Email: "bo@example.com", Subject: "bo"},
		{Email: "cy@example.com", Subject: "cy"}}}
	WithProposalsHoldEmail(owners, notifier, "https://app.kiff.dev/")(h)
	WithProposalsTenantStore(store)(h)
	h.dispatch = func(f func()) { f() }

	h.mandateRefusal(context.Background(), id, propForTest("agent-1"), domain.Spec{}, "PAID")
	if len(notifier.held) != 3 {
		t.Fatalf("emails = %d", len(notifier.held))
	}
	for i, want := range []words.Lang{words.DE, words.FR, words.FR} {
		if notifier.held[i].Lang != want {
			t.Errorf("%s: %s, want %s", notifier.heldTo[i], notifier.held[i].Lang, want)
		}
	}
}

type subjectOwners struct{ recipients []owneremail.Recipient }

func (s *subjectOwners) Email(context.Context, string) (string, error) {
	return s.recipients[0].Email, nil
}
func (s *subjectOwners) HoldRecipientsWithSubjects(context.Context, string) ([]owneremail.Recipient, error) {
	return s.recipients, nil
}
