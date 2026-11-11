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
	return verifySigned(keyDER, a.Message(), a.SignedAt, sigB64, now)
}

// HandoffAnswer is what a phone signs when it answers a step a taf handed
// to its owner ("Your step"): "done" or "skipped".
type HandoffAnswer struct {
	TenantID  string
	HandoffID string
	Answer    string // done | skipped
	Version   int    // the hand-off's version, 1 until its shape changes
	SignedAt  string // RFC 3339, as sent
}

// HandoffMessage names the hand-off answer's message, its first line.
const HandoffMessage = "kiff.handoff.v1"

// Message is the exact text KIFF verifies, one field per line:
//
//	kiff.handoff.v1
//	<tenant_id>
//	<handoff_id>
//	<answer>               done | skipped
//	<version>
//	<signed_at>
func (a HandoffAnswer) Message() []byte {
	return []byte(strings.Join([]string{HandoffMessage, a.TenantID, a.HandoffID, a.Answer, strconv.Itoa(a.Version), a.SignedAt}, "\n"))
}

// VerifyHandoffAnswer checks a base64 DER ECDSA signature over a.Message()
// with the device key, and that signed_at is within MaxAnswerSkew of now.
func VerifyHandoffAnswer(keyDER []byte, a HandoffAnswer, sigB64 string, now time.Time) error {
	return verifySigned(keyDER, a.Message(), a.SignedAt, sigB64, now)
}

func verifySigned(keyDER, message []byte, signedAt, sigB64 string, now time.Time) error {
	at, err := time.Parse(time.RFC3339, signedAt)
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
	digest := sha256.Sum256(message)
	if !ecdsa.VerifyASN1(k, digest[:], sig) {
		return ErrAnswerSignature
	}
	return nil
}

// DeviceSignatureHeader carries a phone's signature on a setup request:
// "t=<RFC 3339>,v1=<base64 DER ECDSA>", or, signed inside an approval
// window (kiff/tafs#494), "t=<RFC 3339>,v2=<base64 DER ECDSA>,w=<window id>":
// the window's id is then the last line of the signed message
// (RequestMessageWindow), so the signature can't be moved to another window.
const DeviceSignatureHeader = "KIFF-Device-Signature"

// signatureHeader is a DeviceSignatureHeader taken apart: its time, its
// signature, and the window it names (v2), or "" (v1).
type signatureHeader struct {
	At, Signature, Window string
}

// parseSignatureHeader reads "t=…,v1=…" or "t=…,v2=…,w=…". Anything else
// is ErrRequestSignature.
func parseSignatureHeader(header string) (signatureHeader, error) {
	var h signatureHeader
	parts := strings.Split(strings.TrimSpace(header), ",")
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "t=") {
		return h, ErrRequestSignature
	}
	h.At = strings.TrimPrefix(parts[0], "t=")
	version := ""
	for _, part := range parts[1:] {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return h, ErrRequestSignature
		}
		switch k {
		case "v1", "v2":
			if version != "" {
				return h, ErrRequestSignature
			}
			version, h.Signature = k, v
		case "w":
			h.Window = strings.TrimSpace(v)
		default:
			return h, ErrRequestSignature
		}
	}
	// A v2 names its window; a v1 names none. A window id is "win_" and
	// 26 base32 characters, so it can't carry a newline into the message.
	if version == "" || (version == "v2") != (h.Window != "") || (h.Window != "" && !windowIDShape(h.Window)) {
		return h, ErrRequestSignature
	}
	return h, nil
}

// windowIDShape reports whether s is shaped as a window id.
func windowIDShape(s string) bool {
	if len(s) != 30 || !strings.HasPrefix(s, "win_") {
		return false
	}
	for _, r := range s[4:] {
		if !(r >= 'a' && r <= 'z') && !(r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}

// SignatureWindow is the window a DeviceSignatureHeader names, or "".
func SignatureWindow(header string) string {
	h, err := parseSignatureHeader(header)
	if err != nil {
		return ""
	}
	return h.Window
}

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

// RequestMessageWindow is what a phone signs with its device key inside an
// approval window (kiff/tafs#494): RequestMessage's fields, under
// kiff.request.v2, with the window's id as the last line.
//
//	kiff.request.v2
//	<tenant_id>
//	<METHOD>
//	<path and query, as sent>
//	<sha256(body) hex>
//	<signed_at>
//	<window id>
func RequestMessageWindow(tenantID, method, uri string, body []byte, signedAt, window string) []byte {
	sum := sha256.Sum256(body)
	return requestMessageWindow(tenantID, method, uri, hex.EncodeToString(sum[:]), signedAt, window)
}

func requestMessageWindow(tenantID, method, uri, bodySHA256, signedAt, window string) []byte {
	if window == "" {
		return requestMessage(tenantID, method, uri, bodySHA256, signedAt)
	}
	return []byte(strings.Join([]string{"kiff.request.v2", tenantID, method, uri, bodySHA256, signedAt, window}, "\n"))
}

// VerifyRequest checks a KIFF-Device-Signature header against the
// request with the device key, and that signed_at is within
// MaxAnswerSkew of now. It returns the request's replay identity: the
// SHA-256 of the key and the signed message. It is not the signature's
// bytes, because an ECDSA signature has a second valid form, (r, n-s),
// that anyone can make from the first without the key.
func VerifyRequest(keyDER []byte, tenantID, method, uri string, body []byte, header string, now time.Time) ([]byte, error) {
	id, _, err := VerifyRequestWindow(keyDER, tenantID, method, uri, body, header, now)
	return id, err
}

// VerifyRequestWindow is VerifyRequest that also returns the window the
// signature names (a v2 header, kiff/tafs#494), or "" for one signed
// outside a window. The caller checks the window is open; the signature
// only binds the request to it.
func VerifyRequestWindow(keyDER []byte, tenantID, method, uri string, body []byte, header string, now time.Time) ([]byte, string, error) {
	sum := sha256.Sum256(body)
	return VerifyRequestDigestWindow(keyDER, tenantID, method, uri, hex.EncodeToString(sum[:]), header, now)
}

// VerifyRequestDigest is VerifyRequest for a request another KIFF service
// received (kiff/tafs#206): it has the body's SHA-256, in lowercase hex,
// not the body.
func VerifyRequestDigest(keyDER []byte, tenantID, method, uri, bodySHA256, header string, now time.Time) ([]byte, error) {
	id, _, err := VerifyRequestDigestWindow(keyDER, tenantID, method, uri, bodySHA256, header, now)
	return id, err
}

// VerifyRequestDigestWindow is VerifyRequestDigest that also returns the
// window the signature names, as VerifyRequestWindow does.
func VerifyRequestDigestWindow(keyDER []byte, tenantID, method, uri, bodySHA256, header string, now time.Time) ([]byte, string, error) {
	h, err := parseSignatureHeader(header)
	if err != nil {
		return nil, "", err
	}
	at, err := time.Parse(time.RFC3339, h.At)
	if err != nil {
		return nil, "", ErrAnswerSignedAt
	}
	if d := now.Sub(at); d > MaxAnswerSkew || d < -MaxAnswerSkew {
		return nil, "", ErrAnswerSignedAt
	}
	k, err := deviceKey(keyDER)
	if err != nil {
		return nil, "", err
	}
	sig, err := base64.StdEncoding.DecodeString(h.Signature)
	if err != nil || len(sig) == 0 {
		return nil, "", ErrRequestSignature
	}
	message := requestMessageWindow(tenantID, method, uri, bodySHA256, h.At, h.Window)
	digest := sha256.Sum256(message)
	if !ecdsa.VerifyASN1(k, digest[:], sig) {
		return nil, "", ErrRequestSignature
	}
	id := sha256.Sum256(append(append(append([]byte(nil), keyDER...), 0), message...))
	return id[:], h.Window, nil
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
