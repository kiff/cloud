package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// claimWith polls POST /v1/pair/claim like claim, with the body's extra fields (credential_proof).
func (r *pairRig) claimWith(nonce string, pub []byte, key *ecdsa.PrivateKey, extra map[string]any) (int, map[string]any) {
	digest := sha256.Sum256(auth.PairClaimMessage(nonce, pub))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	body := map[string]any{"nonce": nonce, "pub": b64url(pub), "proof": base64.StdEncoding.EncodeToString(sig)}
	for k, v := range extra {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	code, out := send(r.pub, nil, http.MethodPost, "/v1/pair/claim", string(b), "")
	r.now = r.now.Add(3 * time.Second)
	return code, out
}

const made = 0x01 | 0x04 | 0x40 // UP, UV, AT

// proofOf is the claim's credential_proof for h's creation over message.
func proofOf(h windowsHello, message []byte, client map[string]any, flags byte) map[string]any {
	cd, ao := h.creationFor(message, client, flags)
	return map[string]any{"credential_proof": map[string]string{"client_data_json": cd, "attestation_object": ao}}
}

// §5.1: a passkey computer's claim proves its own credential was made for this
// pairing, with the same checks as a registration, and the credential and key the phone approved. The QR is unchanged.
func TestAPasskeyComputersClaimProvesItsCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logs := &bytes.Buffer{}
	passkeys := auth.NewMemoryPasskeyStore()
	r := newPairRig(t, handlers.WithDevicePasskeys(passkeys), handlers.WithDeviceLogger(slog.New(slog.NewTextHandler(logs, nil))))
	hello := newWindowsHello(t)
	q := passkeyQR(r, hello)
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("the phone's approval: %d %v", code, out)
	}
	nonce, pub := q["nonce"].(string), b64url(r.pcDER)
	right := auth.PairingCredentialMessage(nonce, pub)
	other := newWindowsHello(t)
	sameIDOtherKey, deviceKey, otherID := other, hello, hello
	sameIDOtherKey.credential = hello.credential
	deviceKey.key = r.pcKey
	otherID.credential = bytes.Repeat([]byte{9}, 32)
	for name, c := range map[string]struct {
		extra  map[string]any
		reason string
	}{
		// 4. The challenge: another nonce, another pub, the registration's message; framed, a get, no UV.
		"another nonce":        {proofOf(hello, auth.PairingCredentialMessage(b64url(bytes.Repeat([]byte{1}, 32)), pub), nil, made), "challenge"},
		"another pub":          {proofOf(hello, auth.PairingCredentialMessage(nonce, b64url(bytes.Repeat([]byte{2}, 65))), nil, made), "challenge"},
		"the registration's":   {proofOf(hello, auth.RegistrationMessage("tenant-1", "user-abc", r.now.Format(time.RFC3339)), nil, made), "challenge"},
		"framed":               {proofOf(hello, right, map[string]any{"crossOrigin": true}, made), "origin"},
		"a get":                {proofOf(hello, right, map[string]any{"type": "webauthn.get"}, made), "type"},
		"no user verification": {proofOf(hello, right, nil, 0x01|0x40), "user_verification"},
		// 2. The key: another P-256 key under the approved id, or the device key itself.
		"another key":    {proofOf(sameIDOtherKey, right, nil, made), "key"},
		"the device key": {proofOf(deviceKey, right, nil, made), "key"},
		// 3. The credential id the QR named.
		"another credential id": {proofOf(otherID, right, nil, made), "credential"},
		// 1. One field alone is never taken as no proof.
		"only client_data_json": {map[string]any{"credential_proof": map[string]string{"client_data_json": "e30"}}, "malformed"},
		"not base64url":         {map[string]any{"credential_proof": map[string]string{"client_data_json": "%%", "attestation_object": "AA"}}, "malformed"},
	} {
		code, out := r.claimWith(nonce, r.pcDER, r.pcKey, c.extra)
		if code != http.StatusBadRequest || out["error"] != "credential_proof_invalid" || out["reason"] != c.reason {
			t.Errorf("%s: %d %v, want reason %s", name, code, out, c.reason)
		}
	}
	// None of those paired anything: the pairing is still there to claim.
	if list, err := r.devices.ListActive(ctx, "tenant-1", "user-abc"); err != nil || len(list) != 1 {
		t.Fatalf("a refused proof made a device: %v %v", list, err)
	}
	// 7. A refusal is logged by its reason, never its bodies.
	if !strings.Contains(logs.String(), "credential proof refused") || strings.Contains(logs.String(), "webauthn") || strings.Contains(logs.String(), "client_data") {
		t.Fatalf("the log: %s", logs.String())
	}
	// The right proof pairs it, as a passkey computer.
	code, out := r.claimWith(nonce, r.pcDER, r.pcKey, proofOf(hello, right, nil, made))
	if code != http.StatusOK || out["approval_mode"] != auth.ApprovalModePasskey {
		t.Fatalf("the proven claim: %d %v", code, out)
	}
	if key, err := passkeys.Find(ctx, "tenant-1", hello.credential); err != nil || !bytes.Equal(key.PublicKey, hello.der) {
		t.Fatalf("its passkey: %+v %v", key, err)
	}

	// 5. The same proof, for another pairing of the same credential: its nonce differs, so it's refused.
	r2 := newPairRig(t, handlers.WithDevicePasskeys(auth.NewMemoryPasskeyStore()))
	r2.pcKey, r2.pcDER = r.pcKey, r.pcDER
	q2 := passkeyQR(r2, hello)
	if code, out := r2.approve(q2); code != http.StatusCreated {
		t.Fatalf("the second pairing: %d %v", code, out)
	}
	if code, out := r2.claimWith(q2["nonce"].(string), r2.pcDER, r2.pcKey, proofOf(hello, right, nil, made)); code != http.StatusBadRequest || out["reason"] != "challenge" {
		t.Fatalf("the proof reused for another pairing: %d %v", code, out)
	}
}

// 6, 1 and 8: a proof only comes with a credential of the pairing's own; with none at all, a claim is taken until
// PairingProofRequiredFrom, and refused from then on.
func TestAPairingsCredentialProofOnlyWhereItHasACredential(t *testing.T) {
	t.Parallel()
	hello := newWindowsHello(t)
	// A hello computer, a credential-less passkey one and a software-keyed one have no credential: a proof is refused.
	for name, qr := range map[string]func(r *pairRig) map[string]any{
		"hello": func(r *pairRig) map[string]any { return r.qr() },
		"passkey with no credential": func(r *pairRig) map[string]any {
			q := r.qr()
			delete(q, "approval_pub")
			q["kind"] = "passkey"
			return q
		},
		"software": func(r *pairRig) map[string]any { return viewQR(r, "passkey") },
	} {
		r := newPairRig(t, handlers.WithDevicePasskeys(auth.NewMemoryPasskeyStore()))
		q := qr(r)
		if code, out := r.approve(q); code != http.StatusCreated {
			t.Fatalf("%s: approval %d %v", name, code, out)
		}
		proof := proofOf(hello, auth.PairingCredentialMessage(q["nonce"].(string), b64url(r.pcDER)), nil, made)
		if code, out := r.claimWith(q["nonce"].(string), r.pcDER, r.pcKey, proof); code != http.StatusBadRequest || out["reason"] != "no_credential" {
			t.Errorf("%s: %d %v", name, code, out)
		}
		// Without one, it pairs as before.
		if code, out := r.claimWith(q["nonce"].(string), r.pcDER, r.pcKey, nil); code != http.StatusOK {
			t.Errorf("%s with no proof: %d %v", name, code, out)
		}
	}
	// A passkey computer with its credential and no proof: taken before the date, refused from it.
	for _, c := range []struct {
		at   time.Time
		code int
	}{{handlers.PairingProofRequiredFrom.Add(-time.Hour), http.StatusOK}, {handlers.PairingProofRequiredFrom, http.StatusBadRequest}} {
		r := newPairRig(t, handlers.WithDevicePasskeys(auth.NewMemoryPasskeyStore()))
		r.now = c.at
		q := passkeyQR(r, hello)
		if code, out := r.approve(q); code != http.StatusCreated {
			t.Fatalf("approval at %v: %d %v", c.at, code, out)
		}
		code, out := r.claimWith(q["nonce"].(string), r.pcDER, r.pcKey, nil)
		if code != c.code || (code == http.StatusBadRequest && out["reason"] != "required") {
			t.Errorf("no proof at %v: %d %v, want %d", c.at, code, out, c.code)
		}
	}
}

// 8: the one release without the proof ends on a date. When it has passed, this fails until the proof is required
// for good (the switch removed, and the claim with no proof refused always).
func TestThePairingProofSwitchIsStillAhead(t *testing.T) {
	if !time.Now().Before(handlers.PairingProofRequiredFrom) {
		t.Fatalf("PairingProofRequiredFrom (%s) has passed: require credential_proof for good and remove the switch", handlers.PairingProofRequiredFrom.Format(time.DateOnly))
	}
}
