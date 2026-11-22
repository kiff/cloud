package handlers_test

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// One fingerprint pairs a Chrome (the tafs Chrome contract's "Pairing"): a
// phone whose key signs once per prompt signs the extension's view-only
// device here (POST /v1/me/pairings) and planque's confirm of the Chrome
// together, under one kiff.requests.v1 signature by its approval key. KIFF
// checks its own request with planque's hash as given; a changed body, the
// device key's combined signature or a request paired with itself pairs
// nothing, and the one signature is taken once.
func TestOneFingerprintPairsTheExtensionBesidePlanquesConfirm(t *testing.T) {
	t.Parallel()
	r := newPairRig(t)
	at := r.now.Format(time.RFC3339)
	b, _ := json.Marshal(r.qr())
	path := "/v1/me/pairings"
	hashOf := func(m []byte) string { s := sha256.Sum256(m); return hex.EncodeToString(s[:]) }
	own := hashOf(auth.RequestMessage("tenant-1", http.MethodPost, path, b, at))
	// planque's request beside it, as planque rebuilds it (planque.request.v1).
	confirm := []byte(`{"chrome_key_sha256":"…","name":"Work Mac","phone_key":"…","phone_proof":"…","phone_sig":"…"}`)
	confirmSum := sha256.Sum256(confirm)
	planque := hashOf([]byte(strings.Join([]string{"planque.request.v1", "https://planque.example", "tenant-1", http.MethodPost,
		"/v1/accounts/tenant-1/chromes/pairings/pair_1/confirm", hex.EncodeToString(confirmSum[:]), at}, "\n")))
	together := func(k *ecdsa.PrivateKey, one, other string) string {
		digest := sha256.Sum256(auth.RequestsMessage("tenant-1", at, one, other))
		sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		return "t=" + at + ",v3=" + base64.StdEncoding.EncodeToString(sig) + ",with=sha256:" + other
	}

	// The device key's combined signature: refused, a window's tap never makes one.
	if code, out := send(r.authed, &r.phone, http.MethodPost, path, string(b), together(r.device, own, planque)); code != http.StatusForbidden {
		t.Fatalf("the device key's combined signature: %d %v", code, out)
	}
	// Signed over another body than the one sent: refused.
	other, _ := json.Marshal(r.qr())
	if code, out := send(r.authed, &r.phone, http.MethodPost, path, string(other), together(r.approval, own, planque)); code != http.StatusForbidden {
		t.Fatalf("a body other than the one signed: %d %v", code, out)
	}
	// Paired with itself: refused.
	if code, out := send(r.authed, &r.phone, http.MethodPost, path, string(b), together(r.approval, own, own)); code != http.StatusForbidden && code != http.StatusUnauthorized {
		t.Fatalf("a request paired with itself: %d %v", code, out)
	}
	// The approval key's, beside planque's confirm: the extension is paired,
	// once.
	header := together(r.approval, own, planque)
	if code, out := send(r.authed, &r.phone, http.MethodPost, path, string(b), header); code != http.StatusCreated {
		t.Fatalf("the one fingerprint's pairing: %d %v", code, out)
	}
	if code, out := send(r.authed, &r.phone, http.MethodPost, path, string(b), header); code != http.StatusConflict || out["error"] != "signature_replayed" {
		t.Fatalf("the same signed request again: %d %v", code, out)
	}
}
