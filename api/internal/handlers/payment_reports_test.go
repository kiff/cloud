package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/payreports"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// reportTools are the account's gateway tools: their titles, and their
// owner-approved profiles (create_refund moves money, chat_postMessage
// sends).
type reportTools struct {
	profiles map[string][]byte
	err      error
}

func (reportTools) ToolTitles(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (t reportTools) ToolProfiles(context.Context, string) (map[string][]byte, error) {
	return t.profiles, t.err
}

type reportRig struct {
	mux      *http.ServeMux
	calls    *proposals.MemoryReader
	receipts *receipt.MemoryStore
	halts    *halts.MemoryStore
	planque  *planqueFake
	reports  *payreports.MemoryStore
	outcomes *fakeOutcomes
	tools    *reportTools
}

func newReportRig(t *testing.T) *reportRig {
	t.Helper()
	read := func(name string) []byte {
		raw, err := os.ReadFile("../profile/testdata/valid/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	r := &reportRig{mux: http.NewServeMux(), calls: proposals.NewMemoryReader(), receipts: receipt.NewMemoryStore(),
		halts: halts.NewMemoryStore(), planque: &planqueFake{}, reports: payreports.NewMemoryStore(),
		outcomes: &fakeOutcomes{m: map[string]tooloutcome.Outcome{}},
		tools:    &reportTools{profiles: map[string][]byte{"create_refund": read("stripe-refunds.json"), "chat_postMessage": read("slack-messages.json")}}}
	hh := handlers.NewHaltsHandler(r.halts, r.planque, r.receipts, nil)
	hh.Register(r.mux)
	handlers.NewPaymentReportsHandler(r.receipts, r.calls, r.tools, r.outcomes, r.reports, hh, nil).Register(r.mux)
	handlers.NewControlsHandler(domain.NewMemoryControlStore(), nil, handlers.WithControlsDecisionsReader(r.calls),
		handlers.WithControlsToolOutcomes(r.outcomes), handlers.WithControlsToolTitles(r.tools),
		handlers.WithControlsPaymentReports(r.reports)).Register(r.mux)
	return r
}

// call adds a call of tenant's that KIFF allowed, made by actor with its
// own key unless unverified, with its receipt trace and the tool's outcome.
func (r *reportRig) call(t *testing.T, tenant, pid, actor, action string, verified bool, outcome tooloutcome.Status) string {
	t.Helper()
	trace := "tr-" + pid
	at := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)
	r.calls.Append(tenant, proposals.DecisionRow{ProposalID: pid, ActorID: actor, ActorVerified: verified, ActionName: action,
		Outcome: "allowed", CreatedAt: at, ReceiptTraceID: trace})
	if err := r.receipts.Save(context.Background(), receipt.Receipt{SchemaVersion: receipt.SchemaVersion, TraceID: trace, ProposalID: pid,
		TenantID: tenant, EntityID: "ch_1", EntityType: "charge", ActionName: action, IssuedAt: at}); err != nil {
		t.Fatal(err)
	}
	if outcome != "" {
		r.outcomes.m[pid] = tooloutcome.Outcome{Status: outcome, At: at}
	}
	return trace
}

type reportAnswer struct {
	TraceID    string     `json:"trace_id"`
	ProposalID string     `json:"proposal_id"`
	Agent      string     `json:"agent"`
	ReportedAt time.Time  `json:"reported_at"`
	Halt       haltAnswer `json:"halt"`
	Error      string     `json:"error"`
}

func (r *reportRig) report(t *testing.T, id auth.Identity, trace string, body any) (int, reportAnswer) {
	t.Helper()
	rec := drivePost(t, withFakeIdentity(r.mux, id), "/v1/receipts/"+trace+"/report", body)
	var a reportAnswer
	_ = json.NewDecoder(rec.Result().Body).Decode(&a)
	return rec.Code, a
}

func (r *reportRig) halted(t *testing.T, tenant, agent string) bool {
	t.Helper()
	h, err := r.halts.Get(context.Background(), tenant, agent)
	if err != nil {
		t.Fatal(err)
	}
	return h.On
}

// A payment its owner didn't expect is reported from its receipt: the
// report is kept, and the taf is halted with the owner's halt, confirmed
// by planque and on the Record with its own receipt.
func TestReportingAPaymentHaltsItsTaf(t *testing.T) {
	r := newReportRig(t)
	trace := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	code, a := r.report(t, haltAdmin(), trace, nil)
	if code != http.StatusOK || a.TraceID != trace || a.ProposalID != "p-pay" || a.Agent != "refund-agent" || a.ReportedAt.IsZero() {
		t.Fatalf("report: %d %+v", code, a)
	}
	if !a.Halt.Halted || a.Halt.State != "halted" || !a.Halt.PlanqueAck || a.Halt.Receipt == "" {
		t.Fatalf("the taf isn't halted and confirmed: %+v", a.Halt)
	}
	if len(r.planque.seen) != 1 || !strings.HasPrefix(r.planque.seen[0], "t1/refund-agent@") {
		t.Fatalf("planque asked: %v", r.planque.seen)
	}
	if _, err := r.receipts.Get(context.Background(), "t1", a.Halt.Receipt); err != nil {
		t.Fatalf("the halt's receipt: %v", err)
	}
	evs, _ := r.halts.Events(context.Background(), "t1", 10)
	if len(evs) != 1 || evs[0].Kind != halts.KindHalt || evs[0].AgentID != "refund-agent" || evs[0].By != "alice" {
		t.Fatalf("the Record's halt: %+v", evs)
	}
	got, _ := r.reports.Reported(context.Background(), "t1", []string{trace})
	if !got[trace].Equal(a.ReportedAt) {
		t.Fatalf("the report kept: %v", got)
	}
}

// Reporting again answers the first report; a taf resumed since is
// halted again.
func TestReportingAgainKeepsTheFirstReportAndHaltsAgain(t *testing.T) {
	r := newReportRig(t)
	trace := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	_, first := r.report(t, haltAdmin(), trace, nil)
	code, again := r.report(t, haltAdmin(), trace, nil)
	if code != http.StatusOK || !again.ReportedAt.Equal(first.ReportedAt) || !again.Halt.Halted || again.Halt.Receipt != "" {
		t.Fatalf("again: %d %+v (first %+v)", code, again, first)
	}
	if _, _, err := r.halts.Resume(context.Background(), "t1", "refund-agent", time.Now(), "alice"); err != nil {
		t.Fatal(err)
	}
	code, third := r.report(t, haltAdmin(), trace, nil)
	if code != http.StatusOK || !third.ReportedAt.Equal(first.ReportedAt) || !r.halted(t, "t1", "refund-agent") || third.Halt.Receipt == "" {
		t.Fatalf("after a resume: %d %+v", code, third)
	}
}

// Only the owner or an admin reports, as only they halt: a member who
// can't manage Cards, an agent's key and the account's API key are
// refused, and nothing is halted.
func TestOnlyAnOwnerOrAdminReportsAPayment(t *testing.T) {
	r := newReportRig(t)
	trace := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	viewer := auth.Identity{Subject: "bob", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleViewer,
		Roles: membership.IdentityRoles(membership.RoleViewer)}
	agentKey := auth.Identity{Subject: "key-1", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, AgentID: "refund-agent"}
	accountKey := auth.Identity{Subject: "key-2", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true}
	for name, id := range map[string]auth.Identity{"viewer": viewer, "agent key": agentKey, "account key": accountKey} {
		if code, a := r.report(t, id, trace, nil); code != http.StatusForbidden {
			t.Errorf("%s: %d %+v", name, code, a)
		}
	}
	if r.halted(t, "t1", "refund-agent") {
		t.Fatal("halted on a refused report")
	}
}

// What isn't a payment, or isn't this account's, is never reported nor
// its taf halted: another account's receipt reads as none, a tool that
// doesn't move money or a payment the tool refused or that was never sent
// is not_a_payment, and a call not made with its taf's own key names no
// taf KIFF can halt.
func TestOnlyAPaymentThatWentOutIsReported(t *testing.T) {
	r := newReportRig(t)
	other := r.call(t, "t2", "p-other", "refund-agent", "create_refund", true, tooloutcome.Done)
	message := r.call(t, "t1", "p-msg", "desk-agent", "chat_postMessage", true, tooloutcome.Done)
	refused := r.call(t, "t1", "p-refused", "refund-agent", "create_refund", true, tooloutcome.RefusedByTool)
	unsent := r.call(t, "t1", "p-unsent", "refund-agent", "create_refund", true, tooloutcome.NotSent)
	claimed := r.call(t, "t1", "p-claimed", "refund-agent", "create_refund", false, tooloutcome.Done)
	for name, c := range map[string]struct {
		trace string
		code  int
		err   string
	}{
		"another account's":     {other, http.StatusNotFound, "no such receipt"},
		"no receipt by that id": {"tr-none", http.StatusNotFound, "no such receipt"},
		"a message":             {message, http.StatusConflict, "not_a_payment"},
		"refused by the tool":   {refused, http.StatusConflict, "not_a_payment"},
		"never sent":            {unsent, http.StatusConflict, "not_a_payment"},
		"its taf only claimed":  {claimed, http.StatusConflict, "agent_unknown"},
	} {
		if code, a := r.report(t, haltAdmin(), c.trace, nil); code != c.code || a.Error != c.err {
			t.Errorf("%s: %d %+v", name, code, a)
		}
	}
	for _, agent := range []string{"refund-agent", "desk-agent"} {
		if r.halted(t, "t1", agent) || r.halted(t, "t2", agent) {
			t.Fatalf("%s halted", agent)
		}
	}
	got, _ := r.reports.Reported(context.Background(), "t1", []string{message, refused, unsent, claimed})
	if len(got) != 0 {
		t.Fatalf("reports kept: %v", got)
	}
}

// A payment whose profiles can't be read isn't reported: KIFF can't tell
// it's a payment, and halts nothing on a guess.
func TestAPaymentIsntReportedWhileItsProfileIsUnread(t *testing.T) {
	r := newReportRig(t)
	trace := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	r.tools.err = context.DeadlineExceeded
	if code, a := r.report(t, haltAdmin(), trace, nil); code != http.StatusServiceUnavailable || r.halted(t, "t1", "refund-agent") {
		t.Fatalf("%d %+v", code, a)
	}
}

// A report keeps no free text: a body, a note among it, is read by no
// one and kept nowhere.
func TestAReportKeepsNoNote(t *testing.T) {
	r := newReportRig(t)
	trace := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	code, a := r.report(t, haltAdmin(), trace, map[string]any{"note": "my card number is 4242"})
	if code != http.StatusOK {
		t.Fatalf("%d %+v", code, a)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "4242") {
		t.Fatalf("the note came back: %s", raw)
	}
}

// A trial's call halts its agent, as a halt of the trial does.
func TestATrialsPaymentHaltsItsAgent(t *testing.T) {
	r := newReportRig(t)
	trace := r.call(t, "t1", "p-trial", "refund-agent~trial", "create_refund", true, tooloutcome.Done)
	if code, a := r.report(t, haltAdmin(), trace, nil); code != http.StatusOK || a.Agent != "refund-agent" || !r.halted(t, "t1", "refund-agent") {
		t.Fatalf("%d %+v", code, a)
	}
}

// The Record says which calls are payments an owner may report, and which
// were reported: a sent payment with a receipt, not a message, nor a
// payment the tool refused.
func TestTheRecordMarksPaymentsAndTheirReports(t *testing.T) {
	r := newReportRig(t)
	paid := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	reported := r.call(t, "t1", "p-reported", "refund-agent", "create_refund", true, tooloutcome.Unknown)
	r.call(t, "t1", "p-msg", "desk-agent", "chat_postMessage", true, tooloutcome.Done)
	r.call(t, "t1", "p-refused", "refund-agent", "create_refund", true, tooloutcome.RefusedByTool)
	if code, a := r.report(t, haltAdmin(), reported, nil); code != http.StatusOK {
		t.Fatalf("report: %d %+v", code, a)
	}
	rec := httptest.NewRecorder()
	withFakeIdentity(r.mux, haltAdmin()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/activity", nil))
	var out activityResp
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("activity: %d %v", rec.Code, err)
	}
	seen := map[string]proposals.CallRow{}
	for _, c := range out.Calls {
		seen[c.ProposalID] = c
	}
	for pid, want := range map[string]bool{"p-pay": true, "p-reported": true, "p-msg": false, "p-refused": false} {
		if seen[pid].Payment != want {
			t.Errorf("%s: payment %v, want %v", pid, seen[pid].Payment, want)
		}
	}
	if c := seen["p-reported"]; c.Receipt == nil || c.Receipt.TraceID != reported || c.Receipt.ReportedAt == nil {
		t.Errorf("the reported payment: %+v", c.Receipt)
	}
	if c := seen["p-pay"]; c.Receipt == nil || c.Receipt.TraceID != paid || c.Receipt.ReportedAt != nil {
		t.Errorf("the unreported payment: %+v", c.Receipt)
	}
}

// failingHalts is a halt store that can't keep a halt.
type failingHalts struct{ *halts.MemoryStore }

func (failingHalts) Halt(context.Context, string, string, time.Time, string) (halts.Halt, bool, error) {
	return halts.Halt{}, false, context.DeadlineExceeded
}

// keptReports remembers every report it is asked to keep.
type keptReports struct {
	*payreports.MemoryStore
	asked []payreports.Report
}

func (k *keptReports) Report(ctx context.Context, rep payreports.Report) (payreports.Report, error) {
	k.asked = append(k.asked, rep)
	return k.MemoryStore.Report(ctx, rep)
}

// anotherCall answers every call read with a call other than the one asked.
type anotherCall struct{ proposals.CallsReader }

func (a anotherCall) ListCalls(ctx context.Context, tenant string, q proposals.CallQuery) (proposals.CallsPage, error) {
	q.Proposal = "p-other"
	return a.CallsReader.ListCalls(ctx, tenant, q)
}

// reportWith serves r's report route over other stores.
func (r *reportRig) reportWith(t *testing.T, hs halts.Store, calls proposals.CallsReader, reports payreports.Store, trace string) (int, reportAnswer) {
	t.Helper()
	mux := http.NewServeMux()
	handlers.NewPaymentReportsHandler(r.receipts, calls, r.tools, r.outcomes, reports, handlers.NewHaltsHandler(hs, r.planque, r.receipts, nil), nil).Register(mux)
	rec := drivePost(t, withFakeIdentity(mux, haltAdmin()), "/v1/receipts/"+trace+"/report", nil)
	var a reportAnswer
	_ = json.NewDecoder(rec.Result().Body).Decode(&a)
	return rec.Code, a
}

// A taf that couldn't be halted is never reported: the report exists only
// for a taf KIFF halted.
func TestAReportIsntKeptWhenItsTafCantBeHalted(t *testing.T) {
	r := newReportRig(t)
	trace := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	kept := &keptReports{MemoryStore: payreports.NewMemoryStore()}
	code, a := r.reportWith(t, failingHalts{halts.NewMemoryStore()}, r.calls, kept, trace)
	if code != http.StatusInternalServerError || !strings.Contains(a.Error, "nothing was reported") || len(kept.asked) != 0 {
		t.Fatalf("%d %+v, kept %+v", code, a, kept.asked)
	}
}

// A report names who made it.
func TestAReportNamesWhoReportedIt(t *testing.T) {
	r := newReportRig(t)
	trace := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	kept := &keptReports{MemoryStore: payreports.NewMemoryStore()}
	if code, a := r.reportWith(t, r.halts, r.calls, kept, trace); code != http.StatusOK {
		t.Fatalf("%d %+v", code, a)
	}
	if len(kept.asked) != 1 || kept.asked[0].ReportedBy != "alice" || kept.asked[0].AgentID != "refund-agent" || kept.asked[0].ProposalID != "p-pay" {
		t.Fatalf("kept %+v", kept.asked)
	}
}

// An agent's identity or a key never reports, even one carrying a role
// that manages Cards.
func TestNoAgentNorKeyReportsAPayment(t *testing.T) {
	r := newReportRig(t)
	trace := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	admin := func(id auth.Identity) auth.Identity {
		id.Subject, id.Tenant, id.MemberRole, id.Roles = "alice", "t1", membership.RoleAdmin, membership.IdentityRoles(membership.RoleAdmin)
		return id
	}
	for name, id := range map[string]auth.Identity{
		"an agent's identity":      admin(auth.Identity{Method: auth.MethodSession, AgentID: "refund-agent"}),
		"a key, by its mark":       admin(auth.Identity{Method: auth.MethodSession, APIKey: true}),
		"a key, by how it came in": admin(auth.Identity{Method: auth.MethodAPIKey}),
	} {
		if code, a := r.report(t, id, trace, nil); code != http.StatusForbidden {
			t.Errorf("%s: %d %+v", name, code, a)
		}
	}
	if r.halted(t, "t1", "refund-agent") {
		t.Fatal("halted")
	}
	if got, _ := r.reports.Reported(context.Background(), "t1", []string{trace}); len(got) != 0 {
		t.Fatalf("kept %v", got)
	}
}

// Only a call KIFF allowed is a payment: a held or refused call through a
// tool that moves money, receipt or not, is not_a_payment; so is a call
// other than the one the receipt names, and a taf whose id isn't in its
// normal form names none KIFF can halt.
func TestOnlyTheAllowedCallItsReceiptNamesIsReported(t *testing.T) {
	r := newReportRig(t)
	at := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)
	for pid, outcome := range map[string]string{"p-held": "owner_required", "p-blocked": "blocked"} {
		r.calls.Append("t1", proposals.DecisionRow{ProposalID: pid, ActorID: "refund-agent", ActorVerified: true, ActionName: "create_refund",
			Outcome: outcome, CreatedAt: at, ReceiptTraceID: "tr-" + pid})
		if err := r.receipts.Save(context.Background(), receipt.Receipt{SchemaVersion: receipt.SchemaVersion, TraceID: "tr-" + pid, ProposalID: pid,
			TenantID: "t1", EntityID: "ch_1", EntityType: "charge", ActionName: "create_refund", IssuedAt: at}); err != nil {
			t.Fatal(err)
		}
		if code, a := r.report(t, haltAdmin(), "tr-"+pid, nil); code != http.StatusConflict || a.Error != "not_a_payment" {
			t.Errorf("%s: %d %+v", pid, code, a)
		}
	}
	unnormal := r.call(t, "t1", "p-spaced", " refund-agent ", "create_refund", true, tooloutcome.Done)
	if code, a := r.report(t, haltAdmin(), unnormal, nil); code != http.StatusConflict || a.Error != "agent_unknown" {
		t.Errorf("an id not in its normal form: %d %+v", code, a)
	}
	paid := r.call(t, "t1", "p-pay", "refund-agent", "create_refund", true, tooloutcome.Done)
	r.call(t, "t1", "p-other", "refund-agent", "create_refund", true, tooloutcome.Done)
	if code, a := r.reportWith(t, r.halts, anotherCall{r.calls}, r.reports, paid); code != http.StatusConflict || a.Error != "not_a_payment" {
		t.Errorf("another call than the receipt's: %d %+v", code, a)
	}
	if r.halted(t, "t1", "refund-agent") {
		t.Fatal("halted")
	}
	if got, _ := r.reports.Reported(context.Background(), "t1", []string{"tr-p-held", "tr-p-blocked", unnormal, paid}); len(got) != 0 {
		t.Fatalf("kept %v", got)
	}
}

// The Record shows a report only on a payment: a call that isn't one shows
// none, whatever the store holds for its receipt.
func TestTheRecordShowsAReportOnlyOnAPayment(t *testing.T) {
	r := newReportRig(t)
	msg := r.call(t, "t1", "p-msg", "desk-agent", "chat_postMessage", true, tooloutcome.Done)
	if _, err := r.reports.Report(context.Background(), payreports.Report{TenantID: "t1", TraceID: msg, ProposalID: "p-msg", AgentID: "desk-agent",
		ReportedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	withFakeIdentity(r.mux, haltAdmin()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/activity", nil))
	var out activityResp
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil || len(out.Calls) != 1 {
		t.Fatalf("activity: %d %v %+v", rec.Code, err, out)
	}
	if c := out.Calls[0]; c.Payment || c.Receipt == nil || c.Receipt.ReportedAt != nil {
		t.Fatalf("a message shows a report: %+v %+v", c, c.Receipt)
	}
}
