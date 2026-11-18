// Package billing turns verified Stripe webhook events into KIFF
// plan state. It owns three concerns kept deliberately
// separate from the rest of the cloud:
//
//   - signature verification (this file): a webhook payload is only
//     trusted if it carries a valid Stripe-Signature for the
//     configured signing secret, within a freshness window. This is
//     the security boundary — the endpoint is unauthenticated
//     (Stripe cannot present a tenant identity), so the signature is
//     the only thing standing between a POST and a plan change.
//   - event parsing (event.go): a minimal projection of the Stripe
//     event shape — only the fields the processor reads.
//   - synchronization (processor.go): idempotent mapping of an event
//     to exactly one tenant via server-controlled correlation, then
//     a plan/billing-status write.
//
// The package never trusts a caller-supplied plan or tenant id on
// its own: the plan comes from our own catalog metadata on the
// Stripe object, and the tenant is resolved against our own store.
package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultTolerance bounds replay: a signature whose timestamp is
// older (or further in the future) than this is rejected even if the
// HMAC matches. Stripe's libraries default to 5 minutes.
const DefaultTolerance = 5 * time.Minute

// Verification errors. Callers map all of these to HTTP 400 — an
// unverifiable payload is never processed.
var (
	ErrNoSignature      = errors.New("billing: missing Stripe-Signature header")
	ErrMalformedHeader  = errors.New("billing: malformed Stripe-Signature header")
	ErrSignatureExpired = errors.New("billing: signature timestamp outside tolerance")
	ErrNoValidSignature = errors.New("billing: no v1 signature matched the signing secret")
)

// VerifySignature checks a Stripe webhook payload against the
// Stripe-Signature header using the signing secret, following
// Stripe's scheme: the header is a comma-separated list of
// "t=<unix>" and "v1=<hex hmac>" pairs; the signed payload is
// "<t>.<raw body>" HMAC-SHA256'd with the secret. Multiple v1
// values can appear (during secret rotation); any match passes.
//
// now is injected so tests pin the freshness window. tolerance<=0
// uses DefaultTolerance.
func VerifySignature(payload []byte, sigHeader, secret string, tolerance time.Duration, now time.Time) error {
	if strings.TrimSpace(sigHeader) == "" {
		return ErrNoSignature
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}

	var (
		tsStr      string
		signatures []string
	)
	for _, part := range strings.Split(sigHeader, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			tsStr = kv[1]
		case "v1":
			signatures = append(signatures, kv[1])
		}
	}
	if tsStr == "" || len(signatures) == 0 {
		return ErrMalformedHeader
	}

	tsSecs, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return ErrMalformedHeader
	}
	ts := time.Unix(tsSecs, 0)
	if diff := now.Sub(ts); diff < -tolerance || diff > tolerance {
		return fmt.Errorf("%w: timestamp %s, now %s", ErrSignatureExpired, ts.UTC(), now.UTC())
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsStr))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := mac.Sum(nil)
	expectedHex := hex.EncodeToString(expected)

	for _, sig := range signatures {
		// Constant-time compare on the decoded bytes; fall back to a
		// constant-time hex compare if the header value is not valid
		// hex (never matches, but stays timing-flat).
		if got, derr := hex.DecodeString(sig); derr == nil {
			if hmac.Equal(got, expected) {
				return nil
			}
		} else if hmac.Equal([]byte(sig), []byte(expectedHex)) {
			return nil
		}
	}
	return ErrNoValidSignature
}
