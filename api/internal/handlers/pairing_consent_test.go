package handlers_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/appsignin"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// A computer that signs in by pairing records the terms its welcome showed
// ticked, in the same step as its session, as the app's sheet does: an old
// version is refused before anything is claimed; a consent that can't be
// recorded leaves no session and the pairing claimable; none sent, as before.
func TestAPairingClaimRecordsTheTermsTicked(t *testing.T) {
	t.Parallel()
	consents := &appsignin.MemoryConsents{}
	app := appsignin.New(false, "https://app.kiff.test/sign-in/app")
	r := newPairRig(t, handlers.WithDeviceAppSignIn(app, consents))
	q := r.qr()
	nonce := q["nonce"].(string)
	q["exp"] = r.now.Add(4 * time.Minute).Format(time.RFC3339)
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("approval: %d %v", code, out)
	}
	r.now = r.now.Add(10 * time.Second)
	if code, out := r.claimWith(nonce, r.pcDER, r.pcKey, map[string]any{"terms_version": "2026-01-01"}); code != http.StatusConflict ||
		out["error"] != "terms_outdated" || out["terms_version"] != appsignin.DefaultTermsVersion {
		t.Fatalf("old terms: %d %v", code, out)
	}
	consents.Fail = errors.New("database down")
	if code, out := r.claimWith(nonce, r.pcDER, r.pcKey, map[string]any{"terms_version": appsignin.DefaultTermsVersion}); code != http.StatusInternalServerError {
		t.Fatalf("consent not recorded: %d %v", code, out)
	}
	// The rig's phone is a faked identity with no stored session, so the
	// computer's would be the only one: none is left.
	if live, _ := r.sessions.ListActiveForSubject(t.Context(), r.phone.Subject, r.now); len(live) != 0 {
		t.Fatalf("a session stayed after a consent that wasn't recorded: %d", len(live))
	}
	consents.Fail = nil
	code, out := r.claimWith(nonce, r.pcDER, r.pcKey, map[string]any{"terms_version": appsignin.DefaultTermsVersion})
	if code != http.StatusOK || out["access_token"] == nil {
		t.Fatalf("claim: %d %v", code, out)
	}
	got := consents.All()
	if len(got) != 1 || got[0].Client != appsignin.PairClientID || got[0].TermsVersion != appsignin.DefaultTermsVersion ||
		got[0].DeviceID != out["device_id"] || got[0].Subject != r.phone.Subject || got[0].SessionID == "" {
		t.Fatalf("consent: %+v (device %v)", got, out["device_id"])
	}
	parsed, _ := auth.ParseDevSessionToken(out["access_token"].(string))
	if s, err := r.sessions.GetByHash(t.Context(), parsed.Hash, r.now); err != nil || s.ID != got[0].SessionID {
		t.Fatalf("the consent's session: %+v %v", s, err)
	}
}

// Without terms_version, a claim is as it was: no consent recorded.
func TestAPairingClaimWithoutTermsRecordsNothing(t *testing.T) {
	t.Parallel()
	consents := &appsignin.MemoryConsents{}
	r := newPairRig(t, handlers.WithDeviceAppSignIn(appsignin.New(false, "https://app.kiff.test/sign-in/app"), consents))
	q := r.qr()
	q["exp"] = r.now.Add(4 * time.Minute).Format(time.RFC3339)
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("approval: %d %v", code, out)
	}
	r.now = r.now.Add(10 * time.Second)
	if code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey); code != http.StatusOK {
		t.Fatalf("claim: %d %v", code, out)
	}
	if len(consents.All()) != 0 {
		t.Fatalf("consents: %v", consents.All())
	}
}
