package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"
	"time"
)

// The hand-off answer's vector, for both apps to pin: a test-only P-256 key,
// whose private scalar is sha256(handoffVectorSeed) and is derived here, the
// exact message, its sha256, and a signature over it that KIFF verifies.
// ECDSA signatures are randomized, so an app checks that its own signature
// over the same message verifies with the same key, and that this one does.

// handoffVectorKeyDER is the vector key's public half, SPKI DER, from its seed.
func handoffVectorKeyDER(t *testing.T) []byte {
	t.Helper()
	d := sha256.Sum256([]byte(handoffVectorSeed))
	k := &ecdsa.PrivateKey{D: new(big.Int).SetBytes(d[:])}
	k.PublicKey.Curve = elliptic.P256()
	k.PublicKey.X, k.PublicKey.Y = elliptic.P256().ScalarBaseMult(d[:])
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

const (
	handoffVectorSeed       = "kiff.handoff.v1 test vector"
	handoffVectorSPKIDigest = "85c559569edf864d4b3186426f3541f8bbf8c424d96de17f707b919b2c593970"
	handoffVectorMessage    = "kiff.handoff.v1\nt_vector\nho_0123456789abcdef01234567\ndone\n1\n2026-11-11T17:00:00Z"
	handoffVectorSHA256     = "1b9f7f923489891b736af1fbc494c9348690494d4ae4901844aeb9a0b6df3b6f"
	handoffVectorSignature  = "MEUCIQDJLYymcVMzMDhv+kRXK1BS2DhZvJROyBCjJLzzTy5NDgIgXe7a2OeeShmHHI5HazLS+/BzMjLVBYpjX7JT0Uy67RA="
)

func TestHandoffAnswerVector(t *testing.T) {
	a := HandoffAnswer{TenantID: "t_vector", HandoffID: "ho_0123456789abcdef01234567", Answer: "done", Version: 1, SignedAt: "2026-11-11T17:00:00Z"}
	if got := string(a.Message()); got != handoffVectorMessage {
		t.Fatalf("message =\n%q\nwant\n%q", got, handoffVectorMessage)
	}
	sum := sha256.Sum256(a.Message())
	if got := hex.EncodeToString(sum[:]); got != handoffVectorSHA256 {
		t.Fatalf("sha256 = %s, want %s", got, handoffVectorSHA256)
	}
	der := handoffVectorKeyDER(t)
	// The public half the apps pin is base64 SPKI DER; its sha256 holds it here.
	if got := sha256.Sum256(der); hex.EncodeToString(got[:]) != handoffVectorSPKIDigest {
		t.Fatalf("the vector's public half moved: %s", base64.StdEncoding.EncodeToString(der))
	}
	now := time.Date(2026, 11, 11, 17, 1, 0, 0, time.UTC)
	if err := VerifyHandoffAnswer(der, a, handoffVectorSignature, now); err != nil {
		t.Fatalf("the vector's signature doesn't verify: %v", err)
	}
	// The same signature never stands for "skipped", another hand-off, account or version.
	for name, b := range map[string]HandoffAnswer{
		"answer":  {TenantID: a.TenantID, HandoffID: a.HandoffID, Answer: "skipped", Version: 1, SignedAt: a.SignedAt},
		"handoff": {TenantID: a.TenantID, HandoffID: "ho_other", Answer: "done", Version: 1, SignedAt: a.SignedAt},
		"tenant":  {TenantID: "t_other", HandoffID: a.HandoffID, Answer: "done", Version: 1, SignedAt: a.SignedAt},
		"version": {TenantID: a.TenantID, HandoffID: a.HandoffID, Answer: "done", Version: 2, SignedAt: a.SignedAt},
	} {
		if err := VerifyHandoffAnswer(der, b, handoffVectorSignature, now); !errors.Is(err, ErrAnswerSignature) {
			t.Errorf("%s changed: err = %v, want ErrAnswerSignature", name, err)
		}
	}
	// A hold's answer message is never a hand-off's: the first line differs.
	hold := Answer{TenantID: a.TenantID, ExceptionID: a.HandoffID, Answer: "done", AccountingVersion: 1, SignedAt: a.SignedAt}
	if string(hold.Message()) == handoffVectorMessage {
		t.Fatal("a hold's answer message equals a hand-off's")
	}
}

func TestVerifyHandoffAnswer(t *testing.T) {
	now := time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC)
	k, pub := testDeviceKey(t, elliptic.P256())
	der, _ := ParseDeviceKey(pub)
	a := HandoffAnswer{TenantID: "t1", HandoffID: "ho_1", Answer: "skipped", Version: 1, SignedAt: now.Add(-time.Minute).Format(time.RFC3339)}
	sig := signMessage(t, k, a.Message())
	if err := VerifyHandoffAnswer(der, a, sig, now); err != nil {
		t.Fatalf("valid signature refused: %v", err)
	}
	for _, at := range []string{now.Add(-6 * time.Minute).Format(time.RFC3339), now.Add(6 * time.Minute).Format(time.RFC3339), "today"} {
		b := a
		b.SignedAt = at
		if err := VerifyHandoffAnswer(der, b, signMessage(t, k, b.Message()), now); !errors.Is(err, ErrAnswerSignedAt) {
			t.Errorf("signed_at %q: err = %v, want ErrAnswerSignedAt", at, err)
		}
	}
	if err := VerifyHandoffAnswer(der, a, "bm90IGEgc2lnbmF0dXJl", now); !errors.Is(err, ErrAnswerSignature) {
		t.Errorf("garbage signature: %v", err)
	}
}

func signMessage(t *testing.T, k *ecdsa.PrivateKey, message []byte) string {
	t.Helper()
	digest := sha256.Sum256(message)
	sig, err := ecdsa.SignASN1(rand.Reader, k, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}
