package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// the governance-roles design at the hold: separation of duties, proposer ≠ answerer, and
// Card changers never answering the Card's holds.

type holdFixture struct {
	t       *testing.T
	mux     *http.ServeMux
	h       *MandatesHandler
	store   *mandate.MemoryStore
	members *membership.MemoryStore
	svc     *membership.Service
	auth    *mandate.Authorizer
	n       int
	notices []CardChange // approver notices
}

func person(subject string, role membership.Role) auth.Identity {
	return auth.Identity{Subject: subject, Tenant: "t1", Method: auth.MethodSession, MemberRole: role,
		Roles: membership.IdentityRoles(role)}
}

func newHoldFixture(t *testing.T) *holdFixture {
	t.Helper()
	f := &holdFixture{t: t, store: mandate.NewMemoryStore(), members: membership.NewMemoryStore()}
	f.store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	f.svc = membership.NewService(f.members)
	ctx := context.Background()
	if _, err := f.svc.EnsureFounder(ctx, "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	for subject, role := range map[string]membership.Role{"erin": membership.RoleEditor, "carol": membership.RoleApprover,
		"ann": membership.RoleApprover, "vic": membership.RoleViewer} {
		_, tok, err := f.svc.CreateInvite(ctx, "t1", membership.Actor{Subject: "alice"}, membership.InviteRequest{Email: subject + "@x.io", Role: role})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.AcceptInvite(ctx, tok, membership.Actor{Subject: subject}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewMandatesHandler(f.store, nil, WithMandatesMemberships(f.members),
		WithMandatesCardChangeNotice(func(_ context.Context, _ string, c CardChange) {
			f.notices = append(f.notices, c)
		}), WithMandatesClock(func() time.Time {
			return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
		}))
	f.mux, f.h = http.NewServeMux(), h
	h.Register(f.mux)
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
	if err := f.store.Issue(ctx, card, "erin"); err != nil { // the editor issued it
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	f.auth = mandate.NewAuthorizer(f.store, nil, func() time.Time { return now })
	return f
}

// hold makes a fresh held action by subject and returns its id.
func (f *holdFixture) hold(subject string) string {
	f.t.Helper()
	f.n++
	d, err := f.auth.Authorize(context.Background(), "t1", "p-"+string(rune('a'+f.n)), mandate.Request{Subject: subject,
		Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}})
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		f.t.Fatalf("hold: %+v %v", d, err)
	}
	return d.ExceptionID
}

func (f *holdFixture) answer(id auth.Identity, exc string) (int, map[string]any) {
	f.t.Helper()
	rr := do(f.t, f.mux, id, http.MethodPost, "/v1/me/card-exceptions/"+exc+"/approve-once", nil)
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	return rr.Code, body
}

func (f *holdFixture) sod(on bool) {
	f.t.Helper()
	if _, err := f.svc.SetSeparationOfDuties(context.Background(), "t1", membership.Actor{Subject: "alice"}, on); err != nil {
		f.t.Fatal(err)
	}
}

func TestHoldAnswersWithSeparationOfDutiesOff(t *testing.T) {
	f := newHoldFixture(t)
	code, body := f.answer(person("alice", membership.RoleAdmin), f.hold("agent://a"))
	if code != http.StatusOK || body["resolved_role"] != "admin" || body["resolved_under_separation_of_duties"] != false {
		t.Fatalf("admin with SoD off: %d %v", code, body)
	}
	if code, _ := f.answer(person("erin", membership.RoleEditor), f.hold("agent://a")); code != http.StatusOK {
		t.Fatalf("editor with SoD off: %d (a Card changer is only barred with SoD on)", code)
	}
	if code, _ := f.answer(person("vic", membership.RoleViewer), f.hold("agent://a")); code != http.StatusForbidden {
		t.Fatalf("viewer answered: %d", code)
	}
	adminKey := auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"tenant_owner"}}
	if code, _ := f.answer(adminKey, f.hold("agent://a")); code != http.StatusOK {
		t.Fatalf("account key with SoD off keeps today's rules: %d", code)
	}
}

func TestHoldAnswersWithSeparationOfDutiesOn(t *testing.T) {
	f := newHoldFixture(t)
	f.sod(true)
	for name, c := range map[string]struct {
		id   auth.Identity
		code int
		err  string
	}{
		"admin":       {person("alice", membership.RoleAdmin), http.StatusForbidden, "separation_of_duties_role"},
		"editor":      {person("erin", membership.RoleEditor), http.StatusForbidden, "separation_of_duties_role"},
		"account key": {auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"tenant_owner"}}, http.StatusForbidden, "separation_of_duties_key"},
	} {
		code, body := f.answer(c.id, f.hold("agent://a"))
		if code != c.code || body["error"] != c.err {
			t.Errorf("%s: %d %v, want %d %s", name, code, body, c.code, c.err)
		}
	}
	code, body := f.answer(person("carol", membership.RoleApprover), f.hold("agent://a"))
	if code != http.StatusOK || body["resolved_role"] != "approver" || body["resolved_under_separation_of_duties"] != true {
		t.Fatalf("approver: %d %v", code, body)
	}
	// The answer is listed with its attribution.
	rr := do(t, f.mux, person("vic", membership.RoleViewer), http.MethodGet, "/v1/me/card-exceptions?status=approved", nil)
	var list struct {
		Exceptions []map[string]any `json:"exceptions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list.Exceptions) != 1 || list.Exceptions[0]["resolved_role"] != "approver" {
		t.Fatalf("viewer reads the statement with attribution: %d %s", rr.Code, rr.Body.String())
	}
}

// A person who changed a Card never answers its holds with SoD on,
// whatever their role is now: raising a limit, stepping down to
// approver and approving the next hold does not work.
func TestCardChangerNeverAnswersItsHolds(t *testing.T) {
	f := newHoldFixture(t)
	ctx := context.Background()
	// erin issued card-x as editor; alice makes erin an approver. ann was
	// an editor too, but never changed card-x: the rule is about the act,
	// not a past role.
	for subject, roles := range map[string][]membership.Role{
		"erin": {membership.RoleApprover},
		"ann":  {membership.RoleEditor, membership.RoleApprover},
	} {
		for _, role := range roles {
			if _, _, err := f.svc.ChangeRole(ctx, "t1", membership.Actor{Subject: "alice"}, subject, role); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.sod(true)
	code, body := f.answer(person("erin", membership.RoleApprover), f.hold("agent://a"))
	if code != http.StatusForbidden || body["error"] != "card_changer" {
		t.Fatalf("former editor now approver: %d %v", code, body)
	}
	if code, _ := f.answer(person("ann", membership.RoleApprover), f.hold("agent://a")); code != http.StatusOK {
		t.Fatalf("a former editor who never touched the Card: %d", code)
	}
}

func TestNobodyAnswersAHoldTheyProposed(t *testing.T) {
	f := newHoldFixture(t)
	card := mandate.Mandate{ID: "card-c", TenantID: "t1", Subject: "actor-carol", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}}}
	if err := f.store.Issue(context.Background(), card, "erin"); err != nil {
		t.Fatal(err)
	}
	exc := f.hold("actor-carol") // carol proposed this from her own session
	code, body := f.answer(person("carol", membership.RoleApprover), exc)
	if code != http.StatusForbidden || body["error"] != "self_review" {
		t.Fatalf("proposer answered: %d %v (M10)", code, body)
	}
	if code, _ := f.answer(person("ann", membership.RoleApprover), exc); code != http.StatusOK {
		t.Fatalf("another approver: %d", code)
	}
}

func TestChangeCardIsACardEdit(t *testing.T) {
	f := newHoldFixture(t)
	f.sod(true)
	rr := do(t, f.mux, person("carol", membership.RoleApprover), http.MethodPost, "/v1/me/card-exceptions/"+f.hold("agent://a")+"/change-card", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("approver changed the Card: %d", rr.Code)
	}
	rr = do(t, f.mux, person("erin", membership.RoleEditor), http.MethodPost, "/v1/me/card-exceptions/"+f.hold("agent://a")+"/change-card", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("editor change-card: %d %s", rr.Code, rr.Body.String())
	}
	// Visible, not hidden: its own resolution, and the approvers are told.
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["resolution"] != "change-card" || body["resolved_role"] != "editor" {
		t.Fatalf("attribution = %v", body)
	}
	if len(f.notices) != 1 || f.notices[0].Changer != "erin" || f.notices[0].Agent != "agent://a" ||
		f.notices[0].Action != "REFUND_INVOICE" || len(f.notices[0].Cards) != 1 || f.notices[0].Cards[0] != "card-x" {
		t.Fatalf("approver notices = %v", f.notices)
	}
}

func TestChangeCardWithSeparationOfDutiesOffTellsNobody(t *testing.T) {
	f := newHoldFixture(t)
	rr := do(t, f.mux, person("erin", membership.RoleEditor), http.MethodPost, "/v1/me/card-exceptions/"+f.hold("agent://a")+"/change-card", nil)
	if rr.Code != http.StatusOK || len(f.notices) != 0 {
		t.Fatalf("SoD off: %d, notices %v", rr.Code, f.notices)
	}
}

type failingAnswers struct{ *membership.MemoryStore }

func (failingAnswers) RecordHoldAnswer(context.Context, membership.HoldAnswer) error {
	return errors.New("disk full")
}

func TestAttributionFailureIsReported(t *testing.T) {
	var got []string
	g := &HoldGate{Memberships: failingAnswers{membership.NewMemoryStore()},
		OnRecordError: func(_ context.Context, tenantID, holdID string, err error) {
			got = append(got, tenantID+"/"+holdID+": "+err.Error())
		}}
	if err := g.Record(context.Background(), person("carol", membership.RoleApprover), "t1", "h1", "approve-once", membership.Governance{}); err == nil {
		t.Fatal("Record hid the failure")
	}
	if len(got) != 1 || got[0] != "t1/h1: disk full" {
		t.Fatalf("reported = %v", got)
	}
}
