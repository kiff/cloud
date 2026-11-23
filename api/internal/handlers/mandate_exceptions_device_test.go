package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"github.com/kiff/cloud/api/internal/permit"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// heldForDevice holds one call outside a Card and returns its exception.
func heldForDevice(t *testing.T, store *mandate.MemoryStore, proposal string) mandate.Exception {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	d, err := a.Authorize(ctx, "t1", proposal, mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE",
		Parameters: map[string]int64{"amount": 500}})
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}
	e, err := store.Exception(ctx, "t1", d.ExceptionID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// A phone signed in through the device flow answers a held call only
// with its key's signature over that exact answer. The
// dashboard's session, without a key, answers as before.
func TestDeviceSessionAnswersNeedTheKeysSignature(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	phone := owner()
	phone.Method, phone.DeviceKey, phone.DeviceSessionID = auth.MethodDeveloper, der, "sess-phone"
	sign := func(a auth.Answer) map[string]any {
		digest := sha256.Sum256(a.Message())
		sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"signed_at": a.SignedAt, "value": base64.StdEncoding.EncodeToString(sig)}
	}
	signedAt := now.Add(-30 * time.Second).Format(time.RFC3339)
	answer := func(e mandate.Exception, verb string) auth.Answer {
		return auth.Answer{TenantID: "t1", ExceptionID: e.ID, Answer: verb, AccountingVersion: e.AccountingVersion, SignedAt: signedAt}
	}
	post := func(id auth.Identity, e mandate.Exception, verb string, body any) (int, string) {
		rr := do(t, mux, id, http.MethodPost, "/v1/me/card-exceptions/"+e.ID+"/"+verb, body)
		return rr.Code, rr.Body.String()
	}

	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
	if err := store.Issue(context.Background(), card, "owner"); err != nil {
		t.Fatal(err)
	}
	e1 := heldForDevice(t, store, "p-1")
	e2 := heldForDevice(t, store, "p-2")

	// Refused: no signature, one made for another hold, for another
	// answer, for an older accounting version, or too old. (A phone's
	// change-card is signed like these: phone_reaches_kiff_test.go.)
	if code, body := post(phone, e1, "approve-once", map[string]any{}); code != http.StatusForbidden || !strings.Contains(body, "signature_required") {
		t.Errorf("unsigned answer: %d %s", code, body)
	}
	if code, body := post(phone, e1, "approve-once", map[string]any{"signature": sign(answer(e2, "approve-once"))}); code != http.StatusForbidden || !strings.Contains(body, "signature_invalid") {
		t.Errorf("another hold's signature: %d %s", code, body)
	}
	if code, body := post(phone, e1, "approve-once", map[string]any{"signature": sign(answer(e1, "reject"))}); code != http.StatusForbidden || !strings.Contains(body, "signature_invalid") {
		t.Errorf("a refusal's signature approved: %d %s", code, body)
	}
	stale := answer(e1, "approve-once")
	stale.AccountingVersion++
	if code, body := post(phone, e1, "approve-once", map[string]any{"signature": sign(stale)}); code != http.StatusForbidden {
		t.Errorf("another accounting version's signature: %d %s", code, body)
	}
	old := answer(e1, "approve-once")
	old.SignedAt = now.Add(-10 * time.Minute).Format(time.RFC3339)
	if code, body := post(phone, e1, "approve-once", map[string]any{"signature": sign(old)}); code != http.StatusBadRequest || !strings.Contains(body, "signed_at_out_of_range") {
		t.Errorf("an old signature: %d %s", code, body)
	}
	if got, _ := store.Exception(context.Background(), "t1", e1.ID); got.Status != mandate.ExceptionHeld {
		t.Fatalf("a refused answer changed the hold: %s", got.Status)
	}

	// Accepted: the right signature approves once.
	if code, body := post(phone, e1, "approve-once", map[string]any{"signature": sign(answer(e1, "approve-once"))}); code != http.StatusOK || !strings.Contains(body, `"status":"approved"`) {
		t.Fatalf("signed approve: %d %s", code, body)
	}

	// A signed refusal covers its note: changing the note after signing
	// fails, the signed one goes through.
	refusal := answer(e2, "reject")
	refusal.Note = "Ask for photos."
	if code, body := post(phone, e2, "reject", map[string]any{"note": "Approved by Gabo.", "signature": sign(refusal)}); code != http.StatusForbidden {
		t.Errorf("a refusal with a changed note: %d %s", code, body)
	}
	if code, body := post(phone, e2, "reject", map[string]any{"note": "Ask for photos.", "signature": sign(refusal)}); code != http.StatusOK {
		t.Fatalf("signed refusal: %d %s", code, body)
	}

	// The dashboard's session has no key: it answers without a signature,
	// and may not send one it cannot have.
	e3 := heldForDevice(t, store, "p-3")
	if code, body := post(owner(), e3, "approve-once", map[string]any{"signature": sign(answer(e3, "approve-once"))}); code != http.StatusBadRequest || !strings.Contains(body, "no_device_key") {
		t.Errorf("a keyless session sent a signature: %d %s", code, body)
	}
	if code, body := post(owner(), e3, "approve-once", nil); code != http.StatusOK {
		t.Errorf("dashboard answer: %d %s", code, body)
	}
}

// The phone's signed answer is kept with who answered, so the
// settled receipt can carry it: the device, its key, the exact message
// KIFF verified and the signature. A dashboard answer keeps none.
func TestSignedAnswerKeepsTheDeviceProof(t *testing.T) {
	f := newHoldFixture(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	phone := person("alice", membership.RoleAdmin)
	phone.Method, phone.DeviceKey, phone.DeviceSessionID, phone.DeviceName = auth.MethodDeveloper, der, "sess-phone", "tafs on iPhone"
	id := f.hold("agent://a")
	e, err := f.store.Exception(context.Background(), "t1", id)
	if err != nil {
		t.Fatal(err)
	}
	signed := auth.Answer{TenantID: "t1", ExceptionID: id, Answer: "reject", NextStep: "", Note: "Ask for photos.",
		AccountingVersion: e.AccountingVersion, SignedAt: time.Date(2026, 9, 12, 11, 59, 30, 0, time.UTC).Format(time.RFC3339)}
	digest := sha256.Sum256(signed.Message())
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	value := base64.StdEncoding.EncodeToString(sig)
	if rr := do(t, f.mux, phone, http.MethodPost, "/v1/me/card-exceptions/"+id+"/reject",
		map[string]any{"note": "Ask for photos.", "signature": map[string]string{"signed_at": signed.SignedAt, "value": value}}); rr.Code != http.StatusOK {
		t.Fatalf("signed reject: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := f.members.HoldAnswers(context.Background(), "t1", []string{id})
	d := got[id].Device
	if d == nil || d.Name != "tafs on iPhone" || d.KeyID != "sess-phone" || string(d.PublicKey) != string(der) ||
		d.Message != string(signed.Message()) || d.Signature != value {
		t.Fatalf("device proof = %+v", d)
	}

	other := f.hold("agent://a")
	if rr := do(t, f.mux, person("alice", membership.RoleAdmin), http.MethodPost, "/v1/me/card-exceptions/"+other+"/reject", nil); rr.Code != http.StatusOK {
		t.Fatalf("dashboard reject: %d %s", rr.Code, rr.Body.String())
	}
	if got, _ := f.members.HoldAnswers(context.Background(), "t1", []string{other}); got[other].Device != nil {
		t.Errorf("a dashboard answer has a device proof: %+v", got[other].Device)
	}
}

// A held sealed fill's answer counts only when it signs over the block the
// phone drew (the hold's browser_sha256, a ninth line): an answer signed as
// for any other hold, or over another block, is refused.
func TestASealedFillsAnswerSignsTheBlockThePhoneDrew(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	phone := owner()
	phone.Method, phone.DeviceKey, phone.DeviceSessionID = auth.MethodDeveloper, der, "sess-phone"
	sign := func(a auth.Answer) map[string]any {
		digest := sha256.Sum256(a.Message())
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		return map[string]any{"signed_at": a.SignedAt, "value": base64.StdEncoding.EncodeToString(sig)}
	}
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 1000}}}}}
	if err := store.Issue(context.Background(), card, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return now })
	d, err := a.Authorize(context.Background(), "t1", "p-fill", mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE",
		Parameters: map[string]int64{"amount": 1}, OwnerAsk: "sealed_fill"})
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}
	e, _ := store.Exception(context.Background(), "t1", d.ExceptionID)
	block := []byte(`{"block":{"v":"planque/describe/v1","site":"shop.example"},"sig":"x"}`)
	if err := store.KeepBrowserBlock(context.Background(), "t1", "p-fill", block); err != nil {
		t.Fatal(err)
	}
	canon, _ := permit.Canonicalize([]byte(`{"v":"planque/describe/v1","site":"shop.example"}`))
	sum := sha256.Sum256(canon)
	answer := auth.Answer{TenantID: "t1", ExceptionID: e.ID, Answer: "approve-once", AccountingVersion: e.AccountingVersion,
		SignedAt: now.Add(-30 * time.Second).Format(time.RFC3339)}
	post := func(body any) (int, string) {
		rr := do(t, mux, phone, http.MethodPost, "/v1/me/card-exceptions/"+e.ID+"/approve-once", body)
		return rr.Code, rr.Body.String()
	}
	// Signed as for any other hold: refused.
	if code, body := post(map[string]any{"signature": sign(answer)}); code != http.StatusForbidden || !strings.Contains(body, "signature_invalid") {
		t.Fatalf("an answer that didn't sign the block: %d %s", code, body)
	}
	// Over another block: refused.
	other := answer
	other.BrowserSHA256 = strings.Repeat("0", 64)
	if code, body := post(map[string]any{"signature": sign(other)}); code != http.StatusForbidden || !strings.Contains(body, "signature_invalid") {
		t.Fatalf("an answer over another block: %d %s", code, body)
	}
	// Over the block the phone drew: taken.
	answer.BrowserSHA256 = hex.EncodeToString(sum[:])
	if code, body := post(map[string]any{"signature": sign(answer)}); code != http.StatusOK {
		t.Fatalf("an answer over the block: %d %s", code, body)
	}
}
