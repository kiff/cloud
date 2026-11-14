package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

var testRP = RelyingParty{ID: "kiff.dev", Origins: []string{"https://kiff.dev"}}

// ceremony is one navigator.credentials.get() as a browser and an
// authenticator make it, each part changeable to test one check at a time.
type ceremony struct {
	typ, origin, rpID string
	crossOrigin       bool
	challenge         []byte
	flags             byte
	count             uint32
	key               *ecdsa.PrivateKey
	credential        []byte
}

func newCeremony(t *testing.T, message []byte) (ceremony, Passkey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	digest := sha256.Sum256(message)
	c := ceremony{typ: "webauthn.get", origin: "https://kiff.dev", rpID: "kiff.dev", challenge: digest[:], flags: flagUserPresent | flagUserVerified,
		count: 7, key: k, credential: []byte("cred-1")}
	return c, Passkey{ID: []byte("cred-1"), Tenant: "t_acme", PublicKey: der, SignCount: 6}
}

func (c ceremony) assert(t *testing.T) Assertion {
	t.Helper()
	client, _ := json.Marshal(map[string]any{"type": c.typ, "challenge": base64.RawURLEncoding.EncodeToString(c.challenge), "origin": c.origin, "crossOrigin": c.crossOrigin, "topOrigin": "https://evil.example"})
	rp := sha256.Sum256([]byte(c.rpID))
	auth := append(append([]byte(nil), rp[:]...), c.flags)
	auth = binary.BigEndian.AppendUint32(auth, c.count)
	ch := sha256.Sum256(client)
	signed := sha256.Sum256(append(append([]byte(nil), auth...), ch[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, c.key, signed[:])
	if err != nil {
		t.Fatal(err)
	}
	e := base64.RawURLEncoding.EncodeToString
	return Assertion{CredentialID: e(c.credential), ClientDataJSON: e(client), AuthenticatorData: e(auth), Signature: e(sig)}
}

func TestAPasskeyApprovesExactlyTheActionItSigned(t *testing.T) {
	now := time.Date(2026, 11, 14, 18, 0, 0, 0, time.UTC)
	at := now.Format(time.RFC3339)
	body := sha256.Sum256([]byte(`{"agent":"refunds"}`))
	bodyHex := hex.EncodeToString(body[:])
	message := requestMessage("t_acme", "POST", "/v1/agent-drafts/dr_1/confirm", bodyHex, at)
	c, key := newCeremony(t, message)

	ok, used, err := VerifyRequestAssertion(testRP, key, "t_acme", "POST", "/v1/agent-drafts/dr_1/confirm", bodyHex, at, c.assert(t), now)
	if err != nil || ok.SignCount != 7 || ok.Synced || len(used) != 32 || string(ok.Used) != string(used) {
		t.Fatalf("a good assertion: %+v, used %x, %v", ok, used, err)
	}
	// A synced passkey (BE and BS) still approves, and says so: it's never the second device.
	synced := c
	synced.flags |= 0x08 | flagBackedUp
	if ok, _, err := VerifyAssertion(testRP, key, "t_acme", synced.assert(t), message); err != nil || !ok.Synced {
		t.Fatalf("a synced passkey: %+v, %v", ok, err)
	}
	// The same approval twice has the same replay identity, for the caller's used set.
	if _, again, _ := VerifyRequestAssertion(testRP, Passkey{ID: key.ID, Tenant: key.Tenant, PublicKey: key.PublicKey, SignCount: 6}, "t_acme", "POST", "/v1/agent-drafts/dr_1/confirm", bodyHex, at, c.assert(t), now); string(again) != string(used) {
		t.Fatal("the same action's replay identity changed")
	}

	other := func(change func(*ceremony, *Passkey)) error {
		c2, k2 := c, key
		change(&c2, &k2)
		_, _, err := VerifyAssertion(testRP, k2, "t_acme", c2.assert(t), message)
		return err
	}
	stranger, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherDigest := sha256.Sum256(requestMessage("t_acme", "POST", "/v1/agent-drafts/dr_2/confirm", bodyHex, at))
	for _, n := range []struct {
		name   string
		change func(*ceremony, *Passkey)
		want   error
	}{
		{"another account's passkey", func(_ *ceremony, k *Passkey) { k.Tenant = "t_other" }, ErrPasskeyAccount},
		{"another credential", func(c *ceremony, _ *Passkey) { c.credential = []byte("cred-2") }, ErrPasskeyAccount},
		{"a registration, not a get", func(c *ceremony, _ *Passkey) { c.typ = "webauthn.create" }, ErrAssertionType},
		{"another origin", func(c *ceremony, _ *Passkey) { c.origin = "https://kiff.dev.example.com" }, ErrAssertionOrigin},
		{"another RP ID", func(c *ceremony, _ *Passkey) { c.rpID = "example.com" }, ErrAssertionOrigin},
		{"kiff.dev framed in another site", func(c *ceremony, _ *Passkey) { c.crossOrigin = true }, ErrAssertionOrigin},
		{"another action's digest", func(c *ceremony, _ *Passkey) { c.challenge = otherDigest[:] }, ErrAssertionAction},
		{"the digest cut short", func(c *ceremony, _ *Passkey) { c.challenge = c.challenge[:31] }, ErrAssertionAction},
		{"present but not verified", func(c *ceremony, _ *Passkey) { c.flags = flagUserPresent }, ErrAssertionUser},
		{"verified but not present", func(c *ceremony, _ *Passkey) { c.flags = flagUserVerified }, ErrAssertionUser},
		{"signed by another key", func(c *ceremony, _ *Passkey) { c.key = stranger }, ErrAssertionSigned},
		{"a counter that went backwards", func(c *ceremony, _ *Passkey) { c.count = 5 }, ErrAssertionCounter},
		{"a counter that stood still", func(c *ceremony, _ *Passkey) { c.count = 6 }, ErrAssertionCounter},
		{"a counter reset to 0", func(c *ceremony, _ *Passkey) { c.count = 0 }, ErrAssertionCounter},
	} {
		if err := other(n.change); !errors.Is(err, n.want) {
			t.Errorf("%s: %v, want %v", n.name, err, n.want)
		}
	}
	// An authenticator that keeps no counter (0 and 0) still approves.
	if err := other(func(c *ceremony, k *Passkey) { c.count, k.SignCount = 0, 0 }); err != nil {
		t.Errorf("no counter on either side: %v", err)
	}
	// A signature over the right data with a tampered clientDataJSON fails as a signature, never as accepted.
	a := c.assert(t)
	a.ClientDataJSON = base64.RawURLEncoding.EncodeToString([]byte(`{"type":"webauthn.get","challenge":"` + base64.RawURLEncoding.EncodeToString(c.challenge) + `","origin":"https://kiff.dev","x":1}`))
	if _, _, err := VerifyAssertion(testRP, key, "t_acme", a, message); !errors.Is(err, ErrAssertionSigned) {
		t.Errorf("clientDataJSON changed after signing: %v", err)
	}
	if _, _, err := VerifyAssertion(testRP, key, "t_acme", Assertion{CredentialID: base64.RawURLEncoding.EncodeToString(key.ID), ClientDataJSON: "!", AuthenticatorData: "", Signature: ""}, message); !errors.Is(err, ErrAssertionShape) {
		t.Errorf("a malformed assertion: %v", err)
	}
	if _, _, err := VerifyRequestAssertion(testRP, key, "t_acme", "POST", "/v1/agent-drafts/dr_1/confirm", bodyHex, at, c.assert(t), now.Add(6*time.Minute)); !errors.Is(err, ErrAnswerSignedAt) {
		t.Errorf("signed 6 minutes ago: %v", err)
	}
}
