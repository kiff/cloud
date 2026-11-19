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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handoff"
	"github.com/kiff/cloud/api/internal/receipt"
)

type handoffNotes struct {
	mu  sync.Mutex
	got []alert.Event
}

func (n *handoffNotes) Notify(ev alert.Event) { n.mu.Lock(); n.got = append(n.got, ev); n.mu.Unlock() }

type handoffRig struct {
	store    *handoff.Memory
	receipts *receipt.MemoryStore
	notes    *handoffNotes
	mux      *http.ServeMux
	now      time.Time
}

func newHandoffRig(t *testing.T) *handoffRig {
	t.Helper()
	rig := &handoffRig{store: handoff.NewMemory(), receipts: receipt.NewMemoryStore(), notes: &handoffNotes{},
		mux: http.NewServeMux(), now: time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sw := &handoff.Sweeper{Store: rig.store, Receipts: rig.receipts, Alerts: rig.notes, Logger: logger}
	h := NewHandoffsHandler(rig.store, nil, sw, rig.notes, logger)
	h.now = func() time.Time { return rig.now }
	h.Register(rig.mux)
	// What the gateway recorded: a1's sent call, a1's refused and held
	// calls, and a2's sent call.
	// Each is named by its operation id here, its proposal id "tc-" + that.
	for op, c := range map[string]handoff.Call{
		"prep-1":    {Agent: "a1", State: "forwarded", ToolOutcome: "done"},
		"refused-1": {Agent: "a1", State: "refused"},
		"held-1":    {Agent: "a1", State: "held"},
		"a2-call":   {Agent: "a2", State: "forwarded"},
	} {
		c.Operation, c.CreatedAt = op, rig.now.Add(-time.Hour)
		rig.store.PutCall("t1", "tc-"+op, c)
	}
	return rig
}

func handoffAgent(agent string) auth.Identity {
	return auth.Identity{Subject: "key_" + agent, Tenant: "t1", APIKey: true, AgentID: agent}
}

func handoffPerson() auth.Identity { return auth.Identity{Subject: "user_1", Tenant: "t1"} }

func (rig *handoffRig) do(t *testing.T, id auth.Identity, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b)).WithContext(auth.WithIdentity(context.Background(), id))
	w := httptest.NewRecorder()
	rig.mux.ServeHTTP(w, r)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func raiseBody(op string, prepared ...string) map[string]any {
	return map[string]any{"what": "Approve 3 supplier payments, €4,210 in all", "why": "Your bank asks you to approve transfers yourself",
		"link": "https://app.qonto.com/requests/r1?token=SECRET", "about": "Batch 11 Nov", "prepared_by": prepared, "operation": op}
}

// A taf raises a hand-off with its own key; KIFF keeps the link's host only
// and saves its raised receipt. Asking again with the same operation
// answers the same one.
func TestHandoffRaise(t *testing.T) {
	rig := newHandoffRig(t)
	code, got := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("op-1", "prep-1"))
	if code != http.StatusCreated || got["status"] != "waiting" || got["link_host"] != "app.qonto.com" ||
		len(got["prepared_by"].([]any)) != 1 || got["prepared_by"].([]any)[0] != "tc-prep-1" {
		t.Fatalf("raise = %d %v", code, got)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "/requests/") {
		t.Errorf("the link's path came back: %s", b)
	}
	id := got["id"].(string)
	if r, err := rig.receipts.Get(context.Background(), "t1", receipt.HandoffReceiptTraceID(id)); err != nil || r.Outcome != "handed_to_owner" {
		t.Errorf("raised receipt: %+v %v", r, err)
	}
	code, again := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("op-1", "prep-1"))
	if code != http.StatusOK || again["id"] != id {
		t.Errorf("raised again = %d %v", code, again)
	}
	// A person, or a key bound to no agent, raises none.
	for _, who := range []auth.Identity{handoffPerson(), {Subject: "acct", Tenant: "t1", APIKey: true}} {
		if code, _ := rig.do(t, who, "POST", "/v1/me/handoffs", raiseBody("op-x")); code != http.StatusForbidden {
			t.Errorf("%s raised one: %d", who.Subject, code)
		}
	}
}

// A hand-off never makes a refused call allowed: every call it names was
// the agent's own and was sent on its own.
func TestHandoffPreparedByMustBeOwnSentCalls(t *testing.T) {
	rig := newHandoffRig(t)
	for _, p := range []string{"refused-1", "held-1", "a2-call", "no-such-call"} {
		code, got := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("op-"+p, "prep-1", p))
		if code != http.StatusUnprocessableEntity || got["error"] != "prepared_by_invalid" {
			t.Errorf("%s: %d %v", p, code, got)
		}
	}
}

func signHandoff(t *testing.T, k *ecdsa.PrivateKey, a auth.HandoffAnswer) string {
	t.Helper()
	sum := sha256.Sum256(a.Message())
	sig, err := ecdsa.SignASN1(rand.Reader, k, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// Only a person answers, once; a phone's session answers only with its
// device key's signature over kiff.handoff.v1; the agent's runtime is told.
func TestHandoffAnswer(t *testing.T) {
	rig := newHandoffRig(t)
	_, got := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("op-1", "prep-1"))
	id := got["id"].(string)
	path := "/v1/me/handoffs/" + id + "/answer"
	if code, _ := rig.do(t, handoffAgent("a1"), "POST", path, map[string]any{"answer": "done"}); code != http.StatusForbidden {
		t.Errorf("the agent answered its own hand-off: %d", code)
	}
	if code, _ := rig.do(t, handoffPerson(), "POST", path, map[string]any{"answer": "approve"}); code != http.StatusBadRequest {
		t.Errorf("a bad answer: %d", code)
	}

	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	phone := handoffPerson()
	phone.DeviceKey, phone.DeviceID = der, "dev_1"
	if code, got := rig.do(t, phone, "POST", path, map[string]any{"answer": "done"}); code != http.StatusForbidden || got["error"] != "signature_required" {
		t.Errorf("unsigned from a phone: %d %v", code, got)
	}
	signedAt := rig.now.Format(time.RFC3339)
	skipSig := signHandoff(t, k, auth.HandoffAnswer{TenantID: "t1", HandoffID: id, Answer: "skipped", Version: 1, SignedAt: signedAt})
	// A signature over "skipped" never answers "done".
	if code, got := rig.do(t, phone, "POST", path, map[string]any{"answer": "done",
		"signature": map[string]string{"signed_at": signedAt, "value": skipSig}}); code != http.StatusForbidden || got["error"] != "signature_invalid" {
		t.Errorf("a skipped signature answered done: %d %v", code, got)
	}
	doneSig := signHandoff(t, k, auth.HandoffAnswer{TenantID: "t1", HandoffID: id, Answer: "done", Version: 1, SignedAt: signedAt})
	code, got := rig.do(t, phone, "POST", path, map[string]any{"answer": "done", "signature": map[string]string{"signed_at": signedAt, "value": doneSig}})
	if code != http.StatusOK || got["status"] != "done" {
		t.Fatalf("signed answer = %d %v", code, got)
	}
	x, _ := rig.store.Get(context.Background(), "t1", id)
	if x.Device == nil || x.Device.KeyID != "dev_1" || !strings.HasPrefix(x.Device.Message, "kiff.handoff.v1\n") ||
		x.AnsweredBy != "user_1" || x.AnsweredByID != "user_1" {
		t.Errorf("kept answer = %+v", x)
	}
	if len(rig.notes.got) != 1 || rig.notes.got[0].Outcome != alert.HandoffAnswered || rig.notes.got[0].Handoff.Status != "done" {
		t.Errorf("alerts = %+v", rig.notes.got)
	}
	if code, got := rig.do(t, phone, "POST", path, map[string]any{"answer": "skipped",
		"signature": map[string]string{"signed_at": signedAt, "value": skipSig}}); code != http.StatusConflict || got["error"] != "already_answered" {
		t.Errorf("answered twice: %d %v", code, got)
	}
}

// After "Done", the taf names its own sent read, made after the answer; the
// settled receipt names it.
func TestHandoffCheck(t *testing.T) {
	rig := newHandoffRig(t)
	_, got := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("op-1", "prep-1"))
	id := got["id"].(string)
	path := "/v1/me/handoffs/" + id + "/check"
	if code, got := rig.do(t, handoffAgent("a1"), "POST", path, map[string]any{"operation": "prep-1"}); code != http.StatusConflict || got["error"] != "not_done" {
		t.Errorf("checked before done: %d %v", code, got)
	}
	if code, _ := rig.do(t, handoffPerson(), "POST", "/v1/me/handoffs/"+id+"/answer", map[string]any{"answer": "done"}); code != http.StatusOK {
		t.Fatalf("dashboard answer: %d", code)
	}
	rig.now = rig.now.Add(time.Minute)
	rig.store.PutCall("t1", "tc-read-1", handoff.Call{Operation: "read-1", Agent: "a1", State: "forwarded", ToolOutcome: "done", CreatedAt: rig.now})
	for p, want := range map[string]string{"prep-1": "made before the owner answered", "a2-call": "not one of this agent's", "refused-1": "not one of this agent's"} {
		if p == "refused-1" {
			rig.store.PutCall("t1", "tc-"+p, handoff.Call{Operation: p, Agent: "a1", State: "refused", CreatedAt: rig.now})
			want = "not sent"
		}
		if code, got := rig.do(t, handoffAgent("a1"), "POST", path, map[string]any{"operation": p}); code != http.StatusUnprocessableEntity ||
			!strings.Contains(got["detail"].(string), want) {
			t.Errorf("%s: %d %v", p, code, got)
		}
	}
	if code, _ := rig.do(t, handoffAgent("a2"), "POST", path, map[string]any{"operation": "read-1"}); code != http.StatusNotFound {
		t.Errorf("another agent checked it: %d", code)
	}
	code, got := rig.do(t, handoffAgent("a1"), "POST", path, map[string]any{"operation": "read-1"})
	if code != http.StatusOK || got["checked_by"] != "tc-read-1" {
		t.Fatalf("check = %d %v", code, got)
	}
	r, err := rig.receipts.Get(context.Background(), "t1", receipt.HandoffSettledTraceID(id))
	if err != nil || r.Outcome != "done" || r.CausalChain[len(r.CausalChain)-1].Kind != "handoff_checked" {
		t.Errorf("settled receipt: %+v %v", r, err)
	}
}

// Another agent's key, or none of the account's, reads nothing; the taf's
// own key and a person do.
func TestHandoffVisibility(t *testing.T) {
	rig := newHandoffRig(t)
	_, got := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("op-1"))
	id := got["id"].(string)
	if code, _ := rig.do(t, handoffAgent("a2"), "GET", "/v1/me/handoffs/"+id, nil); code != http.StatusNotFound {
		t.Errorf("another agent read it: %d", code)
	}
	other := handoffPerson()
	other.Tenant = "t2"
	if code, _ := rig.do(t, other, "GET", "/v1/me/handoffs/"+id, nil); code != http.StatusNotFound {
		t.Errorf("another account read it: %d", code)
	}
	for _, who := range []auth.Identity{handoffAgent("a1"), handoffPerson()} {
		if code, got := rig.do(t, who, "GET", "/v1/me/handoffs", nil); code != http.StatusOK || len(got["handoffs"].([]any)) != 1 {
			t.Errorf("%s list = %d %v", who.Subject, code, got)
		}
	}
	if code, got := rig.do(t, handoffAgent("a2"), "GET", "/v1/me/handoffs", nil); code != http.StatusOK || len(got["handoffs"].([]any)) != 0 {
		t.Errorf("a2 list = %d %v", code, got)
	}
}

// A step its owner skipped takes no check: the route says so before the
// store would.
func TestHandoffCheckAfterSkip(t *testing.T) {
	rig := newHandoffRig(t)
	_, got := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("op-1"))
	id := got["id"].(string)
	if code, _ := rig.do(t, handoffPerson(), "POST", "/v1/me/handoffs/"+id+"/answer", map[string]any{"answer": "skipped"}); code != http.StatusOK {
		t.Fatalf("skip: %d", code)
	}
	rig.store.PutCall("t1", "tc-read-1", handoff.Call{Operation: "read-1", Agent: "a1", State: "forwarded", CreatedAt: rig.now.Add(time.Minute)})
	if code, got := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs/"+id+"/check", map[string]any{"operation": "read-1"}); code != http.StatusConflict || got["error"] != "not_done" {
		t.Errorf("check after skip: %d %v", code, got)
	}
}

// The answer names the person by email when the session carries one.
func TestHandoffAnswerNamesTheEmail(t *testing.T) {
	rig := newHandoffRig(t)
	_, got := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("op-1"))
	id := got["id"].(string)
	p := handoffPerson()
	p.Email = "owner@example.com"
	if code, _ := rig.do(t, p, "POST", "/v1/me/handoffs/"+id+"/answer", map[string]any{"answer": "done"}); code != http.StatusOK {
		t.Fatalf("answer: %d", code)
	}
	if x, _ := rig.store.Get(context.Background(), "t1", id); x.AnsweredBy != "owner@example.com" || x.AnsweredByID != "user_1" {
		t.Errorf("answered by %q (%q)", x.AnsweredBy, x.AnsweredByID)
	}
}

// A later run's call for the same step answers the one already waiting.
func TestHandoffRaiseAlreadyWaiting(t *testing.T) {
	rig := newHandoffRig(t)
	_, first := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("run1-op"))
	code, got := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("run2-op"))
	if code != http.StatusOK || got["id"] != first["id"] || got["already_waiting"] != true {
		t.Fatalf("the same step again = %d %v", code, got)
	}
	if _, again := rig.do(t, handoffAgent("a1"), "POST", "/v1/me/handoffs", raiseBody("run1-op")); again["already_waiting"] != nil {
		t.Errorf("a replay says already_waiting: %v", again)
	}
}
