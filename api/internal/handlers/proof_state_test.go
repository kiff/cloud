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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/proofs"
)

const planqueToken = "planque-service-token-for-tests"

type stateRig struct {
	mux   *http.ServeMux
	store *flakyProofStore
	now   time.Time
}

type flakyProofStore struct {
	*proofs.MemoryStore
	fail bool
}

func (f *flakyProofStore) RunsSince(ctx context.Context, since time.Time) ([]proofs.Run, error) {
	if f.fail {
		return nil, errors.New("down")
	}
	return f.MemoryStore.RunsSince(ctx, since)
}

func newStateRig(t *testing.T) *stateRig {
	t.Helper()
	rig := &stateRig{store: &flakyProofStore{MemoryStore: proofs.NewMemoryStore()},
		now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	reader := proofs.NewReader(rig.store, nil).WithClock(func() time.Time { return rig.now })
	h := NewProofStateHandler(reader, []string{"another-token", planqueToken})
	if h == nil {
		t.Fatal("NewProofStateHandler returned nil")
	}
	rig.mux = http.NewServeMux()
	h.Register(rig.mux)
	return rig
}

func (rig *stateRig) pass(t *testing.T, entry string, minutesAgo ...int) {
	t.Helper()
	for _, m := range minutesAgo {
		end := rig.now.Add(-time.Duration(m) * time.Minute)
		r := proofs.Run{Schema: 1, RunID: entry + "_" + end.Format("150405"), App: strings.Split(entry, "@")[0], Entry: entry,
			Tenant: proofTenant, Trigger: proofs.TriggerSchedule, StartedAt: end.Add(-time.Minute), EndedAt: end,
			Outcome: proofs.OutcomePass, Counted: true,
			Checks: []proofs.Check{{Name: proofs.CheckConnects, OK: true}, {Name: proofs.CheckCardLimitHeld, OK: true}}}
		if err := rig.store.Append(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func (rig *stateRig) get(t *testing.T, token, entry string) (int, proofs.EntryAnswer) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/ops/proofs/state?entry="+url.QueryEscape(entry), nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	rig.mux.ServeHTTP(w, req)
	var a proofs.EntryAnswer
	_ = json.Unmarshal(w.Body.Bytes(), &a)
	return w.Code, a
}

func TestPlanqueReadsAnEntrysState(t *testing.T) {
	rig := newStateRig(t)
	rig.pass(t, "stripe@7", 90, 30)

	if code, a := rig.get(t, planqueToken, "stripe@7"); code != http.StatusOK || a.State != proofs.StateGreen ||
		a.App != "stripe" || a.Entry != "stripe@7" || a.Since.IsZero() {
		t.Fatalf("fresh green: %d %+v", code, a)
	}
	if _, a := rig.get(t, planqueToken, "stripe@8"); a.State != proofs.StateUnknown {
		t.Fatalf("another revision: %+v", a)
	}
	if _, a := rig.get(t, planqueToken, "gmail@1"); a.State != proofs.StateUnknown || a.Reason != proofs.ReasonNoRuns {
		t.Fatalf("no runs: %+v", a)
	}
	// The store unreadable for over 24 h: unknown, stale.
	rig.store.fail = true
	rig.now = rig.now.Add(25 * time.Hour)
	if _, a := rig.get(t, planqueToken, "stripe@7"); a.State != proofs.StateUnknown || a.Reason != proofs.ReasonStale {
		t.Fatalf("stale: %+v", a)
	}
}

func TestPlanquesReadNeedsItsToken(t *testing.T) {
	rig := newStateRig(t)
	rig.pass(t, "stripe@7", 90, 30)
	for name, tok := range map[string]string{"none": "", "wrong": "planque-service-token-for-test", "longer": planqueToken + "x"} {
		if code, _ := rig.get(t, tok, "stripe@7"); code != http.StatusNotFound {
			t.Errorf("%s token: %d", name, code)
		}
	}
	if code, _ := rig.get(t, planqueToken, "not an entry"); code != http.StatusBadRequest {
		t.Errorf("bad entry: %d", code)
	}
	if NewProofStateHandler(proofs.NewReader(rig.store, nil), []string{" ", ""}) != nil {
		t.Error("mounted with no token")
	}
}

// testPhone is the proof runner's test phone: its key, and that key's
// fingerprint listed in KIFF_PROOF_TEST_KEYS.
func testPhone(t *testing.T) (*ecdsa.PrivateKey, []byte, proofs.TestKeys) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	sum := sha256.Sum256(der)
	keys, bad := proofs.ParseTestKeys([]string{hex.EncodeToString(sum[:])})
	if len(bad) != 0 || !keys.Has(der) {
		t.Fatal("test key not parsed")
	}
	return key, der, keys
}

func TestTheTestPhoneAnswersOnlyOnATestTenant(t *testing.T) {
	key, der, keys := testPhone(t)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	answer := func(mux *http.ServeMux, store *mandate.MemoryStore, tenant string) (int, string, mandate.Exception) {
		card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
			Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
				Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
			Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
		if err := store.Issue(context.Background(), card, "owner"); err != nil {
			t.Fatal(err)
		}
		e := heldForDevice(t, store, "p-1")
		a := auth.Answer{TenantID: "t1", ExceptionID: e.ID, Answer: "approve-once", AccountingVersion: e.AccountingVersion,
			SignedAt: now.Add(-30 * time.Second).Format(time.RFC3339)}
		digest := sha256.Sum256(a.Message())
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		phone := owner()
		phone.Method, phone.DeviceKey, phone.DeviceSessionID = auth.MethodDeveloper, der, "sess-test-phone"
		rr := do(t, mux, phone, http.MethodPost, "/v1/me/card-exceptions/"+e.ID+"/approve-once",
			map[string]any{"signature": map[string]any{"signed_at": a.SignedAt, "value": base64.StdEncoding.EncodeToString(sig)}})
		return rr.Code, rr.Body.String(), e
	}
	server := func(testTenants ...string) (*http.ServeMux, *mandate.MemoryStore) {
		store := mandate.NewMemoryStore()
		store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
		h := NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time { return now }),
			WithMandatesProofTestKeys(keys, testTenants))
		mux := http.NewServeMux()
		h.Register(mux)
		return mux, store
	}

	// On a managed test tenant ("t1" here) the test phone approves.
	mux, store := server("t1")
	if code, body, _ := answer(mux, store, "t1"); code != http.StatusOK || !strings.Contains(body, `"status":"approved"`) {
		t.Fatalf("test tenant: %d %s", code, body)
	}

	// On any other tenant: 403, and the hold is still held.
	mux, store = server("tnt_proof_only")
	code, body, e := answer(mux, store, "t1")
	if code != http.StatusForbidden || !strings.Contains(body, "proof_test_key") {
		t.Fatalf("customer tenant: %d %s", code, body)
	}
	if got, _ := store.Exception(context.Background(), "t1", e.ID); got.Status != mandate.ExceptionHeld {
		t.Fatalf("the refused answer changed the hold: %s", got.Status)
	}

	// No test keys configured: the same phone answers as any phone.
	store2 := mandate.NewMemoryStore()
	store2.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store2, nil, WithMandatesClock(func() time.Time { return now }))
	plain := http.NewServeMux()
	h.Register(plain)
	if code, body, _ := answer(plain, store2, "t1"); code != http.StatusOK {
		t.Fatalf("without the list: %d %s", code, body)
	}
}

func TestTheTestPhoneCantOverride(t *testing.T) {
	_, der, keys := testPhone(t)
	for _, releaseAnyway := range []bool{false, true} {
		rig := newOpsProofsRig(t, nil, releaseAnyway, WithOpsProofTestKeys(keys))
		mux := rig.mux
		rig.seed(t, "gmail", "fail", "fail", "fail")
		tomorrow := rig.now.Add(24 * time.Hour).Format(time.RFC3339)
		for _, action := range []string{"hold", "release", "clear"} {
			for _, key := range []string{"device", "approval"} {
				id := auth.Identity{Subject: opsSubject, Method: auth.MethodSession}
				if key == "device" {
					id.DeviceKey = der
				} else {
					id.DeviceKey, id.ApprovalKey = []byte("another device key"), der
				}
				req := httptest.NewRequest(http.MethodPost, "/v1/ops/proofs/apps/gmail/"+action,
					strings.NewReader(`{"reason":"x","until":"`+tomorrow+`","owner_approval":"owner's yes"}`))
				req = req.WithContext(auth.WithIdentity(req.Context(), id))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "proof_test_key") {
					t.Errorf("%s with the test %s key (release anyway %v): %d %s", action, key, releaseAnyway, w.Code, w.Body)
				}
			}
		}
		if o, _ := rig.store.Overrides(context.Background()); len(o) != 0 {
			t.Fatalf("the test key saved an override: %+v", o)
		}
		if a, _ := rig.audit.List(context.Background(), "", 10); len(a) != 0 {
			t.Fatalf("the test key reached the audit: %+v", a)
		}
	}
}

// A session holding the test key as its approval key alone, no device key,
// is refused too: the check comes before the no-device-key branch.
func TestAnApprovalKeyOnlyTestSessionAnswersNothingOnACustomersTenant(t *testing.T) {
	_, der, keys := testPhone(t)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time { return now }),
		WithMandatesProofTestKeys(keys, []string{"tnt_proof_only"}))
	mux := http.NewServeMux()
	h.Register(mux)
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 100000, Window: mandate.WindowCalendarDay}}}
	if err := store.Issue(context.Background(), card, "owner"); err != nil {
		t.Fatal(err)
	}
	e := heldForDevice(t, store, "p-1")
	session := owner()
	session.ApprovalKey = der // and no DeviceKey
	rr := do(t, mux, session, http.MethodPost, "/v1/me/card-exceptions/"+e.ID+"/approve-once", map[string]any{})
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "proof_test_key") {
		t.Fatalf("approval-key-only test session: %d %s", rr.Code, rr.Body)
	}
	if got, _ := store.Exception(context.Background(), "t1", e.ID); got.Status != mandate.ExceptionHeld {
		t.Fatalf("the refused answer changed the hold: %s", got.Status)
	}
}
