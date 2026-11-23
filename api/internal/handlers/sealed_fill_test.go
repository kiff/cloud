package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// A sealed fill is named by its arguments: a "sealed" value shaped as a
// handle, at any depth. Nothing else is one, whatever its keys.
func TestASealedFillIsNamedByItsHandle(t *testing.T) {
	for args, want := range map[string]bool{
		`{"url":"https://shop.example/","read":"r1","steps":[{"fill":"f1","sealed":"sec_a1:password"}]}`: true,
		`{"steps":[{"click":"c1"},{"fill":"f2","sealed":"sec_b:username"}]}`:                             true,
		`{"act":{"kind":"fill","ref":"f1","sealed":"sec_x:password:username"}}`:                          true, // malformed, still a fill
		`{"steps":[{"fill":"f1","value":"lamp"}]}`:                                                       false,
		`{"sealed":true,"document":"contract"}`:                                                          false,
		`{"sealed":"envelope"}`:                                                                          false,
		`{"note":"sec_a1:password"}`:                                                                     false,
		`{}`:                                                                                             false,
	} {
		var v any
		if err := json.Unmarshal([]byte(args), &v); err != nil {
			t.Fatal(err)
		}
		if got := namesSealed(v); got != want {
			t.Errorf("%s: %v, want %v", args, got, want)
		}
	}
}

func testRelease(now time.Time, edit func(map[string]any)) json.RawMessage {
	r := map[string]any{"chrome_id": "chr_workmac0001", "op": "op_fill.3", "origin": "https://shop.example",
		"nonce": base64.RawURLEncoding.EncodeToString([]byte("sixteen-byte-nonce")), "exp": now.Add(60 * time.Second).UnixMilli(), "sig": "MEUCIQ"}
	if edit != nil {
		edit(r)
	}
	b, _ := json.Marshal(r)
	return b
}

// A release is refused on sight when the extension would refuse it: a missing
// field, an origin that isn't https, a short nonce, an exp past or more than
// 2 minutes ahead, or a field it doesn't take.
func TestAReleaseIsCheckedAsTheExtensionWouldOnSight(t *testing.T) {
	now := time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)
	if why := releaseRefusal(testRelease(now, nil), now); why != "" {
		t.Fatalf("a good release: %s", why)
	}
	for name, edit := range map[string]func(map[string]any){
		"no chrome":   func(r map[string]any) { delete(r, "chrome_id") },
		"no op":       func(r map[string]any) { r["op"] = "" },
		"no sig":      func(r map[string]any) { delete(r, "sig") },
		"http":        func(r map[string]any) { r["origin"] = "http://shop.example" },
		"short nonce": func(r map[string]any) { r["nonce"] = base64.RawURLEncoding.EncodeToString([]byte("short")) },
		"padded":      func(r map[string]any) { r["nonce"] = base64.URLEncoding.EncodeToString([]byte("sixteen-byte-nonce!")) },
		"past":        func(r map[string]any) { r["exp"] = now.Add(-time.Second).UnixMilli() },
		"far ahead":   func(r map[string]any) { r["exp"] = now.Add(3 * time.Minute).UnixMilli() },
		"extra":       func(r map[string]any) { r["value"] = "hunter2" },
		"two lines":   func(r map[string]any) { r["origin"] = "https://shop.example\nhttps://evil.example" },
	} {
		if releaseRefusal(testRelease(now, edit), now) == "" {
			t.Errorf("%s: taken", name)
		}
	}
	for _, none := range []string{"", "null"} {
		if releaseRefusal(json.RawMessage(none), now) == "" {
			t.Errorf("%q: taken", none)
		}
	}
}

// sealedRig is a draft rig whose gateway has the held calls' arguments: one
// sealed fill, one ordinary call.
func sealedRig(t *testing.T, args *outcomeAndArgs) (*draftRig, string, string) {
	t.Helper()
	r := newDraftRig(t, WithMandatesToolOutcomes(args))
	r.startRae()
	held := func(proposal string, cents int64) string {
		t.Helper()
		d := r.authorize("rae", proposal, cents)
		if d.Outcome != mandate.OutcomeOwnerRequired || d.ExceptionID == "" {
			t.Fatalf("%s not held: %+v", proposal, d)
		}
		e, err := r.store.Exception(t.Context(), "t1", d.ExceptionID)
		if err != nil || e.Request.Domain != gatewayDomain || e.ProposalID != proposal {
			t.Fatalf("%s isn't a held gateway call: %+v %v", proposal, e, err)
		}
		return d.ExceptionID
	}
	return r, held("p-sealed", 32000), held("p-plain", 32000)
}

func sealedArgs() *outcomeAndArgs {
	return &outcomeAndArgs{args: map[string]json.RawMessage{
		"p-sealed": json.RawMessage(`{"url":"https://shop.example/","read":"r1","steps":[{"fill":"f1","sealed":"sec_a1:password"}]}`),
		"p-plain":  json.RawMessage(`{"amount_cents":32000}`),
	}}
}

// The precondition for turning sealed fills on: KIFF takes an allow of a
// sealed fill only as approve-once with the phone's release, from any client.
// Without one (an older app, a desktop), the hold stays held and nothing is
// recorded. No lasting answer lets one go: not Extra room, not "Allow this
// from now on". A refusal is always taken. Every other hold is answered as
// before.
func TestASealedFillIsAllowedOnlyOnceWithThePhonesRelease(t *testing.T) {
	answer := func(r *draftRig, id, verb string, body any) (int, map[string]any) {
		t.Helper()
		rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/card-exceptions/"+id+"/"+verb, body)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	status := func(r *draftRig, id string) mandate.ExceptionStatus {
		t.Helper()
		e, err := r.store.Exception(t.Context(), "t1", id)
		if err != nil {
			t.Fatal(err)
		}
		return e.Status
	}

	r, sealed, plain := sealedRig(t, sealedArgs())
	// An older client: no release.
	for _, body := range []any{nil, map[string]any{}, map[string]any{"release": nil}} {
		if code, out := answer(r, sealed, "approve-once", body); code != http.StatusBadRequest || out["error"] != "release_required" {
			t.Fatalf("approve-once without a release (%v): %d %v", body, code, out)
		}
	}
	// A release the extension would refuse on sight.
	late := testRelease(r.now, func(m map[string]any) { m["exp"] = r.now.Add(-time.Minute).UnixMilli() })
	if code, out := answer(r, sealed, "approve-once", map[string]any{"release": late}); code != http.StatusBadRequest || out["error"] != "release_required" ||
		!strings.Contains(out["detail"].(string), "clock") {
		t.Fatalf("a late release: %d %v", code, out)
	}
	// Extra room, and "Allow this from now on": never, with or without a release.
	if code, out := answer(r, sealed, "change-card", map[string]any{"release": testRelease(r.now, nil)}); code != http.StatusConflict || out["error"] != "sealed_fill_once" {
		t.Fatalf("change-card: %d %v", code, out)
	}
	if code, out := answer(r, sealed, "allow-draft", map[string]any{}); code != http.StatusOK || out["none"] != mandate.AllowNoneNoChange {
		t.Fatalf("allow-draft: %d %v", code, out)
	}
	if s := status(r, sealed); s != mandate.ExceptionHeld {
		t.Fatalf("refused allows changed the hold: %s", s)
	}
	// The ordinary hold beside it is answered as before, with no release.
	if code, out := answer(r, plain, "approve-once", map[string]any{}); code != http.StatusOK {
		t.Fatalf("an ordinary hold: %d %v", code, out)
	}
	// With the phone's release, once.
	if code, out := answer(r, sealed, "approve-once", map[string]any{"release": testRelease(r.now, nil)}); code != http.StatusOK {
		t.Fatalf("with its release: %d %v", code, out)
	}
	if s := status(r, sealed); s != mandate.ExceptionApproved {
		t.Fatalf("after the allow: %s", s)
	}

	// Refused, a sealed fill needs nothing.
	r2, sealed2, _ := sealedRig(t, sealedArgs())
	if code, out := answer(r2, sealed2, "reject", map[string]any{}); code != http.StatusOK {
		t.Fatalf("reject: %d %v", code, out)
	}
}

// A held call whose arguments can't be read is never allowed on a guess:
// the answer is refused, to be tried again, and the hold stays held. A
// refusal still goes.
func TestAnAllowIsRefusedWhenTheHeldCallCantBeRead(t *testing.T) {
	args := sealedArgs()
	r, sealed, plain := sealedRig(t, args)
	args.err = errors.New("gateway record unavailable")
	for _, id := range []string{sealed, plain} {
		rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/card-exceptions/"+id+"/approve-once", map[string]any{"release": testRelease(r.now, nil)})
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: %d %s", id, rr.Code, rr.Body.String())
		}
		if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/card-exceptions/"+id+"/allow-draft", map[string]any{}); rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("allow-draft %s: %d %s", id, rr.Code, rr.Body.String())
		}
	}
	if e, _ := r.store.Exception(t.Context(), "t1", sealed); e.Status != mandate.ExceptionHeld {
		t.Fatalf("the hold moved: %s", e.Status)
	}
	if rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/card-exceptions/"+sealed+"/reject", map[string]any{}); rr.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rr.Code, rr.Body.String())
	}
}

// An external approver may refuse or defer a sealed fill, never approve it:
// it's the owner's to allow, with the phone's release.
func TestAnExternalApproverNeverApprovesASealedFill(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	s := mandate.NewMemoryStore()
	s.SetApproverPolicy(func(context.Context, string, string) error { return nil })
	m := mandate.Mandate{ID: "card", TenantID: "t1", Subject: "worker", Domain: gatewayDomain, OnExceed: mandate.OnExceedOwner, ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour),
		Grants: []mandate.Grant{{Action: "browse_act", PerAction: []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 50}}}}}
	if err := s.Issue(ctx, m, "alice"); err != nil {
		t.Fatal(err)
	}
	args := &outcomeAndArgs{args: map[string]json.RawMessage{
		"op-sealed": json.RawMessage(`{"url":"https://shop.example/","steps":[{"fill":"f1","sealed":"sec_a1:password"}]}`),
	}}
	h := NewMandatesHandler(s, nil, WithMandatesClock(func() time.Time { return now }), WithMandatesToolOutcomes(args))
	mux := http.NewServeMux()
	h.Register(mux)
	h.RegisterExternalApprover(mux)
	admin := owner()
	admin.Method = auth.MethodSession
	admin.MemberRole = membership.RoleAdmin
	rr := do(t, mux, admin, "POST", "/v1/me/approver-grants", map[string]any{"name": "night-shift", "card_id": "card", "worker": "worker", "action": "browse_act", "parameter": "amount", "max": 100, "expires_at": now.Add(time.Hour)})
	if rr.Code != 201 {
		t.Fatalf("create %d %s", rr.Code, rr.Body)
	}
	var created struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, func() time.Time { return now })
	d, err := a.Authorize(ctx, "t1", "op-sealed", mandate.Request{Subject: "worker", Domain: gatewayDomain, Action: "browse_act", EntityID: "page", Parameters: map[string]int64{"amount": 75}})
	if err != nil || d.ExceptionID == "" {
		t.Fatalf("not held: %+v %v", d, err)
	}
	send := func(method, path string, body any) *httptest.ResponseRecorder {
		var b bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&b).Encode(body)
		}
		req := httptest.NewRequest(method, path, &b)
		req.Header.Set("Authorization", "Bearer "+created.Credential)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}
	path := "/v1/approver/holds/" + d.ExceptionID
	var view approverView
	if rr := send("GET", path, nil); rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &view) != nil || view.ReviewToken == "" {
		t.Fatalf("read %d %s", rr.Code, rr.Body)
	}
	answer := map[string]any{"review_token": view.ReviewToken, "decision": "approve", "reason_code": "fixture"}
	if rr := send("POST", path+"/answer", answer); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "sealed_fill_once") {
		t.Fatalf("an approver's approve: %d %s", rr.Code, rr.Body)
	}
	if e, _ := s.Exception(ctx, "t1", d.ExceptionID); e.Status != mandate.ExceptionHeld {
		t.Fatalf("the hold moved: %s", e.Status)
	}
	answer["decision"] = "reject"
	if rr := send("POST", path+"/answer", answer); rr.Code != 200 {
		t.Fatalf("an approver's refusal: %d %s", rr.Code, rr.Body)
	}
}
