package billing_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/billing"
)

const testSecret = "whsec_test_abc123"

// signPayload builds a valid Stripe-Signature header for payload at ts.
func signPayload(t *testing.T, payload []byte, secret string, ts time.Time) string {
	t.Helper()
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsStr))
	mac.Write([]byte("."))
	mac.Write(payload)
	return fmt.Sprintf("t=%s,v1=%s", tsStr, hex.EncodeToString(mac.Sum(nil)))
}

func TestVerifySignature_Valid(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	payload := []byte(`{"id":"evt_1","type":"checkout.session.completed"}`)
	hdr := signPayload(t, payload, testSecret, now)
	if err := billing.VerifySignature(payload, hdr, testSecret, 0, now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifySignature_WrongSecret(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	payload := []byte(`{"id":"evt_1"}`)
	hdr := signPayload(t, payload, "whsec_other", now)
	if err := billing.VerifySignature(payload, hdr, testSecret, 0, now); !errors.Is(err, billing.ErrNoValidSignature) {
		t.Fatalf("err = %v, want ErrNoValidSignature", err)
	}
}

func TestVerifySignature_TamperedPayload(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	hdr := signPayload(t, []byte(`{"id":"evt_1"}`), testSecret, now)
	if err := billing.VerifySignature([]byte(`{"id":"evt_HACKED"}`), hdr, testSecret, 0, now); !errors.Is(err, billing.ErrNoValidSignature) {
		t.Fatalf("err = %v, want ErrNoValidSignature for tampered body", err)
	}
}

func TestVerifySignature_Expired(t *testing.T) {
	t.Parallel()
	signedAt := time.Unix(1_700_000_000, 0)
	payload := []byte(`{"id":"evt_1"}`)
	hdr := signPayload(t, payload, testSecret, signedAt)
	// 10 minutes later, default 5m tolerance.
	now := signedAt.Add(10 * time.Minute)
	if err := billing.VerifySignature(payload, hdr, testSecret, 0, now); !errors.Is(err, billing.ErrSignatureExpired) {
		t.Fatalf("err = %v, want ErrSignatureExpired", err)
	}
}

func TestVerifySignature_MissingAndMalformed(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	payload := []byte(`{}`)
	if err := billing.VerifySignature(payload, "", testSecret, 0, now); !errors.Is(err, billing.ErrNoSignature) {
		t.Errorf("empty header: err = %v, want ErrNoSignature", err)
	}
	if err := billing.VerifySignature(payload, "t=123", testSecret, 0, now); !errors.Is(err, billing.ErrMalformedHeader) {
		t.Errorf("no v1: err = %v, want ErrMalformedHeader", err)
	}
	if err := billing.VerifySignature(payload, "v1=deadbeef", testSecret, 0, now); !errors.Is(err, billing.ErrMalformedHeader) {
		t.Errorf("no t: err = %v, want ErrMalformedHeader", err)
	}
}

// Secret rotation: multiple v1 values, one matching, passes.
func TestVerifySignature_RotationMultipleV1(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	payload := []byte(`{"id":"evt_1"}`)
	good := signPayload(t, payload, testSecret, now) // t=..,v1=GOOD
	// Append a bogus v1 value.
	hdr := good + ",v1=00000000"
	if err := billing.VerifySignature(payload, hdr, testSecret, 0, now); err != nil {
		t.Fatalf("rotation header rejected: %v", err)
	}
}
