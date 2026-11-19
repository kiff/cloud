package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// A phone sets the account's time zone only as a signed setup call
// (kiff/tafs#96): unsigned it's refused, signed it reaches the handler, and
// a captured request replays to nothing.
func TestPhoneSetsTheTimeZoneOnlySigned(t *testing.T) {
	reached := 0
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/me/time-zone", func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	})
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	p := newPhoneRig(t)
	phone := p.on(owner())
	path, body := "/v1/me/time-zone", `{"time_zone":"Europe/Paris"}`

	if rr := serve(policed, phone, http.MethodPut, path, body, nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "signature_required") {
		t.Fatalf("unsigned: %d %s", rr.Code, rr.Body.String())
	}
	sig := map[string]string{auth.DeviceSignatureHeader: p.signedRequest("t1", http.MethodPut, path, body, time.Now())}
	if rr := serve(policed, phone, http.MethodPut, path, body, sig); rr.Code != http.StatusOK || reached != 1 {
		t.Fatalf("signed: %d %s (reached %d)", rr.Code, rr.Body.String(), reached)
	}
	if rr := serve(policed, phone, http.MethodPut, path, body, sig); rr.Code != http.StatusConflict || reached != 1 {
		t.Fatalf("replayed: %d %s (reached %d)", rr.Code, rr.Body.String(), reached)
	}
}
