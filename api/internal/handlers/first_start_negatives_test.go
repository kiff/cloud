package handlers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/membership"
)

// The negatives for a taf's first start under one fingerprint: the phone
// signs the tools batch (checked by the gateway through
// POST /v1/me/device-signature) and the new agent's draft with its Cards
// (POST /v1/me/agent-drafts) once, under kiff.requests.v1, and sends the
// batch first. What must not get through: a draft made before its tools
// are connected, the draft taken on that signature without the batch it
// was signed with, a third request on the same signature, and a combined
// signature made with the device key.

// swapDomain is the account's gateway domain as connecting tools changes
// it: before the batch, no tool declares the Card's action.
type swapDomain struct {
	stubDomainSource
	mu  sync.Mutex
	cur string
}

func (s *swapDomain) set(yaml string) { s.mu.Lock(); s.cur = yaml; s.mu.Unlock() }
func (s *swapDomain) get() stubDomainSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stubDomainSource{yaml: s.cur}
}
func (s *swapDomain) Load(ctx context.Context, t string) (domain.TenantDomain, error) {
	return s.get().Load(ctx, t)
}
func (s *swapDomain) LoadByDomain(ctx context.Context, t, d string) (domain.TenantDomain, error) {
	return s.get().LoadByDomain(ctx, t, d)
}
func (s *swapDomain) List(ctx context.Context, t string) ([]domain.TenantDomain, error) {
	return s.get().List(ctx, t)
}

// beforeTheBatch is the domain with no tool declaring refund yet.
var beforeTheBatch = strings.Replace(strings.Replace(refundCentsYAML, "name: refund", "name: lookup", 1),
	"required_parameters: [amount_cents]", "required_parameters: []", 1)

type firstStart struct {
	t                   *testing.T
	r                   *draftRig
	dom                 *swapDomain
	h                   http.Handler
	phone               auth.Identity
	deviceKey, approval *ecdsa.PrivateKey
	at                  string
	batchPath           string
	batchBody, draft    []byte
}

func newFirstStart(t *testing.T) *firstStart {
	t.Helper()
	dom := &swapDomain{cur: beforeTheBatch}
	r := newDraftRigOn(t, dom)
	// One record of signed requests for KIFF's routes and the gateway's
	// check, as in production (cmd/api: deps.signedRequests).
	made := auth.NewMemorySignedRequests()
	sigs := NewDeviceSignatureHandler(made)
	sigs.now = func() time.Time { return r.now }
	sigs.Register(r.mux)
	key := func() (*ecdsa.PrivateKey, []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		return k, der
	}
	deviceKey, deviceDER := key()
	approval, approvalDER := key()
	f := &firstStart{t: t, r: r, dom: dom, deviceKey: deviceKey, approval: approval,
		h: auth.DefaultRoutePolicy().WithSignedRequests(made).WithClock(func() time.Time { return r.now }).Wrap(r.mux),
		phone: auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin,
			Roles: membership.IdentityRoles(membership.RoleAdmin), DeviceKey: deviceDER, DeviceSessionID: "sess-phone",
			DeviceID: "dev_phone", DeviceName: "Pixel", ApprovalKey: approvalDER},
		at: r.now.Format(time.RFC3339), batchPath: "/v1/connections/batch"}
	f.batchBody = []byte(`{"connections":[{"name":"payments","tools":["refund"]}]}`)
	f.draft, _ = json.Marshal(map[string]any{"agent_id": "coach", "cards": []any{cardMax("coach-refund", "coach", 30000)}})
	return f
}

// hash is the sha256 hex of a request's own kiff.request.v1 message.
func (f *firstStart) hash(method, path string, body []byte) string {
	s := sha256.Sum256(auth.RequestMessage("t1", method, path, body, f.at))
	return hex.EncodeToString(s[:])
}

func (f *firstStart) batchHash() string { return f.hash(http.MethodPost, f.batchPath, f.batchBody) }
func (f *firstStart) draftHash() string {
	return f.hash(http.MethodPost, "/v1/me/agent-drafts", f.draft)
}

// pair is k's one signature over the batch and the draft.
func (f *firstStart) pair(k *ecdsa.PrivateKey) string {
	d := sha256.Sum256(auth.RequestsMessage("t1", f.at, f.batchHash(), f.draftHash()))
	sig, _ := ecdsa.SignASN1(rand.Reader, k, d[:])
	return base64.StdEncoding.EncodeToString(sig)
}

// check asks, as the gateway does for the batch, whether the phone signed it.
func (f *firstStart) check(uri, bodySHA, header string) (int, map[string]any) {
	f.t.Helper()
	b, _ := json.Marshal(map[string]string{"method": http.MethodPost, "uri": uri, "body_sha256": bodySHA, "signature": header})
	req := httptest.NewRequest(http.MethodPost, "/v1/me/device-signature", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), f.phone)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (f *firstStart) checkBatch(header string) (int, map[string]any) {
	s := sha256.Sum256(f.batchBody)
	return f.check(f.batchPath, hex.EncodeToString(s[:]), header)
}

// submit sends a draft body with header, from the phone.
func (f *firstStart) submit(body []byte, header string) (int, map[string]any) {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/me/agent-drafts", bytes.NewReader(body))
	if header != "" {
		req.Header.Set(auth.DeviceSignatureHeader, header)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), f.phone)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (f *firstStart) batchHeader(sig string) string {
	return "t=" + f.at + ",v3=" + sig + ",with=sha256:" + f.draftHash()
}
func (f *firstStart) draftHeader(sig string) string {
	return "t=" + f.at + ",v3=" + sig + ",with=sha256:" + f.batchHash()
}

// connected is the batch taking effect: the account's tools now declare refund.
func (f *firstStart) connected() { f.dom.set(refundCentsYAML) }

func (f *firstStart) noCoach(when string) {
	f.t.Helper()
	if active := f.r.anyCardActive(); len(active) != 0 {
		f.t.Fatalf("%s: Cards active: %v", when, active)
	}
}

// The order the apps send, as the positive the negatives stand against: the
// batch's check, then the draft, each on the one signature, once.
func TestAFirstStartTakesOneSignatureForTheBatchThenTheDraft(t *testing.T) {
	t.Parallel()
	f := newFirstStart(t)
	sig := f.pair(f.approval)
	if c, out := f.checkBatch(f.batchHeader(sig)); c != http.StatusOK || out["key"] != auth.KeyCritical {
		t.Fatalf("the batch's check: %d %v", c, out)
	}
	f.connected()
	if c, out := f.submit(f.draft, f.draftHeader(sig)); c != http.StatusCreated || out["status"] != "confirmed" {
		t.Fatalf("the draft: %d %v", c, out)
	}
	if !f.r.cardActive("coach-refund") {
		t.Fatal("Coach's Card isn't active after the first start")
	}
}

// Category: authority
// Attack: the combined signature made with the device key (a tap, no fingerprint), on the batch's check or the draft
// Refused: both; the gateway is never told the phone signed the batch
func TestAFirstStartSignedWithTheDeviceKeyIsRefused(t *testing.T) {
	t.Parallel()
	f := newFirstStart(t)
	sig := f.pair(f.deviceKey)
	if c, out := f.checkBatch(f.batchHeader(sig)); c == http.StatusOK || out["signed"] == true {
		t.Errorf("the gateway's check took a device-key v3 for the batch: %d %v", c, out)
	}
	f.connected()
	if c, out := f.submit(f.draft, f.draftHeader(sig)); c != http.StatusForbidden {
		t.Errorf("the draft on a device-key v3: %d %v", c, out)
	}
	f.noCoach("after device-key signatures")
}

// Category: authority
// Attack: the draft sent before the batch, so its Card would stand over a tool not yet connected
// Refused: the draft, and its half of the signature is used: once the tools
// are connected, the same draft needs a fresh fingerprint
func TestAFirstStartsDraftBeforeItsBatchIsRefused(t *testing.T) {
	t.Parallel()
	f := newFirstStart(t)
	sig := f.pair(f.approval)
	if c, out := f.submit(f.draft, f.draftHeader(sig)); c/100 == 2 {
		t.Fatalf("the draft before its tools were connected: %d %v", c, out)
	}
	f.noCoach("after the draft before the batch")
	if c, out := f.checkBatch(f.batchHeader(sig)); c == http.StatusOK && out["key"] != auth.KeyCritical {
		t.Errorf("the batch's check after the refused draft: %d %v", c, out)
	}
	f.noCoach("after the batch that followed a refused draft")
	f.connected()
	if c, out := f.submit(f.draft, f.draftHeader(sig)); c != http.StatusConflict {
		t.Errorf("the refused draft again on the same signature, once its tools are connected: %d %v", c, out)
	}
	f.noCoach("after the refused draft was sent again")
}

// Category: authority
// Attack: the draft taken on the pair's signature as if signed alone: no with, a v1 header, or with naming itself
// Refused: each; nothing drafted
func TestAFirstStartsDraftAloneOnThatSignatureIsRefused(t *testing.T) {
	t.Parallel()
	f := newFirstStart(t)
	f.connected()
	sig := f.pair(f.approval)
	for name, h := range map[string]string{
		"v3 without with":    "t=" + f.at + ",v3=" + sig,
		"as a v1":            "t=" + f.at + ",v1=" + sig,
		"with naming itself": "t=" + f.at + ",v3=" + sig + ",with=sha256:" + f.draftHash(),
	} {
		if c, out := f.submit(f.draft, h); c/100 == 2 {
			t.Errorf("%s: %d %v", name, c, out)
		}
	}
	f.noCoach("after the draft alone")
}

// Category: authority
// Attack: a third request on the same signature: another draft, the batch's check on another path, either one again
// Refused: each; one Coach, at the limit signed
func TestAFirstStartsSignatureStandsForNoThirdRequest(t *testing.T) {
	t.Parallel()
	f := newFirstStart(t)
	sig := f.pair(f.approval)
	if c, out := f.checkBatch(f.batchHeader(sig)); c != http.StatusOK {
		t.Fatalf("the batch's check: %d %v", c, out)
	}
	if c, out := f.checkBatch(f.batchHeader(sig)); c != http.StatusConflict {
		t.Errorf("the batch's check again: %d %v", c, out)
	}
	s := sha256.Sum256(f.batchBody)
	if c, out := f.check("/v1/connections", hex.EncodeToString(s[:]), f.batchHeader(sig)); c != http.StatusForbidden {
		t.Errorf("the signature on another path at the gateway: %d %v", c, out)
	}
	f.connected()
	bigger, _ := json.Marshal(map[string]any{"agent_id": "coach", "cards": []any{cardMax("coach-refund", "coach", 900000)}})
	if c, out := f.submit(bigger, f.draftHeader(sig)); c != http.StatusForbidden {
		t.Errorf("another draft on the signature: %d %v", c, out)
	}
	other, _ := json.Marshal(map[string]any{"agent_id": "coach2", "cards": []any{cardMax("coach2-refund", "coach2", 30000)}})
	if c, out := f.submit(other, f.draftHeader(sig)); c != http.StatusForbidden {
		t.Errorf("a second agent on the signature: %d %v", c, out)
	}
	if c, out := f.submit(f.draft, f.draftHeader(sig)); c != http.StatusCreated {
		t.Fatalf("the draft signed: %d %v", c, out)
	}
	if c, out := f.submit(f.draft, f.draftHeader(sig)); c != http.StatusConflict {
		t.Errorf("the draft again: %d %v", c, out)
	}
	if got := f.r.anyCardActive(); len(got) != 1 || f.r.perActionMax("coach-refund") != 30000 {
		t.Errorf("Cards after the third requests: %v, per refund %d", got, f.r.perActionMax("coach-refund"))
	}
}
