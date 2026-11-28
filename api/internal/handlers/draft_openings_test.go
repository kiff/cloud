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
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// classedTools is the gateway's tools' titles and recorded classes.
type classedTools map[string]tooloutcome.ToolClass

func (classedTools) ToolTitles(context.Context, string) (map[string]string, error) { return nil, nil }
func (c classedTools) ToolClasses(context.Context, string) (map[string]tooloutcome.ToolClass, error) {
	return c, nil
}

// A draft opens the narrowest category its granted tool's table gives, the
// whole class only when there is none, nothing for a read, a write or a
// conditioned tool (create_client_invoice: a draft-pinned call is a write,
// and its whole class would open finalising), and leaves a grant that names
// its own openings as it is.
func TestADraftOpensTheNarrowestCategoryFirst(t *testing.T) {
	classes := map[string]tooloutcome.ToolClass{
		"status":  {Class: "irreversible", Opens: []tooloutcome.ToolOpening{{Argument: "operation", Value: "finalize"}}},
		"send":    {Class: "irreversible"},
		"notes":   {Class: "write"},
		"refund":  {Class: "money"},
		"invoice": {Class: "irreversible", Conditioned: true},
	}
	body := mandateBody{Domain: gatewayDomain, Grants: []mandateGrantBody{
		{Action: "status"}, {Action: "send"}, {Action: "notes"}, {Action: "unknown"}, {Action: "invoice"},
		{Action: "refund", Opens: []mandateOpeningBody{{Class: "money", Argument: "currency", Value: "EUR"}}},
	}}
	if !draftOpenings(&body, classes) {
		t.Fatal("nothing opened")
	}
	want := [][]mandateOpeningBody{
		{{Class: "irreversible", Argument: "operation", Value: "finalize"}},
		{{Class: "irreversible"}},
		nil, nil, nil,
		{{Class: "money", Argument: "currency", Value: "EUR"}},
	}
	for i, g := range body.Grants {
		got, _ := json.Marshal(g.Opens)
		w, _ := json.Marshal(want[i])
		if string(got) != string(w) {
			t.Errorf("%s opens %s, want %s", g.Action, got, w)
		}
	}
	other := mandateBody{Domain: "orders", Grants: []mandateGrantBody{{Action: "send"}}}
	if draftOpenings(&other, classes) || other.Grants[0].Opens != nil {
		t.Errorf("a Card outside the gateway was opened: %+v", other.Grants)
	}
}

// K4, step 6b: KIFF fills a taf's first Card's openings, and each is its own
// line the owner reads before the fingerprint. A tap, even inside the taf's
// own approval window, is refused with critical_key_required; the approval
// key confirms it, and the Card in force opens what the draft said. The
// confirm never fills again: a class that changes after the draft opens
// nothing the owner didn't read.
// Category: authority
// Attack: an opening KIFF filled in is signed with a tap, or reaches the Card without being shown
func TestAFilledOpeningIsReadAndSignedBehindTheFingerprint(t *testing.T) {
	t.Parallel()
	tools := classedTools{"refund": {Class: "money"}}
	r := newDraftRig(t, WithMandatesToolTitles(tools))
	windows := auth.NewMemoryWindowStore()
	policed := auth.DefaultRoutePolicy().WithWindows(windows).WithClock(func() time.Time { return r.now }).Wrap(r.mux)
	key := func() (*ecdsa.PrivateKey, []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		return k, der
	}
	deviceKey, deviceDER := key()
	approvalKey, approvalDER := key()
	phone := auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodSession, MemberRole: membership.RoleAdmin,
		Roles: membership.IdentityRoles(membership.RoleAdmin), DeviceKey: deviceDER, DeviceSessionID: "sess-phone", DeviceID: "dev_phone", ApprovalKey: approvalDER}
	win := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "t1", Subject: "alice", DeviceID: "dev_phone", SessionID: "sess-phone",
		OpenedAt: r.now, ExpiresAt: r.now.Add(auth.WindowLength), CapAt: r.now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(approvalDER), Agent: "sam"}
	if err := windows.Open(t.Context(), win); err != nil {
		t.Fatal(err)
	}
	confirm := func(draft, version string, k *ecdsa.PrivateKey, window string) (int, map[string]any) {
		path := "/v1/me/agent-drafts/" + draft + "/confirm"
		body, _ := json.Marshal(map[string]string{"version": version})
		ts := r.now.Format(time.RFC3339)
		digest := sha256.Sum256(auth.RequestMessageWindow("t1", http.MethodPost, path, body, ts, window))
		sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		header := "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)
		if window != "" {
			header = "t=" + ts + ",v2=" + base64.StdEncoding.EncodeToString(sig) + ",w=" + window
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set(auth.DeviceSignatureHeader, header)
		rec := httptest.NewRecorder()
		policed.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), phone)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, d := r.submit(agentKey("setup-agent", ""), map[string]any{"agent_id": "sam", "cards": []any{cardMax("sam-refund", "sam", 10000)}})
	if code != http.StatusCreated || d["status"] != "draft" {
		t.Fatalf("drafting sam's first Card: %d %v", code, d)
	}
	id, version := d["id"].(string), d["version"].(string)
	grant := d["cards"].([]any)[0].(map[string]any)["grants"].([]any)[0].(map[string]any)
	if opens, _ := json.Marshal(grant["opens"]); string(opens) != `[{"class":"money"}]` {
		t.Fatalf("the draft's grant opens %s", opens)
	}
	rr := do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts/"+id, nil)
	var view struct {
		ConfirmKey string `json:"confirm_key"`
		Widens     []struct{ Card, Kind, Words string }
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &view)
	if view.ConfirmKey != "critical" || len(view.Widens) != 1 || view.Widens[0].Kind != "opens" || view.Widens[0].Words != "Opens money calls for refund" {
		t.Fatalf("the draft's view: %s", rr.Body.String())
	}

	// The class changes before the confirm: what is confirmed is what was read.
	tools["refund"] = tooloutcome.ToolClass{Class: "irreversible"}
	if code, out := confirm(id, version, deviceKey, win.ID); code != http.StatusForbidden || out["error"] != "critical_key_required" || out["reason"] == "window_scope" {
		t.Fatalf("a tap confirming a filled opening: %d %v", code, out)
	}
	if ms, _ := r.store.List(t.Context(), "t1"); len(ms) != 0 {
		t.Fatalf("the refused tap issued a Card: %+v", ms)
	}
	if code, out := confirm(id, version, approvalKey, ""); code != http.StatusOK {
		t.Fatalf("the approval key confirming it: %d %v", code, out)
	}
	ms, _ := r.store.List(t.Context(), "t1")
	if len(ms) != 1 || len(ms[0].Grants[0].Opens) != 1 || ms[0].Grants[0].Opens[0] != (mandate.Opening{Class: "money"}) {
		t.Fatalf("the Card in force: %+v", ms)
	}
}
