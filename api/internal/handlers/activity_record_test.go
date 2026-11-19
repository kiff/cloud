package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/proposals"
)

// kiff/tafs#167: an activity row says what the owner's record needs: the
// amount and a hash of what the call was on (as the decision recorded
// them), whether the tool only reads, how a held call was answered and
// from which phone, and its receipt.

type toolsFake struct {
	titles   map[string]string
	readOnly map[string]bool
}

func (f toolsFake) ToolTitles(context.Context, string) (map[string]string, error) {
	return f.titles, nil
}

func (f toolsFake) ReadOnlyTools(context.Context, string) (map[string]bool, error) {
	return f.readOnly, nil
}

type exceptionsFake map[string]mandate.Exception

func (f exceptionsFake) Exception(_ context.Context, _, id string) (mandate.Exception, error) {
	e, ok := f[id]
	if !ok {
		return mandate.Exception{}, mandate.ErrExceptionNotFound
	}
	return e, nil
}

type holdAnswersFake map[string]membership.HoldAnswer

func (f holdAnswersFake) HoldAnswers(_ context.Context, _ string, ids []string) (map[string]membership.HoldAnswer, error) {
	out := map[string]membership.HoldAnswer{}
	for _, id := range ids {
		if a, ok := f[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func TestActivity_RowsCarryTheRecord(t *testing.T) {
	t.Parallel()
	const tenant = "t1"
	at := time.Date(2026, 10, 21, 0, 40, 0, 0, time.UTC)
	answeredAt := at.Add(51 * time.Second)
	excB, excC, excF := mandate.ExceptionID(tenant, "tc-b"), mandate.ExceptionID(tenant, "tc-c"), mandate.ExceptionID(tenant, "tc-f")
	reader := proposals.NewMemoryReader()
	h := handlers.NewControlsHandler(domain.NewMemoryControlStore(), nil,
		handlers.WithControlsDecisionsReader(reader),
		handlers.WithControlsToolTitles(toolsFake{titles: map[string]string{"stripe_refund": "Refund a payment"},
			readOnly: map[string]bool{"stripe_payment": true}}),
		handlers.WithControlsHoldAnswers(
			exceptionsFake{
				excB: {ID: excB, ProposalID: "tc-b", Status: mandate.ExceptionConsumed, ResolvedAt: &answeredAt},
				excC: {ID: excC, ProposalID: "tc-c", Status: mandate.ExceptionRejected, ResolvedAt: &answeredAt},
				excF: {ID: excF, ProposalID: "tc-f", Status: mandate.ExceptionApproved, ResolvedAt: &answeredAt},
			},
			holdAnswersFake{
				excB: {HoldID: excB, Answer: "approve-once", Answerer: "owner", Role: membership.Role("owner"),
					Device: &membership.DeviceProof{Name: "iPhone", KeyID: "dev-1"}},
				// Another admin answered: the reader didn't (kiff/tafs#170).
				excF: {HoldID: excF, Answer: "approve-once", Answerer: "user_other", Role: membership.Role("admin")},
				// An earlier answer the exception no longer records is not it.
				excC: {HoldID: excC, Answer: "approve-once", Role: membership.Role("owner")},
			}),
	)
	inner := http.NewServeMux()
	h.Register(inner)
	srv := withFakeIdentity(inner, auth.Identity{Tenant: tenant, Subject: "owner", Method: auth.MethodSession})

	eur := func(v string) *proposals.CallAmount { return &proposals.CallAmount{Value: v, Unit: "EUR"} }
	on := handlers.ResourceHashForTest("pi_QbstR")
	// A: within the Card, receipt signed.
	reader.Append(tenant, proposals.DecisionRow{ProposalID: "tc-a", ActionName: "stripe_refund", Outcome: "allowed", CreatedAt: at,
		Amount: eur("20.00"), OnArg: "charge", OnHash: on, ReceiptTraceID: "decision-tc-a", ReceiptStatus: "anchored", ReceiptSigned: true})
	// B: held, approved once from a phone, then allowed.
	reader.Append(tenant, proposals.DecisionRow{ProposalID: "tc-b", ActionName: "stripe_refund", Outcome: "owner_required", CreatedAt: at.Add(time.Minute),
		Amount: eur("150.00"), OnArg: "charge", OnHash: on})
	reader.Append(tenant, proposals.DecisionRow{ProposalID: "tc-b", ActionName: "stripe_refund", Outcome: "allowed", CreatedAt: at.Add(2 * time.Minute),
		Amount: eur("150.00"), OnArg: "charge", OnHash: on, ReceiptTraceID: "settled-tc-b", ReceiptStatus: "pending"})
	// C: held and refused, from the dashboard.
	reader.Append(tenant, proposals.DecisionRow{ProposalID: "tc-c", ActionName: "stripe_refund", Outcome: "owner_required", CreatedAt: at.Add(3 * time.Minute),
		Amount: eur("150.00"), OnArg: "charge", OnHash: on})
	reader.Append(tenant, proposals.DecisionRow{ProposalID: "tc-c", ActionName: "stripe_refund", Outcome: "blocked", CreatedAt: at.Add(4 * time.Minute),
		Amount: eur("150.00"), OnArg: "charge", OnHash: on})
	// D: a read, no amount, no receipt.
	reader.Append(tenant, proposals.DecisionRow{ProposalID: "tc-d", ActionName: "stripe_payment", Outcome: "allowed", CreatedAt: at.Add(5 * time.Minute),
		OnArg: "payment", OnHash: on})
	// E: held, not answered yet.
	reader.Append(tenant, proposals.DecisionRow{ProposalID: "tc-e", ActionName: "stripe_refund", Outcome: "owner_required", CreatedAt: at.Add(6 * time.Minute)})
	// F: held, approved once by another admin, from the dashboard.
	reader.Append(tenant, proposals.DecisionRow{ProposalID: "tc-f", ActionName: "stripe_refund", Outcome: "owner_required", CreatedAt: at.Add(7 * time.Minute)})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/activity", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Calls []map[string]any `json:"calls"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	calls := map[string]map[string]any{}
	for _, c := range body.Calls {
		calls[c["proposal_id"].(string)] = c
	}
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	for id, want := range map[string]map[string]string{
		"tc-a": {"amount": `{"unit":"EUR","value":"20.00"}`, "on_arg": `"charge"`, "on_hash": js(on),
			"receipt": `{"status":"signed","trace_id":"decision-tc-a"}`, "answer": `null`, "read_only": `null`, "title": `"Refund a payment"`},
		"tc-b": {"amount": `{"unit":"EUR","value":"150.00"}`, "receipt": `{"status":"pending","trace_id":"settled-tc-b"}`,
			"answer": js(map[string]any{"decision": "approved_once", "by": "owner", "you": true, "signed": true, "device": "iPhone", "at": answeredAt})},
		"tc-c": {"answer": js(map[string]any{"decision": "refused", "signed": false, "at": answeredAt}), "receipt": `null`},
		"tc-d": {"amount": `null`, "read_only": `true`, "receipt": `null`, "answer": `null`},
		"tc-e": {"answer": `null`, "amount": `null`, "on_hash": `null`},
		"tc-f": {"answer": js(map[string]any{"decision": "approved_once", "by": "admin", "signed": false, "at": answeredAt})},
	} {
		c, ok := calls[id]
		if !ok {
			t.Fatalf("%s missing: %s", id, rec.Body)
		}
		for k, v := range want {
			if got := js(c[k]); got != v {
				t.Errorf("%s.%s = %s, want %s", id, k, got, v)
			}
		}
	}
	if strings.Contains(rec.Body.String(), "pi_QbstR") {
		t.Fatalf("the resource value is on the wire: %s", rec.Body)
	}
}
