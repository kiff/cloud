package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
)

type holdAlerts struct {
	mu  sync.Mutex
	got []alert.Event
}

func (a *holdAlerts) Notify(ev alert.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, ev)
}

// A target's signing secret is shown when it is made and when it
// is rotated, never in the list; rotating an unknown target is a 404.
func TestAlertTargetSigningSecretShownOnce(t *testing.T) {
	store := alert.NewMemoryStore()
	mux := http.NewServeMux()
	NewAlertTargetsHandler(store, false).Register(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req = req.WithContext(auth.WithIdentity(req.Context(), owner()))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec := call(http.MethodPost, "/v1/me/alert-targets", `{"url":"https://hooks.example.com/kiff","outcomes":["owner_required","hold_answered"]}`)
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	secret, _ := created["signing_secret"].(string)
	id, _ := created["id"].(string)
	if rec.Code != http.StatusCreated || !strings.HasPrefix(secret, "whsec_") || id == "" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	targets, _ := store.ListTargets(context.Background(), "t1")
	if len(targets) != 1 || targets[0].Secret != secret {
		t.Fatalf("stored secret = %+v", targets)
	}
	if list := call(http.MethodGet, "/v1/me/alert-targets", ""); strings.Contains(list.Body.String(), "whsec_") || strings.Contains(list.Body.String(), "secret") {
		t.Errorf("the list shows a secret: %s", list.Body.String())
	}
	rot := call(http.MethodPost, "/v1/me/alert-targets/"+id+"/rotate-secret", "")
	var rotated map[string]string
	_ = json.Unmarshal(rot.Body.Bytes(), &rotated)
	if rot.Code != http.StatusOK || !strings.HasPrefix(rotated["signing_secret"], "whsec_") || rotated["signing_secret"] == secret {
		t.Fatalf("rotate: %d %s", rot.Code, rot.Body.String())
	}
	if targets, _ := store.ListTargets(context.Background(), "t1"); targets[0].Secret != rotated["signing_secret"] {
		t.Error("rotation did not store the new secret")
	}
	if miss := call(http.MethodPost, "/v1/me/alert-targets/alrt_nope/rotate-secret", ""); miss.Code != http.StatusNotFound {
		t.Errorf("rotating an unknown target: %d", miss.Code)
	}
}

// A Card hold's alert names the held call, its Needs you link and when it
// stops waiting.
func TestOwnerRequiredAlertNamesTheHold(t *testing.T) {
	rec := &holdAlerts{}
	h := &ProposalsHandler{alerter: rec, logger: testLogger(), now: func() time.Time { return time.Unix(0, 0) }}
	at := time.Date(2026, 9, 12, 12, 10, 0, 0, time.UTC)
	h.maybeAlert(context.Background(), owner(), propForTest("agent-1"), proposalResponse{Outcome: string(outcomeOwnerRequired),
		Exception: &exceptionHandle{ID: "exc-1", Status: "held", ReviewURL: "/needs-you/exc-1", ExpiresAt: &at}})
	if len(rec.got) != 1 || rec.got[0].Hold == nil || rec.got[0].Hold.ID != "exc-1" || rec.got[0].Hold.ReviewURL != "/needs-you/exc-1" ||
		rec.got[0].Hold.ExpiresAt == nil || !rec.got[0].Hold.ExpiresAt.Equal(at) {
		t.Fatalf("alert = %+v", rec.got)
	}
	h.maybeAlert(context.Background(), owner(), propForTest("agent-1"), proposalResponse{Outcome: string(outcomeBlocked),
		Exception: &exceptionHandle{ID: "exc-2", Status: "rejected"}})
	if len(rec.got) != 2 || rec.got[1].Hold != nil {
		t.Errorf("a refusal carries a hold: %+v", rec.got[1])
	}
}

// A held call's alert says whether it moves money and whether it may wait,
// from its tool's owner-approved profile. can_wait is true only for a read,
// a draft, a write or bookkeeping, and false when it sends or moves money.
// Both are left out when KIFF can't tell (no profile, no operation by that
// name, or the read failed), so a receiver that waits only on can_wait true
// never makes a money call or a message wait on a guess.
func TestAHoldsAlertSaysWhetherItMovesMoneyAndCanWait(t *testing.T) {
	read := func(name string) []byte {
		raw, err := os.ReadFile("../profile/testdata/valid/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	stripe, slack, holded, notion, gmail := read("stripe-refunds.json"), read("slack-messages.json"), read("holded-invoices.json"), read("notion-pages.json"), read("gmail-drafts.json")
	held := proposalResponse{Outcome: string(outcomeOwnerRequired), Exception: &exceptionHandle{ID: "exc-1", Status: "held"}}
	yes, no := true, false
	for name, c := range map[string]struct {
		action            string
		profiles          map[string][]byte
		err               error
		moneyOut, canWait *bool
	}{
		"moves money (create_refund)":    {action: "create_refund", profiles: map[string][]byte{"create_refund": stripe}, moneyOut: &yes, canWait: &no},
		"sends (chat_postMessage)":       {action: "chat_postMessage", profiles: map[string][]byte{"chat_postMessage": slack}, moneyOut: &no, canWait: &no},
		"records money (create_invoice)": {action: "create_invoice", profiles: map[string][]byte{"create_invoice": holded}, moneyOut: &no, canWait: &yes},
		"writes (notion-create-pages)":   {action: "notion-create-pages", profiles: map[string][]byte{"notion-create-pages": notion}, moneyOut: &no, canWait: &yes},
		"a draft (drafts_create)":        {action: "drafts_create", profiles: map[string][]byte{"drafts_create": gmail}, moneyOut: &no, canWait: &yes},
		"a read (get_payment_intent)":    {action: "get_payment_intent", profiles: map[string][]byte{"get_payment_intent": stripe}, moneyOut: &no, canWait: &yes},
		"no profile for the tool":        {action: "create_refund", profiles: map[string][]byte{}},
		"no operation by that name":      {action: "send_invoice", profiles: map[string][]byte{"send_invoice": stripe}},
		"a profile that doesn't parse":   {action: "create_refund", profiles: map[string][]byte{"create_refund": []byte(`{"v":"0"}`)}},
		"the profiles couldn't be read":  {action: "create_refund", err: context.DeadlineExceeded},
	} {
		rec := &holdAlerts{}
		h := &ProposalsHandler{alerter: rec, logger: testLogger(), now: func() time.Time { return time.Unix(0, 0) },
			toolProfiles: stubToolProfiles{profiles: c.profiles, err: c.err}}
		prop := propForTest("agent-1")
		prop.ActionName = c.action
		h.maybeAlert(context.Background(), owner(), prop, held)
		b, _ := json.Marshal(rec.got[0].Hold)
		for _, f := range []struct {
			key       string
			got, want *bool
		}{{"money_out", rec.got[0].Hold.MoneyOut, c.moneyOut}, {"can_wait", rec.got[0].Hold.CanWait, c.canWait}} {
			if (f.got == nil) != (f.want == nil) || (f.got != nil && *f.got != *f.want) {
				t.Errorf("%s: %s %s, want %s", name, f.key, show(f.got), show(f.want))
			}
			if f.want == nil && strings.Contains(string(b), `"`+f.key+`"`) {
				t.Errorf("%s: %s sent when unknown: %s", name, f.key, b)
			}
			if f.want != nil && !strings.Contains(string(b), fmt.Sprintf(`"%s":%t`, f.key, *f.want)) {
				t.Errorf("%s: a known %s wasn't sent: %s", name, f.key, b)
			}
		}
	}
	// No profiles wired: both left out, as before.
	rec := &holdAlerts{}
	h := &ProposalsHandler{alerter: rec, logger: testLogger(), now: func() time.Time { return time.Unix(0, 0) }}
	h.maybeAlert(context.Background(), owner(), propForTest("agent-1"), held)
	if rec.got[0].Hold.MoneyOut != nil || rec.got[0].Hold.CanWait != nil {
		t.Errorf("without profiles: %+v", rec.got[0].Hold)
	}
}

func show(b *bool) string {
	if b == nil {
		return "left out"
	}
	return fmt.Sprint(*b)
}

type stubToolProfiles struct {
	profiles map[string][]byte
	err      error
}

func (s stubToolProfiles) ToolProfiles(context.Context, string) (map[string][]byte, error) {
	return s.profiles, s.err
}

// Answering a held call tells the alert targets, with the hold and how it
// was answered.
func TestAnsweringAHoldAlerts(t *testing.T) {
	rec := &holdAlerts{}
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC) }),
		WithMandatesAlerts(rec))
	mux := http.NewServeMux()
	h.Register(mux)
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
	if err := store.Issue(context.Background(), card, "owner"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	d, err := mandate.NewAuthorizer(store, nil, func() time.Time { return now }).Authorize(context.Background(), "t1", "p-1",
		mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}})
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}
	if r := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/reject", nil); r.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", r.Code, r.Body.String())
	}
	if len(rec.got) != 1 || rec.got[0].Outcome != alert.HoldAnswered || rec.got[0].Hold == nil || rec.got[0].Hold.ID != d.ExceptionID ||
		rec.got[0].Hold.Status != string(mandate.ExceptionRejected) || rec.got[0].TenantID != "t1" || rec.got[0].Action != "REFUND_INVOICE" {
		t.Fatalf("answered alert = %+v", rec.got)
	}
}

// A target KIFF switched off shows why in the list, and its owner switches
// it back on; another account's target is not found.
func TestAlertTargetSwitchedOffAndBackOn(t *testing.T) {
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(context.Background(), alert.Target{ID: "alrt_1", TenantID: "t1", URL: "https://hooks.example.com/kiff", Enabled: true})
	_, _ = store.SetEnabled(context.Background(), "t1", "alrt_1", false, "deliveries kept failing for about a day: webhook returned 500", time.Now())
	mux := http.NewServeMux()
	NewAlertTargetsHandler(store, false).Register(mux)
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req = req.WithContext(auth.WithIdentity(req.Context(), owner()))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if list := call(http.MethodGet, "/v1/me/alert-targets"); !strings.Contains(list.Body.String(), `"disabled_reason":"deliveries kept failing`) ||
		!strings.Contains(list.Body.String(), `"enabled":false`) {
		t.Fatalf("list: %s", list.Body.String())
	}
	if rec := call(http.MethodPost, "/v1/me/alert-targets/alrt_1/enable"); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	if ts, _ := store.ListTargets(context.Background(), "t1"); !ts[0].Enabled || ts[0].DisabledReason != "" {
		t.Fatalf("after enable: %+v", ts[0])
	}
	if rec := call(http.MethodPost, "/v1/me/alert-targets/alrt_nope/enable"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown target: %d", rec.Code)
	}
}
