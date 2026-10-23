package receipt_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/receipt"
)

type fakeSettlements []receipt.Settlement

func (f fakeSettlements) Settled(context.Context, string) ([]receipt.Settlement, error) {
	return f, nil
}

// A held call gets its decision receipt at the hold; once settled
// it gets a second, settled receipt with every decision, the answer and
// the tool's report. The first is never rebuilt, the settled one is
// minted once, and it is what the proposal's receipt lookup returns.
func TestReconcileSettlements_MintsSettledReceiptOnce(t *testing.T) {
	t.Parallel()
	store := receipt.NewMemoryStore()
	at := time.Date(2026, time.October, 13, 1, 56, 0, 0, time.UTC)
	answered := at.Add(25 * time.Minute)
	sod := false
	settled := fakeSettlements{
		{ProposalID: "tc-rej", HoldID: "exc-1", Outcome: "rejected", Answer: "reject", AnsweredBy: "you (ai@kiff.dev)", Role: "admin",
			SeparationOfDuties: &sod, At: answered, NextStep: "return_first", Note: "Ask for photos."},
		{ProposalID: "tc-used", HoldID: "exc-2", Outcome: "approved_once", Answer: "approve-once", AnsweredBy: "you (ai@kiff.dev)", At: answered,
			Tool: &receipt.SettlementTool{Status: "refused_by_tool", Message: "only 0.00 is left to refund", At: answered.Add(time.Minute)}},
		{ProposalID: "tc-none", HoldID: "exc-3", Outcome: "expired", At: answered}, // no decision record: skipped
	}
	pl, err := receipt.NewPipeline(store, receipt.NewLocalAttester(mustWallet(t), nil), receipt.WithSettlements(settled))
	if err != nil {
		t.Fatal(err)
	}
	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("tc-rej", "owner_required", at),
		proposalDecisionRecord("tc-used", "owner_required", at),
		func() kiffaudit.Record {
			r := proposalDecisionRecord("tc-used", "allowed", answered.Add(30*time.Second))
			r.ID = "dec-tc-used-2"
			return r
		}(),
	})}
	ctx := context.Background()
	if _, err := pl.ReconcileDecisions(ctx, "ten-1", rt); err != nil {
		t.Fatal(err)
	}
	held, err := store.Get(ctx, "ten-1", receipt.DecisionReceiptTraceID("tc-rej"))
	if err != nil || held.Outcome != "owner_required" {
		t.Fatalf("decision receipt: %+v %v", held, err)
	}

	built, err := pl.ReconcileSettlements(ctx, "ten-1", rt)
	if err != nil || built != 2 {
		t.Fatalf("settlements built %d, err %v; want 2", built, err)
	}
	rej, err := store.Get(ctx, "ten-1", receipt.SettledReceiptTraceID("tc-rej"))
	if err != nil {
		t.Fatal(err)
	}
	last := rej.CausalChain[len(rej.CausalChain)-1]
	if rej.Outcome != "rejected" || rej.ProposalID != "tc-rej" || len(rej.CausalChain) != 2 || last.Kind != "hold_settled" ||
		last.ActorID != "you (ai@kiff.dev)" || !last.OccurredAt.Equal(answered) || !strings.Contains(last.Message, "return_first") || len(last.DataHash) != 64 {
		t.Errorf("settled rejection: %+v", rej)
	}
	used, _ := store.Get(ctx, "ten-1", receipt.SettledReceiptTraceID("tc-used"))
	if used.Outcome != "approved_once" || !used.IssuedAt.Equal(answered.Add(time.Minute)) {
		t.Errorf("settled approval: %+v", used)
	}
	// In the order things happened, the answer before the
	// allowed retry it led to, numbered after ordering.
	var kinds []string
	for i, st := range used.CausalChain {
		kinds = append(kinds, st.Kind)
		if st.Sequence != i+1 {
			t.Errorf("step %d has sequence %d", i, st.Sequence)
		}
	}
	if strings.Join(kinds, ",") != "proposal_decision,hold_settled,proposal_decision,tool_outcome" {
		t.Errorf("chain order = %v", kinds)
	}
	// The values themselves are in the signed receipt and survive a JSON
	// round trip, not only their hash.
	raw, err := json.Marshal(rej)
	if err != nil {
		t.Fatal(err)
	}
	var back receipt.Receipt
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	answer := back.CausalChain[1].Data
	if answer["role"] != "admin" || answer["note"] != "Ask for photos." || answer["next_step"] != "return_first" || answer["answered_by"] != "you (ai@kiff.dev)" || answer["separation_of_duties"] != false {
		t.Errorf("answer data after a round trip = %v", answer)
	}
	if tool := used.CausalChain[3].Data; tool["status"] != "refused_by_tool" || tool["message"] != "only 0.00 is left to refund" {
		t.Errorf("tool data = %v", tool)
	}
	// The proposal's receipt is now the settled one; the first is kept.
	if got, _ := store.GetByProposalID(ctx, "ten-1", "tc-rej"); got.TraceID != receipt.SettledReceiptTraceID("tc-rej") {
		t.Errorf("by proposal = %s, want the settled receipt", got.TraceID)
	}
	if first, _ := store.Get(ctx, "ten-1", receipt.DecisionReceiptTraceID("tc-rej")); first.Outcome != "owner_required" {
		t.Errorf("the decision receipt changed: %+v", first)
	}
	if again, err := pl.ReconcileSettlements(ctx, "ten-1", rt); err != nil || again != 0 {
		t.Errorf("second sweep built %d (err %v), want 0", again, err)
	}
	// The answer's hash covers what it reports: a different note is a
	// different hash.
	other := settled[0]
	other.Note = "Something else."
	r2, err := receipt.BuildSettlement(ctx, rt.Audit, "ten-1", other, time.Now())
	if err != nil || r2.CausalChain[1].DataHash == last.DataHash {
		t.Errorf("the answer's hash ignores the note: %v", err)
	}
}

// Two receipts of one proposal issued at the same time
// resolve to the settled one, whichever was stored first.
func TestGetByProposalID_SettledWinsATie(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.October, 14, 9, 0, 0, 0, time.UTC)
	for _, order := range [][]string{{"decision-p", "settled-p"}, {"settled-p", "decision-p"}} {
		store := receipt.NewMemoryStore()
		for _, trace := range order {
			if err := store.Save(context.Background(), receipt.Receipt{SchemaVersion: receipt.SchemaVersion, TraceID: trace, ProposalID: "p", TenantID: "t", EntityID: "e", ActionName: "a", IssuedAt: at}); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 20; i++ {
			if got, _ := store.GetByProposalID(context.Background(), "t", "p"); got.TraceID != "settled-p" {
				t.Fatalf("order %v: by proposal = %s", order, got.TraceID)
			}
		}
	}
}

// The memory store copies a step's Data, so neither the
// receipt saved nor one returned shares its map with the stored receipt.
func TestMemoryStore_StepDataIsCopied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := receipt.NewMemoryStore()
	r := receipt.Receipt{SchemaVersion: receipt.SchemaVersion, TraceID: "settled-p", ProposalID: "p", TenantID: "t", EntityID: "e", ActionName: "a",
		IssuedAt:    time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC),
		CausalChain: []receipt.ChainStep{{Sequence: 1, Kind: "hold_settled", DataHash: "h", Data: map[string]any{"note": "Ask for photos."}}}}
	if err := store.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.CausalChain[0].Data["note"] = "changed after Save"
	got, err := store.Get(ctx, "t", "settled-p")
	if err != nil || got.CausalChain[0].Data["note"] != "Ask for photos." {
		t.Fatalf("Save shared the map: %v %v", got.CausalChain[0].Data, err)
	}
	got.CausalChain[0].Data["note"] = "changed after Get"
	again, _ := store.GetByProposalID(ctx, "t", "p")
	if again.CausalChain[0].Data["note"] != "Ask for photos." {
		t.Errorf("Get shared the map: %v", again.CausalChain[0].Data)
	}
}

type fakeEmails map[string]string

func (f fakeEmails) EmailForSubject(_ context.Context, subject string) (string, error) {
	if e, ok := f[subject]; ok {
		return e, nil
	}
	return "", errors.New("no such user")
}

// The agent asking again while held writes an identical decision: the
// chain says "asked again", and the answerer is a person's email, with the
// sign-in id kept beside it. Unknown ids and external approvers stay as
// they were.
func TestSettledReceipt_RepeatsAndAnswererEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	at := time.Date(2026, time.October, 13, 1, 56, 29, 0, time.UTC)
	answered := at.Add(25 * time.Minute)
	store := receipt.NewMemoryStore()
	settled := fakeSettlements{
		{ProposalID: "tc-1", HoldID: "exc-1", Outcome: "rejected", Answer: "reject", AnsweredBy: "user_3KQe", At: answered},
		{ProposalID: "tc-2", HoldID: "exc-2", Outcome: "rejected", Answer: "reject", AnsweredBy: "user_gone", At: answered},
		{ProposalID: "tc-3", HoldID: "exc-3", Outcome: "approved_once", Answer: "approve-once", AnsweredBy: "grant-9", ExternalApprover: "Refund review", At: answered,
			Tool: &receipt.SettlementTool{Status: "done", At: answered.Add(time.Minute)}},
	}
	pl, err := receipt.NewPipeline(store, receipt.NewLocalAttester(mustWallet(t), nil), receipt.WithSettlements(settled),
		receipt.WithAnswererEmails(fakeEmails{"user_3KQe": "ai@kiff.dev", "grant-9": "should-not-be-used@x"}))
	if err != nil {
		t.Fatal(err)
	}
	second := proposalDecisionRecord("tc-1", "owner_required", at)
	second.ID = "dec-tc-1-again"
	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("tc-1", "owner_required", at), second,
		proposalDecisionRecord("tc-2", "owner_required", at),
		proposalDecisionRecord("tc-3", "owner_required", at),
	})}
	if built, err := pl.ReconcileSettlements(ctx, "t", rt); err != nil || built != 3 {
		t.Fatalf("built %d, %v", built, err)
	}
	one, _ := store.Get(ctx, "t", receipt.SettledReceiptTraceID("tc-1"))
	if one.CausalChain[0].Message != "held for its owner" || one.CausalChain[1].Message != "asked again: held for its owner" {
		t.Errorf("repeat not labelled: %q, %q", one.CausalChain[0].Message, one.CausalChain[1].Message)
	}
	ans := one.CausalChain[2]
	if ans.ActorID != "ai@kiff.dev" || ans.Data["answered_by"] != "ai@kiff.dev" || ans.Data["answered_by_id"] != "user_3KQe" {
		t.Errorf("answerer = %q %v", ans.ActorID, ans.Data)
	}
	two, _ := store.Get(ctx, "t", receipt.SettledReceiptTraceID("tc-2"))
	if d := two.CausalChain[1].Data; d["answered_by"] != "user_gone" || d["answered_by_id"] != nil {
		t.Errorf("a failed lookup changed the answerer: %v", d)
	}
	three, _ := store.Get(ctx, "t", receipt.SettledReceiptTraceID("tc-3"))
	if d := three.CausalChain[1].Data; d["answered_by"] != "grant-9" || d["external_approver"] != "Refund review" {
		t.Errorf("an external approver was looked up: %v", d)
	}
}

// A phone's answer is in the settled receipt with its proof:
// anyone holding the receipt can check the phone's signature over the
// exact answer with the key the receipt names.
func TestSettledReceipt_CarriesThePhonesSignedAnswer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	at := time.Date(2026, time.October, 14, 9, 0, 0, 0, time.UTC)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	message := "kiff.answer.v1\nt\nexc-1\nreject\nreturn_first\n\n2\n2026-10-14T09:25:00Z"
	digest := sha256.Sum256([]byte(message))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	store := receipt.NewMemoryStore()
	settled := fakeSettlements{{ProposalID: "tc-1", HoldID: "exc-1", Outcome: "rejected", Answer: "reject", AnsweredBy: "ai@kiff.dev", At: at.Add(25 * time.Minute),
		Device: &receipt.SettlementDevice{Name: "tafs on iPhone", KeyID: "sess-phone", PublicKey: der, Message: message, Signature: base64.StdEncoding.EncodeToString(sig)}}}
	pl, err := receipt.NewPipeline(store, receipt.NewLocalAttester(mustWallet(t), nil), receipt.WithSettlements(settled))
	if err != nil {
		t.Fatal(err)
	}
	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{proposalDecisionRecord("tc-1", "owner_required", at)})}
	if built, err := pl.ReconcileSettlements(ctx, "t", rt); err != nil || built != 1 {
		t.Fatalf("built %d, %v", built, err)
	}
	r, _ := store.Get(ctx, "t", receipt.SettledReceiptTraceID("tc-1"))
	var device map[string]any
	for _, s := range r.CausalChain {
		if s.Kind == "hold_settled" {
			device, _ = s.Data["device"].(map[string]any)
		}
	}
	if device == nil || device["name"] != "tafs on iPhone" || device["key_id"] != "sess-phone" || device["algorithm"] != "ecdsa-p256-sha256" {
		t.Fatalf("device = %v", device)
	}
	// Check it from the receipt alone.
	pubDER, _ := base64.StdEncoding.DecodeString(device["public_key"].(string))
	pub, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatal(err)
	}
	gotSig, _ := base64.StdEncoding.DecodeString(device["signature"].(string))
	signed := sha256.Sum256([]byte(device["signed_message"].(string)))
	if !ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), signed[:], gotSig) {
		t.Error("the receipt's device signature does not verify")
	}
	tampered := sha256.Sum256([]byte(strings.Replace(device["signed_message"].(string), "reject", "approve-once", 1)))
	if ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), tampered[:], gotSig) {
		t.Error("a changed answer still verifies")
	}
}

// A settled receipt's decision steps carry what the call moved and was on,
// like the decision receipt's (kiff/tafs#167): the phone's sheet for a
// held call reads the amount from this receipt.
func TestSettledReceipt_DecisionStepsCarryTheAmount(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.October, 21, 0, 40, 0, 0, time.UTC)
	held := proposalDecisionRecord("tc-1", "owner_required", at)
	held.Data["amount"] = map[string]any{"value": "150.00", "unit": "EUR"}
	held.Data["on_arg"], held.Data["on_hash"] = "charge", "sha256:ab"
	audit := newAuditStoreWith(t, []kiffaudit.Record{held})
	r, err := receipt.BuildSettlement(context.Background(), audit, "t",
		receipt.Settlement{ProposalID: "tc-1", HoldID: "exc-1", Outcome: "rejected", Answer: "reject", At: at.Add(time.Minute)}, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range r.CausalChain {
		if s.Kind == "proposal_decision" {
			found = true
			if s.Data["on_hash"] != "sha256:ab" || s.Data["amount"] == nil {
				t.Fatalf("decision step data = %v", s.Data)
			}
		}
	}
	if !found {
		t.Fatalf("no decision step: %+v", r.CausalChain)
	}
}
