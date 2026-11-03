package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// A device key is the public half of a key that lives on a person's phone:
// a P-256 key, in Secure Enclave on an iPhone, bound to the
// developer session the phone signed in with. An answer to a held call
// made through that session counts only with a signature from the key,
// so a stolen session token cannot approve anything.

// MaxAnswerSkew is how far an answer's signed_at may be from the server's
// clock. A hold is answered only once, so this only bounds how long a
// signature that was never sent stays usable.
const MaxAnswerSkew = 5 * time.Minute

var (
	ErrDeviceKey       = errors.New("auth: device key must be a P-256 public key, base64 SPKI DER")
	ErrAnswerSignature = errors.New("auth: the signature does not verify for this answer")
	ErrAnswerSignedAt  = errors.New("auth: signed_at must be RFC 3339, within 5 minutes of now")
)

// ParseDeviceKey decodes a base64 SPKI DER public key and accepts only
// P-256 ECDSA. It returns the DER bytes to store.
func ParseDeviceKey(b64 string) ([]byte, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(der) == 0 || len(der) > 512 {
		return nil, ErrDeviceKey
	}
	if _, err := deviceKey(der); err != nil {
		return nil, err
	}
	return der, nil
}

func deviceKey(der []byte) (*ecdsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, ErrDeviceKey
	}
	k, ok := pub.(*ecdsa.PublicKey)
	if !ok || k.Curve != elliptic.P256() {
		return nil, ErrDeviceKey
	}
	return k, nil
}

// DeviceKeyFingerprint is a short, readable name for a key: the first
// eight bytes of its SHA-256, in four groups ("3f2a 91c0 7b1e 0d44").
func DeviceKeyFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := hex.EncodeToString(sum[:8])
	return h[0:4] + " " + h[4:8] + " " + h[8:12] + " " + h[12:16]
}

// Answer is what a phone signs when it answers a held call.
type Answer struct {
	TenantID          string
	ExceptionID       string
	Answer            string // approve-once | reject | change-card
	NextStep          string
	Note              string
	AccountingVersion int
	SignedAt          string // RFC 3339, as sent
}

// Message is the exact text KIFF verifies, one field per line:
//
//	kiff.answer.v1
//	<tenant_id>
//	<exception_id>
//	<answer>
//	<next_step>            empty unless a refusal names one
//	<sha256(note) hex>     empty when there is no note
//	<accounting_version>
//	<signed_at>
func (a Answer) Message() []byte {
	note := ""
	if a.Note != "" {
		sum := sha256.Sum256([]byte(a.Note))
		note = hex.EncodeToString(sum[:])
	}
	return []byte(strings.Join([]string{
		"kiff.answer.v1", a.TenantID, a.ExceptionID, a.Answer, a.NextStep, note,
		strconv.Itoa(a.AccountingVersion), a.SignedAt,
	}, "\n"))
}

// VerifyAnswer checks a base64 DER ECDSA signature over a.Message() with
// the device key, and that signed_at is within MaxAnswerSkew of now.
func VerifyAnswer(keyDER []byte, a Answer, sigB64 string, now time.Time) error {
	at, err := time.Parse(time.RFC3339, a.SignedAt)
	if err != nil {
		return ErrAnswerSignedAt
	}
	if d := now.Sub(at); d > MaxAnswerSkew || d < -MaxAnswerSkew {
		return ErrAnswerSignedAt
	}
	k, err := deviceKey(keyDER)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) == 0 {
		return ErrAnswerSignature
	}
	digest := sha256.Sum256(a.Message())
	if !ecdsa.VerifyASN1(k, digest[:], sig) {
		return ErrAnswerSignature
	}
	return nil
}

// DeviceSignatureHeader carries a phone's signature on a setup request:
// "t=<RFC 3339>,v1=<base64 DER ECDSA>".
const DeviceSignatureHeader = "KIFF-Device-Signature"

// ErrRequestSignature is a setup request without a valid signature.
var ErrRequestSignature = errors.New("auth: this request needs the phone's signature")

// RequestMessage is what a phone signs to make a setup call, one field
// per line:
//
//	kiff.request.v1
//	<tenant_id>
//	<METHOD>
//	<path and query, as sent>
//	<sha256(body) hex>
//	<signed_at>
func RequestMessage(tenantID, method, uri string, body []byte, signedAt string) []byte {
	sum := sha256.Sum256(body)
	return requestMessage(tenantID, method, uri, hex.EncodeToString(sum[:]), signedAt)
}

func requestMessage(tenantID, method, uri, bodySHA256, signedAt string) []byte {
	return []byte(strings.Join([]string{"kiff.request.v1", tenantID, method, uri, bodySHA256, signedAt}, "\n"))
}

// VerifyRequest checks a KIFF-Device-Signature header against the
// request with the device key, and that signed_at is within
// MaxAnswerSkew of now. It returns the request's replay identity: the
// SHA-256 of the key and the signed message. It is not the signature's
// bytes, because an ECDSA signature has a second valid form, (r, n-s),
// that anyone can make from the first without the key.
func VerifyRequest(keyDER []byte, tenantID, method, uri string, body []byte, header string, now time.Time) ([]byte, error) {
	sum := sha256.Sum256(body)
	return VerifyRequestDigest(keyDER, tenantID, method, uri, hex.EncodeToString(sum[:]), header, now)
}

// VerifyRequestDigest is VerifyRequest for a request another KIFF service
// received (kiff/tafs#206): it has the body's SHA-256, in lowercase hex,
// not the body.
func VerifyRequestDigest(keyDER []byte, tenantID, method, uri, bodySHA256, header string, now time.Time) ([]byte, error) {
	t, v, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(header), "t="), ",v1=")
	if !ok || !strings.HasPrefix(strings.TrimSpace(header), "t=") {
		return nil, ErrRequestSignature
	}
	at, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return nil, ErrAnswerSignedAt
	}
	if d := now.Sub(at); d > MaxAnswerSkew || d < -MaxAnswerSkew {
		return nil, ErrAnswerSignedAt
	}
	k, err := deviceKey(keyDER)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(sig) == 0 {
		return nil, ErrRequestSignature
	}
	message := requestMessage(tenantID, method, uri, bodySHA256, t)
	digest := sha256.Sum256(message)
	if !ecdsa.VerifyASN1(k, digest[:], sig) {
		return nil, ErrRequestSignature
	}
	id := sha256.Sum256(append(append(append([]byte(nil), keyDER...), 0), message...))
	return id[:], nil
}

// ErrRelinkProof is a re-link whose new approval key didn't sign it.
var ErrRelinkProof = errors.New("auth: the new approval key must sign the re-link")

// RelinkMessage is what a phone's new approval key signs to re-link it
// (kiff/tafs#233's case 2), one field per line:
//
//	kiff.approval-key-relink.v1
//	<tenant_id>
//	<sha256(device key SPKI DER) hex>   the session's device key names it
//	<sha256(new approval key SPKI DER) hex>
//	<signed_at>
//
// The device key names the session: the phone knows it, and only that
// session holds it.
func RelinkMessage(tenantID string, deviceKeyDER, newKeyDER []byte, signedAt string) []byte {
	device, key := sha256.Sum256(deviceKeyDER), sha256.Sum256(newKeyDER)
	return []byte(strings.Join([]string{"kiff.approval-key-relink.v1", tenantID,
		hex.EncodeToString(device[:]), hex.EncodeToString(key[:]), signedAt}, "\n"))
}

// VerifyRelink checks that the new approval key signed RelinkMessage
// (base64 DER ECDSA), within MaxAnswerSkew of now: the phone holds the
// key, and its biometric prompt works, before KIFF swaps anything.
func VerifyRelink(newKeyDER []byte, tenantID string, deviceKeyDER []byte, signedAt, sigB64 string, now time.Time) error {
	at, err := time.Parse(time.RFC3339, signedAt)
	if err != nil {
		return ErrAnswerSignedAt
	}
	if d := now.Sub(at); d > MaxAnswerSkew || d < -MaxAnswerSkew {
		return ErrAnswerSignedAt
	}
	k, err := deviceKey(newKeyDER)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) == 0 {
		return ErrRelinkProof
	}
	digest := sha256.Sum256(RelinkMessage(tenantID, deviceKeyDER, newKeyDER, signedAt))
	if !ecdsa.VerifyASN1(k, digest[:], sig) {
		return ErrRelinkProof
	}
	return nil
}
