package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
)

// withdrawRig is an account with one agent ("agent://a") whose refunds
// above 100 are held for the owner, and an alert target that records.
func withdrawRig(t *testing.T) (*http.ServeMux, *mandate.MemoryStore, *holdAlerts) {
	t.Helper()
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	alerts := &holdAlerts{}
	h := NewMandatesHandler(store, nil, WithMandatesAlerts(alerts), WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	mux := http.NewServeMux()
	h.Register(mux)
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}}}
	if err := store.Issue(context.Background(), card, "owner"); err != nil {
		t.Fatal(err)
	}
	return mux, store, alerts
}

func withdrawOf(t *testing.T, mux *http.ServeMux, id auth.Identity, exceptionID string) (int, map[string]any) {
	t.Helper()
	rr := do(t, mux, id, http.MethodPost, "/v1/me/holds/"+exceptionID+"/withdraw", nil)
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	return rr.Code, body
}

// An agent whose run was cancelled withdraws its own held call. The
// hold stops waiting, the owner can no longer approve it, the phone is told,
// and withdrawing again answers the same without telling it twice.
func TestAnAgentWithdrawsItsOwnHeldCall(t *testing.T) {
	mux, store, alerts := withdrawRig(t)
	e := heldForDevice(t, store, "p-1")
	agent := agentKey("agent://a", "")

	code, body := withdrawOf(t, mux, agent, e.ID)
	if code != http.StatusOK || body["status"] != string(mandate.ExceptionInvalidated) || body["withdrawn"] != true {
		t.Fatalf("withdraw: %d %v", code, body)
	}
	got, _ := store.Exception(context.Background(), "t1", e.ID)
	if !got.Withdrawn() || got.ResolvedBy != "agent://a" {
		t.Fatalf("stored = %+v", got)
	}
	if len(alerts.got) != 1 || alerts.got[0].Outcome != alert.HoldAnswered || alerts.got[0].Hold.ID != e.ID ||
		alerts.got[0].Hold.Status != string(mandate.ExceptionInvalidated) {
		t.Fatalf("alerts = %+v", alerts.got)
	}

	// Again: the same answer, and the phone isn't told twice.
	if code, body := withdrawOf(t, mux, agent, e.ID); code != http.StatusOK || body["withdrawn"] != true {
		t.Fatalf("second withdraw: %d %v", code, body)
	}
	if len(alerts.got) != 1 {
		t.Fatalf("alerted %d times", len(alerts.got))
	}

	// The owner's later answer is refused: nothing is approved.
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+e.ID+"/approve-once", nil); rr.Code != http.StatusConflict {
		t.Fatalf("approve after withdraw: %d %s", rr.Code, rr.Body.String())
	}
	if got, _ := store.Exception(context.Background(), "t1", e.ID); !got.Withdrawn() {
		t.Fatalf("an answer changed a withdrawn hold: %+v", got)
	}
}

// A call the owner refused isn't withdrawn: 409, with the answer, so the
// agent can record what KIFF did.
func TestARefusedCallIsNotWithdrawn(t *testing.T) {
	mux, store, alerts := withdrawRig(t)
	e := heldForDevice(t, store, "p-1")
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+e.ID+"/reject", nil); rr.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rr.Code, rr.Body.String())
	}
	alerts.got = nil
	code, body := withdrawOf(t, mux, agentKey("agent://a", ""), e.ID)
	hold, _ := body["hold"].(map[string]any)
	if code != http.StatusConflict || body["error"] != "already_answered" || hold["status"] != string(mandate.ExceptionRejected) {
		t.Fatalf("withdraw after refusal: %d %v", code, body)
	}
	if got, _ := store.Exception(context.Background(), "t1", e.ID); got.Status != mandate.ExceptionRejected {
		t.Fatalf("the refusal changed: %s", got.Status)
	}
	if len(alerts.got) != 0 {
		t.Fatalf("alerted on a refused withdrawal: %+v", alerts.got)
	}
}

// A call the owner approved but the agent hasn't collected is withdrawn:
// nothing was sent, and a cancelled run never collects it. The approval
// stays in its history, and a later retry of the call isn't forwarded.
func TestAnApprovedUncollectedCallIsWithdrawn(t *testing.T) {
	mux, store, alerts := withdrawRig(t)
	e := heldForDevice(t, store, "p-1")
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+e.ID+"/approve-once", nil); rr.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rr.Code, rr.Body.String())
	}
	alerts.got = nil
	if code, body := withdrawOf(t, mux, agentKey("agent://a", ""), e.ID); code != http.StatusOK || body["withdrawn"] != true {
		t.Fatalf("withdraw an approved call: %d %v", code, body)
	}
	got, _ := store.Exception(context.Background(), "t1", e.ID)
	if !got.Withdrawn() || len(got.PriorApprovals) != 1 {
		t.Fatalf("stored = %+v", got)
	}
	if len(alerts.got) != 1 || alerts.got[0].Hold.Status != string(mandate.ExceptionInvalidated) {
		t.Fatalf("alerts = %+v", alerts.got)
	}
	d, err := mandate.NewAuthorizer(store, nil, func() time.Time { return time.Date(2026, 9, 12, 12, 1, 0, 0, time.UTC) }).Authorize(
		context.Background(), "t1", "p-1", mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}})
	if err != nil || d.Outcome != mandate.OutcomeWithdrawn {
		t.Fatalf("a retry after the withdrawal: %+v %v", d, err)
	}
	if got, _ := store.Exception(context.Background(), "t1", e.ID); !got.Withdrawn() {
		t.Fatalf("the retry changed the withdrawn record: %+v", got)
	}
}

// A withdrawn call stays withdrawn on every retry: it isn't held again for
// the owner to approve, and it isn't let through once it fits a Card.
func TestAWithdrawnCallStaysWithdrawnOnRetry(t *testing.T) {
	mux, store, _ := withdrawRig(t)
	ctx := context.Background()
	e := heldForDevice(t, store, "p-1")
	if code, _ := withdrawOf(t, mux, agentKey("agent://a", ""), e.ID); code != http.StatusOK {
		t.Fatalf("withdraw: %d", code)
	}
	req := mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}}
	retry := func(when time.Time) mandate.Decision {
		t.Helper()
		d, err := mandate.NewAuthorizer(store, nil, func() time.Time { return when }).Authorize(ctx, "t1", "p-1", req)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := retry(time.Date(2026, 9, 12, 12, 1, 0, 0, time.UTC)); d.Outcome != mandate.OutcomeWithdrawn || d.ExceptionID != e.ID {
		t.Fatalf("retry with the Card unchanged: %+v", d)
	}
	if got, _ := store.Exception(ctx, "t1", e.ID); !got.Withdrawn() {
		t.Fatalf("re-held: %+v", got)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/card-exceptions/"+e.ID+"/approve-once", nil); rr.Code != http.StatusConflict {
		t.Fatalf("approve after a retry: %d %s", rr.Code, rr.Body.String())
	}
	// Now a Card fits the call: still withdrawn, nothing drawn.
	roomy := mandate.Mandate{ID: "card-roomy", TenantID: "t1", Subject: "agent://a",
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 10000}}}}}
	if err := store.Issue(ctx, roomy, "owner"); err != nil {
		t.Fatal(err)
	}
	if d := retry(time.Date(2026, 9, 12, 12, 2, 0, 0, time.UTC)); d.Outcome != mandate.OutcomeWithdrawn {
		t.Fatalf("retry once a Card fits: %+v", d)
	}
	if draws, _ := store.ProposalDraws(ctx, "t1", "p-1"); len(draws) != 0 {
		t.Fatalf("a withdrawn call drew: %+v", draws)
	}
}

// Only the agent that made the call can withdraw it. Another agent gets a
// 404, like for a missing call; a person's session is refused.
func TestOnlyItsAgentWithdrawsAHeldCall(t *testing.T) {
	mux, store, _ := withdrawRig(t)
	e := heldForDevice(t, store, "p-1")
	if code, _ := withdrawOf(t, mux, agentKey("agent://b", ""), e.ID); code != http.StatusNotFound {
		t.Errorf("another agent: %d", code)
	}
	if code, _ := withdrawOf(t, mux, agentKey("agent://a", ""), "exc-missing"); code != http.StatusNotFound {
		t.Errorf("a missing call: %d", code)
	}
	if code, _ := withdrawOf(t, mux, owner(), e.ID); code != http.StatusForbidden {
		t.Errorf("a person: %d", code)
	}
	if got, _ := store.Exception(context.Background(), "t1", e.ID); got.Status != mandate.ExceptionHeld {
		t.Fatalf("the hold changed: %s", got.Status)
	}
}

// The sweep expires a held call nobody answered, past its expiry and the
// grace, and tells the alert targets once: hold_answered, status expired.
func TestTheSweepExpiresUnansweredHolds(t *testing.T) {
	_, store, _ := withdrawRig(t)
	alerts := &holdAlerts{}
	e := heldForDevice(t, store, "p-1")
	if e.ExpiresAt == nil {
		t.Fatal("a held call without an expiry")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	if n := ExpireHolds(ctx, store, alerts, e.ExpiresAt.Add(HoldExpiryGrace-time.Second), logger); n != 0 {
		t.Fatalf("expired %d within the grace", n)
	}
	if n := ExpireHolds(ctx, store, alerts, e.ExpiresAt.Add(HoldExpiryGrace), logger); n != 1 {
		t.Fatalf("expired %d", n)
	}
	if len(alerts.got) != 1 || alerts.got[0].Outcome != alert.HoldAnswered || alerts.got[0].Hold.Status != string(mandate.ExceptionExpired) {
		t.Fatalf("alerts = %+v", alerts.got)
	}
	if n := ExpireHolds(ctx, store, alerts, e.ExpiresAt.Add(time.Hour), logger); n != 0 || len(alerts.got) != 1 {
		t.Fatalf("a second sweep expired %d, alerts %d", n, len(alerts.got))
	}
	if got, _ := store.Exception(ctx, "t1", e.ID); got.Status != mandate.ExceptionExpired {
		t.Fatalf("stored status %s", got.Status)
	}
}

// A retry right after the expiry records it before the sweep does (the
// gateway's collection is that retry). The sweep still tells the alert
// targets, once.
func TestTheSweepTellsAnExpiryARetryRecordedFirst(t *testing.T) {
	_, store, _ := withdrawRig(t)
	alerts := &holdAlerts{}
	e := heldForDevice(t, store, "p-1")
	ctx := context.Background()
	retryAt := e.ExpiresAt.Add(10 * time.Second)
	d, err := mandate.NewAuthorizer(store, nil, func() time.Time { return retryAt }).Authorize(ctx, "t1", "p-1",
		mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}})
	if err != nil || d.Outcome != mandate.OutcomeOwnerExpired {
		t.Fatalf("retry: %+v %v", d, err)
	}
	if got, _ := store.Exception(ctx, "t1", e.ID); got.Status != mandate.ExceptionExpired {
		t.Fatalf("the retry didn't record the expiry: %s", got.Status)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if n := ExpireHolds(ctx, store, alerts, e.ExpiresAt.Add(HoldExpiryGrace), logger); n != 1 || len(alerts.got) != 1 ||
		alerts.got[0].Hold.Status != string(mandate.ExceptionExpired) {
		t.Fatalf("sweep told %d, alerts %+v", n, alerts.got)
	}
	if n := ExpireHolds(ctx, store, alerts, e.ExpiresAt.Add(2*HoldExpiryGrace), logger); n != 0 || len(alerts.got) != 1 {
		t.Fatalf("told twice: %d, alerts %d", n, len(alerts.got))
	}
}

// A held call that expired long ago (nobody retried it, before the sweep
// existed) is recorded expired on the first sweep, and not announced late.
func TestAnOldExpiryIsRecordedNotAnnounced(t *testing.T) {
	_, store, _ := withdrawRig(t)
	alerts := &holdAlerts{}
	e := heldForDevice(t, store, "p-1")
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if n := ExpireHolds(ctx, store, alerts, e.ExpiresAt.Add(2*time.Hour), logger); n != 0 || len(alerts.got) != 0 {
		t.Fatalf("announced %d late, alerts %+v", n, alerts.got)
	}
	if got, _ := store.Exception(ctx, "t1", e.ID); got.Status != mandate.ExceptionExpired {
		t.Fatalf("stored status %s", got.Status)
	}
	if n := ExpireHolds(ctx, store, alerts, e.ExpiresAt.Add(3*time.Hour), logger); n != 0 || len(alerts.got) != 0 {
		t.Fatalf("a second sweep announced %d", n)
	}
}

// An agent reads which of its holds it withdrew. Both are
// invalidated, but only the one its run withdrew carries withdrawn; one
// whose Card was revoked does not, so the gateway tells the two apart.
func TestAgentHoldsSayWhichWereWithdrawn(t *testing.T) {
	mux, store, _ := withdrawRig(t)
	withdrawn := heldForDevice(t, store, "p-withdrawn")
	revoked := heldForDevice(t, store, "p-revoked")
	agent := agentKey("agent://a", "")
	if code, body := withdrawOf(t, mux, agent, withdrawn.ID); code != http.StatusOK {
		t.Fatalf("withdraw: %d %v", code, body)
	}
	if err := store.Revoke(context.Background(), "t1", "card-x", time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	rr := do(t, mux, agent, http.MethodGet, "/v1/me/holds?ids="+withdrawn.ID+","+revoked.ID, nil)
	var body struct {
		Holds []map[string]any `json:"holds"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || rr.Code != http.StatusOK || len(body.Holds) != 2 {
		t.Fatalf("holds: %d %s", rr.Code, rr.Body.String())
	}
	got := map[string]map[string]any{}
	for _, h := range body.Holds {
		got[h["exception_id"].(string)] = h
	}
	if h := got[withdrawn.ID]; h["status"] != string(mandate.ExceptionInvalidated) || h["withdrawn"] != true {
		t.Errorf("the withdrawn hold: %v", h)
	}
	if h := got[revoked.ID]; h["status"] != string(mandate.ExceptionInvalidated) || h["withdrawn"] != nil {
		t.Errorf("the revoked Card's hold: %v", h)
	}
}
