package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func testDeviceKey(t *testing.T, curve elliptic.Curve) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return k, base64.StdEncoding.EncodeToString(der)
}

func signAnswer(t *testing.T, k *ecdsa.PrivateKey, a Answer) string {
	t.Helper()
	digest := sha256.Sum256(a.Message())
	sig, err := ecdsa.SignASN1(rand.Reader, k, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// Only a P-256 public key in SPKI DER is a device key.
func TestParseDeviceKey(t *testing.T) {
	_, p256 := testDeviceKey(t, elliptic.P256())
	if der, err := ParseDeviceKey(p256); err != nil || len(der) == 0 {
		t.Fatalf("P-256 refused: %v", err)
	}
	_, p384 := testDeviceKey(t, elliptic.P384())
	rk, _ := rsa.GenerateKey(rand.Reader, 1024)
	rder, _ := x509.MarshalPKIXPublicKey(&rk.PublicKey)
	for name, in := range map[string]string{"P-384": p384, "RSA": base64.StdEncoding.EncodeToString(rder), "not base64": "%%%", "garbage": "aGVsbG8=", "empty": ""} {
		if _, err := ParseDeviceKey(in); !errors.Is(err, ErrDeviceKey) {
			t.Errorf("%s: err = %v, want ErrDeviceKey", name, err)
		}
	}
}

// The signed message is fixed, one field per line, with the note hashed.
func TestAnswerMessage(t *testing.T) {
	a := Answer{TenantID: "t1", ExceptionID: "ce_1", Answer: "reject", NextStep: "return_first", Note: "Ask for photos.", AccountingVersion: 2, SignedAt: "2026-10-14T12:00:00Z"}
	sum := sha256.Sum256([]byte("Ask for photos."))
	want := "kiff.answer.v1\nt1\nce_1\nreject\nreturn_first\n" + base16(sum[:]) + "\n2\n2026-10-14T12:00:00Z"
	if got := string(a.Message()); got != want {
		t.Errorf("message =\n%s\nwant\n%s", got, want)
	}
	if got := string(Answer{TenantID: "t1", ExceptionID: "ce_1", Answer: "approve-once", AccountingVersion: 2, SignedAt: "x"}.Message()); got != "kiff.answer.v1\nt1\nce_1\napprove-once\n\n\n2\nx" {
		t.Errorf("approve message = %q", got)
	}
}

func base16(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexdigits[c>>4], hexdigits[c&15])
	}
	return string(out)
}

// A signature verifies only for the exact answer it was made over, from
// the key it was made with, near the server's time.
func TestVerifyAnswer(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	k, pub := testDeviceKey(t, elliptic.P256())
	der, _ := ParseDeviceKey(pub)
	a := Answer{TenantID: "t1", ExceptionID: "ce_1", Answer: "reject", NextStep: "return_first", Note: "photos", AccountingVersion: 2, SignedAt: now.Add(-time.Minute).Format(time.RFC3339)}
	sig := signAnswer(t, k, a)
	if err := VerifyAnswer(der, a, sig, now); err != nil {
		t.Fatalf("valid signature refused: %v", err)
	}
	for name, changed := range map[string]func(Answer) Answer{
		"tenant":    func(x Answer) Answer { x.TenantID = "t2"; return x },
		"hold":      func(x Answer) Answer { x.ExceptionID = "ce_2"; return x },
		"answer":    func(x Answer) Answer { x.Answer = "approve-once"; return x },
		"next step": func(x Answer) Answer { x.NextStep = ""; return x },
		"note":      func(x Answer) Answer { x.Note = "photos!"; return x },
		"version":   func(x Answer) Answer { x.AccountingVersion = 3; return x },
	} {
		if err := VerifyAnswer(der, changed(a), sig, now); !errors.Is(err, ErrAnswerSignature) {
			t.Errorf("%s changed: err = %v, want ErrAnswerSignature", name, err)
		}
	}
	other, otherPub := testDeviceKey(t, elliptic.P256())
	otherDER, _ := ParseDeviceKey(otherPub)
	if err := VerifyAnswer(otherDER, a, sig, now); !errors.Is(err, ErrAnswerSignature) {
		t.Errorf("another key verified: %v", err)
	}
	if err := VerifyAnswer(der, a, signAnswer(t, other, a), now); !errors.Is(err, ErrAnswerSignature) {
		t.Errorf("another key's signature verified: %v", err)
	}
	if err := VerifyAnswer(der, a, "bm90IGEgc2lnbmF0dXJl", now); !errors.Is(err, ErrAnswerSignature) {
		t.Errorf("garbage signature: %v", err)
	}
	for _, at := range []string{now.Add(-6 * time.Minute).Format(time.RFC3339), now.Add(6 * time.Minute).Format(time.RFC3339), "yesterday"} {
		b := a
		b.SignedAt = at
		if err := VerifyAnswer(der, b, signAnswer(t, k, b), now); !errors.Is(err, ErrAnswerSignedAt) {
			t.Errorf("signed_at %q: err = %v, want ErrAnswerSignedAt", at, err)
		}
	}
	if got := DeviceKeyFingerprint(der); len(got) != 19 {
		t.Errorf("fingerprint %q", got)
	}
}

func signRequest(t *testing.T, k *ecdsa.PrivateKey, tenant, method, uri string, body []byte, at string) string {
	t.Helper()
	digest := sha256.Sum256(RequestMessage(tenant, method, uri, body, at))
	sig, err := ecdsa.SignASN1(rand.Reader, k, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return "t=" + at + ",v1=" + base64.StdEncoding.EncodeToString(sig)
}

// A setup call's signature covers the tenant, method, path and query,
// and the body's hash; it verifies only for that exact request.
func TestVerifyRequest(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	k, pub := testDeviceKey(t, elliptic.P256())
	der, _ := ParseDeviceKey(pub)
	at := now.Add(-time.Minute).Format(time.RFC3339)
	body := []byte(`{"id":"rae"}`)
	header := signRequest(t, k, "t1", "POST", "/v1/me/agents", body, at)
	if _, err := VerifyRequest(der, "t1", "POST", "/v1/me/agents", body, header, now); err != nil {
		t.Fatalf("valid request refused: %v", err)
	}
	for name, try := range map[string]func() error{
		"tenant": func() error {
			_, err := VerifyRequest(der, "t2", "POST", "/v1/me/agents", body, header, now)
			return err
		},
		"method": func() error {
			_, err := VerifyRequest(der, "t1", "PUT", "/v1/me/agents", body, header, now)
			return err
		},
		"path": func() error { _, err := VerifyRequest(der, "t1", "POST", "/v1/keys", body, header, now); return err },
		"body": func() error {
			_, err := VerifyRequest(der, "t1", "POST", "/v1/me/agents", []byte(`{"id":"eve"}`), header, now)
			return err
		},
		"none": func() error { _, err := VerifyRequest(der, "t1", "POST", "/v1/me/agents", body, "", now); return err },
	} {
		if err := try(); !errors.Is(err, ErrRequestSignature) {
			t.Errorf("%s changed: err = %v, want ErrRequestSignature", name, err)
		}
	}
	old := now.Add(-10 * time.Minute).Format(time.RFC3339)
	if _, err := VerifyRequest(der, "t1", "POST", "/v1/me/agents", body, signRequest(t, k, "t1", "POST", "/v1/me/agents", body, old), now); !errors.Is(err, ErrAnswerSignedAt) {
		t.Errorf("old signature: %v", err)
	}
}
