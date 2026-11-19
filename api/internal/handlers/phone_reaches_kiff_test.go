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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// What a tafs user did in KIFF, they now do from the phone, through
// the real route policy and only with the phone's signature where it
// widens what an agent may do.

type phoneRig struct {
	t   *testing.T
	key *ecdsa.PrivateKey
	der []byte
}

func newPhoneRig(t *testing.T) phoneRig {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	return phoneRig{t: t, key: key, der: der}
}

// on makes id a phone's session bound to this key.
func (p phoneRig) on(id auth.Identity) auth.Identity {
	id.Method, id.DeviceKey, id.DeviceSessionID, id.DeviceName = auth.MethodDeveloper, p.der, "sess-phone", "tafs on iPhone"
	return id
}

func (p phoneRig) sign(message []byte) string {
	p.t.Helper()
	digest := sha256.Sum256(message)
	sig, err := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// signedRequest signs a setup call as tafs does: KIFF-Device-Signature over
// the tenant, method, path and body.
func (p phoneRig) signedRequest(tenantID, method, path, body string, at time.Time) string {
	ts := at.UTC().Format(time.RFC3339)
	return "t=" + ts + ",v1=" + p.sign(auth.RequestMessage(tenantID, method, path, []byte(body), ts))
}

func serve(h http.Handler, id auth.Identity, method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), id))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// change-card from a phone is an answer: it takes the phone's signature
// over exactly that answer and the hold's accounting version, keeps the
// proof for the receipt, and can't be replayed once the hold is answered.
// It still needs cards.manage. What it changes depends on the breach: a
// per-action limit is raised for good (this test), a reached total gets
// room for this period only (the next).
func TestPhoneChangesTheCardOnAHeldCallWithItsSignature(t *testing.T) {
	f := newHoldFixture(t)
	policed := auth.DefaultRoutePolicy().Wrap(f.mux)
	p := newPhoneRig(t)
	phone := p.on(person("alice", membership.RoleAdmin))
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	id := f.hold("agent://a")
	held, err := f.store.Exception(context.Background(), "t1", id)
	if err != nil {
		t.Fatal(err)
	}
	signed := func(verb string, version int) string {
		a := auth.Answer{TenantID: "t1", ExceptionID: id, Answer: verb, AccountingVersion: version,
			SignedAt: now.Add(-30 * time.Second).Format(time.RFC3339)}
		b, _ := json.Marshal(map[string]any{"signature": map[string]string{"signed_at": a.SignedAt, "value": p.sign(a.Message())}})
		return string(b)
	}
	path := "/v1/me/card-exceptions/" + id + "/change-card"

	for name, c := range map[string]struct {
		body, want string
	}{
		"unsigned":                   {`{}`, "signature_required"},
		"signed to approve":          {signed("approve-once", held.AccountingVersion), "signature_invalid"},
		"another accounting version": {signed("change-card", held.AccountingVersion+1), "signature_invalid"},
	} {
		if rr := serve(policed, phone, http.MethodPost, path, c.body, nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), c.want) {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
	}
	if got, _ := f.store.Exception(context.Background(), "t1", id); got.Status != mandate.ExceptionHeld {
		t.Fatalf("a refused answer changed the hold: %s", got.Status)
	}
	// An approver may answer, but not change a Card, from a phone too.
	approver := p.on(person("carol", membership.RoleApprover))
	if rr := serve(policed, approver, http.MethodPost, path, signed("change-card", held.AccountingVersion), nil); rr.Code != http.StatusForbidden {
		t.Errorf("an approver's phone changed the Card: %d %s", rr.Code, rr.Body.String())
	}

	body := signed("change-card", held.AccountingVersion)
	rr := serve(policed, phone, http.MethodPost, path, body, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"resolution":"change-card"`) {
		t.Fatalf("signed change-card: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := f.members.HoldAnswers(context.Background(), "t1", []string{id})
	if d := got[id].Device; d == nil || d.Name != "tafs on iPhone" || !strings.Contains(d.Message, "\nchange-card\n") {
		t.Fatalf("device proof = %+v", got[id].Device)
	}
	if rr := serve(policed, phone, http.MethodPost, path, body, nil); rr.Code != http.StatusConflict {
		t.Errorf("replayed change-card: %d %s", rr.Code, rr.Body.String())
	}
	// The held call was over the per-action limit (100), so the Card's
	// limit is now its amount, for every later call too.
	ms, err := f.store.ForSubject(context.Background(), "t1", "agent://a")
	if err != nil || len(ms) != 1 {
		t.Fatal(ms, err)
	}
	if max := ms[0].Grants[0].PerAction[0].Max; max != 500 || len(ms[0].Aggregates[0].ExtraRoom) != 0 {
		t.Fatalf("after change-card: per-action max %d, extra room %+v", max, ms[0].Aggregates[0].ExtraRoom)
	}
}

// A held call that reaches a Card's total gets room for this period from
// a phone's change-card; the total itself stays as it was.
func TestPhoneChangeCardOnAReachedTotalAddsRoomForThisPeriod(t *testing.T) {
	f := newHoldFixture(t)
	policed := auth.DefaultRoutePolicy().Wrap(f.mux)
	p := newPhoneRig(t)
	phone := p.on(person("alice", membership.RoleAdmin))
	ctx := context.Background()
	card := mandate.Mandate{ID: "card-b", TenantID: "t1", Subject: "agent://b", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 1000}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 600, Window: mandate.WindowCalendarDay}}}
	if err := f.store.Issue(ctx, card, "erin"); err != nil {
		t.Fatal(err)
	}
	req := mandate.Request{Subject: "agent://b", Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}}
	if d, err := f.auth.Authorize(ctx, "t1", "p-b1", req); err != nil || d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("first call: %+v %v", d, err)
	}
	d, err := f.auth.Authorize(ctx, "t1", "p-b2", req)
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("second call: %+v %v", d, err)
	}
	held, err := f.store.Exception(ctx, "t1", d.ExceptionID)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.Answer{TenantID: "t1", ExceptionID: held.ID, Answer: "change-card", AccountingVersion: held.AccountingVersion,
		SignedAt: time.Date(2026, 9, 12, 11, 59, 30, 0, time.UTC).Format(time.RFC3339)}
	b, _ := json.Marshal(map[string]any{"signature": map[string]string{"signed_at": a.SignedAt, "value": p.sign(a.Message())}})
	if rr := serve(policed, phone, http.MethodPost, "/v1/me/card-exceptions/"+held.ID+"/change-card", string(b), nil); rr.Code != http.StatusOK {
		t.Fatalf("signed change-card: %d %s", rr.Code, rr.Body.String())
	}
	ms, err := f.store.ForSubject(ctx, "t1", "agent://b")
	if err != nil || len(ms) != 1 {
		t.Fatal(ms, err)
	}
	agg := ms[0].Aggregates[0]
	if agg.Limit != 600 || ms[0].Grants[0].PerAction[0].Max != 1000 || len(agg.ExtraRoom) != 1 ||
		agg.ExtraRoom[0].Duration != mandate.RoomThisWindow || agg.ExtraRoom[0].Amount != 400 {
		t.Fatalf("after change-card: limit %d, per-action %d, room %+v", agg.Limit, ms[0].Grants[0].PerAction[0].Max, agg.ExtraRoom)
	}
}

// Extra room from a phone is a signed setup call. A captured request
// replays to nothing, and a re-signed retry of the same revision grants
// the room once.
func TestPhoneGivesExtraRoomOnlySigned(t *testing.T) {
	mux, s := mandatesServer(t, owner())
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	m := mandate.Mandate{TenantID: "t1", ID: "room", Subject: "agent-a", Grants: []mandate.Grant{{Action: "refund"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay}}}
	if err := s.Issue(context.Background(), m, "owner"); err != nil {
		t.Fatal(err)
	}
	p := newPhoneRig(t)
	phone := p.on(owner())
	path := "/v1/me/mandates/room/extra-room"
	body := `{"revision_id":"phone-room","quantity":"count","window":"calendar_day","measure":"attempts","amount":3,"expected_base_limit":10}`

	if rr := serve(policed, phone, http.MethodPost, path, body, nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "signature_required") {
		t.Fatalf("unsigned extra room: %d %s", rr.Code, rr.Body.String())
	}
	other := `{"revision_id":"phone-room","quantity":"count","window":"calendar_day","measure":"attempts","amount":300,"expected_base_limit":10}`
	if rr := serve(policed, phone, http.MethodPost, path, other, map[string]string{auth.DeviceSignatureHeader: p.signedRequest("t1", http.MethodPost, path, body, time.Now())}); rr.Code != http.StatusForbidden {
		t.Fatalf("a signature over another body: %d %s", rr.Code, rr.Body.String())
	}
	sig := map[string]string{auth.DeviceSignatureHeader: p.signedRequest("t1", http.MethodPost, path, body, time.Now())}
	if rr := serve(policed, phone, http.MethodPost, path, body, sig); rr.Code != http.StatusOK {
		t.Fatalf("signed extra room: %d %s", rr.Code, rr.Body.String())
	}
	if rr := serve(policed, phone, http.MethodPost, path, body, sig); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "signature_replayed") {
		t.Fatalf("replayed extra room: %d %s", rr.Code, rr.Body.String())
	}
	retry := map[string]string{auth.DeviceSignatureHeader: p.signedRequest("t1", http.MethodPost, path, body, time.Now().Add(time.Second))}
	if rr := serve(policed, phone, http.MethodPost, path, body, retry); rr.Code != http.StatusOK {
		t.Fatalf("re-signed retry: %d %s", rr.Code, rr.Body.String())
	}
	ms, err := s.ForSubject(context.Background(), "t1", "agent-a")
	if err != nil || len(ms) != 1 || len(ms[0].Aggregates[0].ExtraRoom) != 1 || ms[0].Aggregates[0].ExtraRoom[0].Amount != 3 {
		t.Fatalf("room = %+v %v", ms, err)
	}
}

// The drafts waiting for confirmation are listed to the people who can
// confirm them, phone included, and to nobody else.
func TestWaitingDraftsListedToWhoMayConfirm(t *testing.T) {
	r := newDraftRig(t)
	policed := auth.DefaultRoutePolicy().Wrap(r.mux)
	if code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "rae", "label": "Rae", "cards": []any{card("rae-refund", "rae")}}); code != http.StatusCreated || d["status"] != "draft" {
		t.Fatalf("submit: %d %v", code, d)
	}
	if code, d := r.submit(admin(), map[string]any{"agent_id": "bo", "label": "Bo", "cards": []any{card("bo-refund", "bo")}}); code != http.StatusCreated || d["status"] != "confirmed" {
		t.Fatalf("owner submit: %d %v", code, d)
	}
	list := func(id auth.Identity) (int, []map[string]any) {
		rr := serve(policed, id, http.MethodGet, "/v1/me/agent-drafts", "", nil)
		var out struct {
			Drafts []map[string]any `json:"drafts"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out.Drafts
	}
	for _, id := range []auth.Identity{admin(), newPhoneRig(t).on(admin())} {
		code, ds := list(id)
		if code != http.StatusOK || len(ds) != 1 || ds[0]["agent_id"] != "rae" || ds[0]["claim_secret"] != nil {
			t.Fatalf("%s: %d %v", id.Method, code, ds)
		}
	}
	for _, role := range []membership.Role{membership.RoleEditor, membership.RoleApprover, membership.RoleViewer} {
		if code, _ := list(person("erin", role)); code != http.StatusForbidden {
			t.Errorf("%s listed waiting drafts: %d", role, code)
		}
	}
	other := admin()
	other.Tenant = "t2"
	if code, ds := list(other); code != http.StatusOK || len(ds) != 0 {
		t.Errorf("another account: %d %v", code, ds)
	}
}

// A call over both a per-action limit and a reached total takes two
// answers: the first change adds room for this period only and
// the call is held again for the per-action limit alone; changing the
// Card on that hold raises the limit, and the call then fits. The docs
// tell apps to expect this.
func TestChangeCardOnBothLimitsTakesTwoAnswers(t *testing.T) {
	f := newHoldFixture(t)
	ctx := context.Background()
	card := mandate.Mandate{ID: "card-m", TenantID: "t1", Subject: "agent://m", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 300}}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 600, Window: mandate.WindowCalendarDay}}}
	if err := f.store.Issue(ctx, card, "erin"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"m1", "m2"} {
		if d, err := f.auth.Authorize(ctx, "t1", p, mandate.Request{Subject: "agent://m", Action: "REFUND_INVOICE",
			Parameters: map[string]int64{"amount": 250}}); err != nil || d.Outcome != mandate.OutcomeWithinLimit {
			t.Fatalf("%s: %+v %v", p, d, err)
		}
	}
	big := mandate.Request{Subject: "agent://m", Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 400}}
	change := func(proposal string) mandate.Mandate {
		t.Helper()
		d, err := f.auth.Authorize(ctx, "t1", proposal, big)
		if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
			t.Fatalf("%s: %+v %v", proposal, d, err)
		}
		if rr := do(t, f.mux, person("alice", membership.RoleAdmin), http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/change-card", nil); rr.Code != http.StatusOK {
			t.Fatalf("change-card: %d %s", rr.Code, rr.Body.String())
		}
		ms, _ := f.store.ForSubject(ctx, "t1", "agent://m")
		return ms[0]
	}
	m := change("m3")
	if m.Grants[0].PerAction[0].Max != 300 || m.Aggregates[0].Limit != 600 || len(m.Aggregates[0].ExtraRoom) != 1 || m.Aggregates[0].ExtraRoom[0].Duration != mandate.RoomThisWindow {
		t.Fatalf("first answer: per-action %d, total %d, room %+v", m.Grants[0].PerAction[0].Max, m.Aggregates[0].Limit, m.Aggregates[0].ExtraRoom)
	}
	m = change("m4")
	if m.Grants[0].PerAction[0].Max != 400 {
		t.Fatalf("second answer: per-action %d", m.Grants[0].PerAction[0].Max)
	}
	if d, err := f.auth.Authorize(ctx, "t1", "m5", big); err != nil || d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("after two answers: %+v %v", d, err)
	}
}

// Archiving an agent from a phone is a signed setup call (kiff/agents#78):
// unsigned it's refused, signed it archives, and a viewer's signature still
// lacks cards.manage.
func TestPhoneArchivesAnAgentOnlySigned(t *testing.T) {
	store := agents.NewMemoryStore()
	if err := store.Create(context.Background(), agents.Agent{TenantID: "t1", ID: "eval-taf", CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewAgentsHandler(store).Register(mux)
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	p := newPhoneRig(t)
	path := "/v1/me/agents/eval-taf/archive"

	if rr := serve(policed, p.on(person("alice", membership.RoleAdmin)), http.MethodPost, path, "", nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "signature_required") {
		t.Fatalf("unsigned: %d %s", rr.Code, rr.Body.String())
	}
	viewerSig := map[string]string{auth.DeviceSignatureHeader: p.signedRequest("t1", http.MethodPost, path, "", time.Now())}
	if rr := serve(policed, p.on(person("vic", membership.RoleViewer)), http.MethodPost, path, "", viewerSig); rr.Code != http.StatusForbidden {
		t.Fatalf("a viewer's phone: %d %s", rr.Code, rr.Body.String())
	}
	sig := map[string]string{auth.DeviceSignatureHeader: p.signedRequest("t1", http.MethodPost, path, "", time.Now().Add(time.Second))}
	if rr := serve(policed, p.on(person("alice", membership.RoleAdmin)), http.MethodPost, path, "", sig); rr.Code != http.StatusOK {
		t.Fatalf("signed: %d %s", rr.Code, rr.Body.String())
	}
	list, _ := store.List(context.Background(), "t1")
	if len(list) != 1 || list[0].ArchivedAt == nil {
		t.Fatalf("not archived: %+v", list)
	}
}

// The app lists the account's people, to add one to a group or share a
// taf with them; inviting or changing anyone stays off a phone.
func TestPhoneReadsTheAccountsPeopleOnly(t *testing.T) {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("GET /v1/tenant/members", ok)
	mux.HandleFunc("POST /v1/tenant/invites", ok)
	mux.HandleFunc("PUT /v1/tenant/members/{subject}/role", ok)
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	phone := newPhoneRig(t).on(person("alice", membership.RoleAdmin))
	if rr := serve(policed, phone, http.MethodGet, "/v1/tenant/members", "", nil); rr.Code != http.StatusOK {
		t.Errorf("listing people: %d %s", rr.Code, rr.Body.String())
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/tenant/invites"},
		{http.MethodPut, "/v1/tenant/members/bob/role"},
	} {
		if rr := serve(policed, phone, c.method, c.path, `{}`, nil); rr.Code != http.StatusForbidden {
			t.Errorf("%s %s from a phone: %d", c.method, c.path, rr.Code)
		}
	}
}
