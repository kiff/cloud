package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// A device's sign-in without a device key (the CLI's, or a device
// with no secure hardware) doesn't answer a held call, whichever answer;
// the dashboard's answer is unchanged.
func TestADeviceSignInWithoutAKeyDoesNotAnswer(t *testing.T) {
	f := newHoldFixture(t)
	policed := auth.DefaultRoutePolicy().Wrap(f.mux)
	cli := person("alice", membership.RoleAdmin)
	cli.Method = auth.MethodDeveloper
	id := f.hold("agent://a")
	for _, verb := range []string{"approve-once", "reject", "change-card"} {
		rr := serve(policed, cli, http.MethodPost, "/v1/me/card-exceptions/"+id+"/"+verb, `{}`, nil)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "device_required") {
			t.Errorf("%s from a keyless device sign-in: %d %s", verb, rr.Code, rr.Body.String())
		}
	}
	if got, _ := f.store.Exception(context.Background(), "t1", id); got.Status != mandate.ExceptionHeld {
		t.Fatalf("a refused answer changed the hold: %s", got.Status)
	}
	// The dashboard (a browser session) answers as before.
	if rr := serve(policed, person("alice", membership.RoleAdmin), http.MethodPost, "/v1/me/card-exceptions/"+id+"/approve-once", `{}`, nil); rr.Code != http.StatusOK {
		t.Fatalf("the dashboard's answer: %d %s", rr.Code, rr.Body.String())
	}
}

// The receipt's proof names the approval device, not the session it
// signed in with, so it stays the same across sign-ins.
func TestAnAnswersProofNamesTheDevice(t *testing.T) {
	f := newHoldFixture(t)
	policed := auth.DefaultRoutePolicy().Wrap(f.mux)
	p := newPhoneRig(t)
	phone := p.on(person("alice", membership.RoleAdmin))
	phone.DeviceID = "dev_abcdefghijklmnopqrstuvwxyz"
	id := f.hold("agent://a")
	held, err := f.store.Exception(context.Background(), "t1", id)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.Answer{TenantID: "t1", ExceptionID: id, Answer: "approve-once", AccountingVersion: held.AccountingVersion,
		SignedAt: time.Date(2026, 9, 12, 11, 59, 30, 0, time.UTC).Format(time.RFC3339)}
	b, _ := json.Marshal(map[string]any{"signature": map[string]string{"signed_at": a.SignedAt, "value": p.sign(a.Message())}})
	if rr := serve(policed, phone, http.MethodPost, "/v1/me/card-exceptions/"+id+"/approve-once", string(b), nil); rr.Code != http.StatusOK {
		t.Fatalf("signed answer: %d %s", rr.Code, rr.Body.String())
	}
	got, _ := f.members.HoldAnswers(context.Background(), "t1", []string{id})
	if d := got[id].Device; d == nil || d.KeyID != phone.DeviceID || string(d.PublicKey) != string(p.der) {
		t.Fatalf("device proof = %+v; want the device's id and key", got[id].Device)
	}
}
