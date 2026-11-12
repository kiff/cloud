package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// One fingerprint for two requests (kiff/tafs/docs/secure-connection.md):
// the approval key signs kiff.requests.v1 over both request messages' hashes,
// and each service checks its own request, with the other's hash as given.
// Category: authority
// Attack: one signature reused on a third request, a swapped other hash, an old signed_at, a request paired with itself, a device key's signature
// Refused: each, by the request check alone
func TestOneSignatureOverTwoRequests(t *testing.T) {
	t.Parallel()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	at := now.Format(time.RFC3339)
	hash := func(m []byte) string { s := sha256.Sum256(m); return hex.EncodeToString(s[:]) }

	body := []byte(`{"version":"v1"}`)
	confirm := RequestMessage("t1", "POST", "/v1/me/agent-drafts/drf_1/confirm", body, at)
	keyGiven := []byte("planque.request.v1\nhttps://planque.test\nt1\nPOST\n/v1/accounts/t1/grants/g1/key\nabc\n" + at)
	sign := func(m []byte, k *ecdsa.PrivateKey) string {
		d := sha256.Sum256(m)
		sig, _ := ecdsa.SignASN1(rand.Reader, k, d[:])
		return base64.StdEncoding.EncodeToString(sig)
	}
	together := sign(RequestsMessage("t1", at, hash(confirm), hash(keyGiven)), key)
	header := func(sig, with string) string { return "t=" + at + ",v3=" + sig + ",with=sha256:" + with }

	if !SignedTogether(header(together, hash(keyGiven))) || SignedTogether("t="+at+",v1="+together) {
		t.Fatal("SignedTogether")
	}
	id, window, err := VerifyRequestWindow(der, "t1", "POST", "/v1/me/agent-drafts/drf_1/confirm", body, header(together, hash(keyGiven)), now)
	if err != nil || window != "" || len(id) == 0 {
		t.Fatalf("the confirm, signed with the key's request: %v", err)
	}
	// The same message alone (a v1) isn't the same use: the replay record is per request.
	single := sign(confirm, key)
	if id1, _, err := VerifyRequestWindow(der, "t1", "POST", "/v1/me/agent-drafts/drf_1/confirm", body, "t="+at+",v1="+single, now); err != nil || bytes.Equal(id1, id) {
		t.Fatalf("v1 beside v3: %v", err)
	}

	// Two requests signed together: each verifies with the other's hash, and
	// each is its own use, so both services can take the one signature.
	confirm2 := RequestMessage("t1", "POST", "/v1/me/agent-drafts/drf_2/confirm", body, at)
	both := sign(RequestsMessage("t1", at, hash(confirm), hash(confirm2)), key)
	idA, _, errA := VerifyRequestWindow(der, "t1", "POST", "/v1/me/agent-drafts/drf_1/confirm", body, header(both, hash(confirm2)), now)
	idB, _, errB := VerifyRequestWindow(der, "t1", "POST", "/v1/me/agent-drafts/drf_2/confirm", body, header(both, hash(confirm)), now)
	if errA != nil || errB != nil || bytes.Equal(idA, idB) {
		t.Fatalf("two requests, one signature: %v %v, same use %v", errA, errB, bytes.Equal(idA, idB))
	}

	refused := func(what, uri string, b []byte, h string, at time.Time) {
		t.Helper()
		if _, _, err := VerifyRequestWindow(der, "t1", "POST", uri, b, h, at); err == nil {
			t.Fatalf("%s: verified", what)
		}
	}
	// On a third request: its own message isn't one of the two.
	refused("a third request", "/v1/me/agent-drafts/drf_2/confirm", body, header(together, hash(keyGiven)), now)
	// Another draft's version in the body.
	refused("another body", "/v1/me/agent-drafts/drf_1/confirm", []byte(`{"version":"v2"}`), header(together, hash(keyGiven)), now)
	// A swapped other hash.
	refused("a swapped other hash", "/v1/me/agent-drafts/drf_1/confirm", body, header(together, hash([]byte("another request"))), now)
	// Paired with itself.
	self := sign(RequestsMessage("t1", at, hash(confirm), hash(confirm)), key)
	refused("a request paired with itself", "/v1/me/agent-drafts/drf_1/confirm", body, header(self, hash(confirm)), now)
	// Outside the 5 minutes.
	if _, _, err := VerifyRequestWindow(der, "t1", "POST", "/v1/me/agent-drafts/drf_1/confirm", body, header(together, hash(keyGiven)), now.Add(6*time.Minute)); !errors.Is(err, ErrAnswerSignedAt) {
		t.Fatalf("an old signed_at: %v", err)
	}
	// Another key's signature.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	refused("another key", "/v1/me/agent-drafts/drf_1/confirm", body, header(sign(RequestsMessage("t1", at, hash(confirm), hash(keyGiven)), other), hash(keyGiven)), now)
	// Malformed: a with without v3, a v3 without with, an upper-case hash.
	for _, h := range []string{"t=" + at + ",v1=" + together + ",with=sha256:" + hash(keyGiven), "t=" + at + ",v3=" + together,
		"t=" + at + ",v3=" + together + ",with=sha256:" + "AB" + hash(keyGiven)[2:]} {
		refused("malformed "+h[:20], "/v1/me/agent-drafts/drf_1/confirm", body, h, now)
	}
}
