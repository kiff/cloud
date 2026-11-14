package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// A passkey approves the same way a device key does: it signs the exact
// action. For a computer with no secure key store of its own (an old Mac,
// Linux), a WebAuthn credential — the person's phone's own passkey through
// a QR code, or a USB security key — signs an assertion whose challenge is
// the SHA-256 of the message a device key would sign (RequestMessage), so
// the approval is bound to that one action exactly as a device signature is.
// Registering one is itself an approval by an existing device.

// Passkey is a WebAuthn credential registered to an account: its id, the
// account it belongs to, its P-256 public key (SPKI DER, as the browser's
// getPublicKey() gives it; only ES256 is accepted), and the last sign
// counter KIFF saw.
type Passkey struct {
	ID        []byte
	Tenant    string
	PublicKey []byte
	SignCount uint32
}

// Assertion is what the browser's navigator.credentials.get() answers, each
// field base64url as WebAuthn gives it.
type Assertion struct {
	CredentialID      string `json:"credential_id"`
	ClientDataJSON    string `json:"client_data_json"`
	AuthenticatorData string `json:"authenticator_data"`
	Signature         string `json:"signature"`
}

// RelyingParty is who the passkeys are for: KIFF's RP ID (kiff.dev) and the
// origins a ceremony may run on (https://kiff.dev).
type RelyingParty struct {
	ID      string
	Origins []string
}

var (
	ErrPasskeyAccount   = errors.New("auth: this passkey isn't this account's")
	ErrAssertionType    = errors.New("auth: the assertion isn't a WebAuthn get")
	ErrAssertionOrigin  = errors.New("auth: the assertion isn't for kiff.dev")
	ErrAssertionAction  = errors.New("auth: the assertion's challenge isn't this action's digest")
	ErrAssertionUser    = errors.New("auth: the assertion has no user verification")
	ErrAssertionSigned  = errors.New("auth: the assertion's signature does not verify")
	ErrAssertionCounter = errors.New("auth: the passkey's sign counter went backwards")
	ErrAssertionShape   = errors.New("auth: the assertion is malformed")
)

// Approved is what an assertion that verified tells the caller: the sign
// counter to store, whether the passkey is synced to a cloud account (the
// BS flag: such a passkey is never a second, independent device), and the
// approval's replay identity, which the caller records as used.
type Approved struct {
	SignCount uint32
	Synced    bool
	Used      []byte
}

const (
	flagUserPresent  = 0x01
	flagUserVerified = 0x04
	flagBackedUp     = 0x10
	// authData is rpIdHash (32), flags (1), signCount (4), then extensions.
	authDataMin = 37
)

func b64url(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
}

// VerifyAssertion checks that a passkey approved exactly message, in the
// order the review set out, each with its own error:
//
//  1. the credential is this passkey, and the passkey is the tenant's;
//  2. clientDataJSON's type is "webauthn.get";
//  3. its origin is one of the RP's, never in another site's frame
//     (crossOrigin), and authData's rpIdHash is the RP ID's;
//  4. its challenge is the base64url of sha256(message), exactly;
//  5. the user was present and verified (UP and UV flags);
//  6. the signature verifies over authData ‖ sha256(clientDataJSON) with the
//     passkey's key;
//  7. the sign counter doesn't go backwards (a counter of 0 on both sides is
//     an authenticator that keeps none).
//
// It returns what verified (Approved): the counter, whether the passkey is
// synced, and the replay identity (the credential and the message), which
// the caller records as used, as it does a device signature's.
func VerifyAssertion(rp RelyingParty, key Passkey, tenantID string, a Assertion, message []byte) (Approved, []byte, error) {
	id, err := b64url(a.CredentialID)
	if err != nil || len(id) == 0 || subtle.ConstantTimeCompare(id, key.ID) != 1 || key.Tenant == "" || key.Tenant != tenantID {
		return Approved{}, nil, ErrPasskeyAccount
	}
	rawClient, err1 := b64url(a.ClientDataJSON)
	authData, err2 := b64url(a.AuthenticatorData)
	sig, err3 := b64url(a.Signature)
	if err1 != nil || err2 != nil || err3 != nil || len(authData) < authDataMin || len(sig) == 0 || len(rawClient) > 4096 {
		return Approved{}, nil, ErrAssertionShape
	}
	var client struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
		// CrossOrigin is a ceremony in a frame inside another site: an approval never runs framed.
		CrossOrigin bool `json:"crossOrigin"`
	}
	if json.Unmarshal(rawClient, &client) != nil {
		return Approved{}, nil, ErrAssertionShape
	}
	if client.Type != "webauthn.get" {
		return Approved{}, nil, ErrAssertionType
	}
	rpHash := sha256.Sum256([]byte(rp.ID))
	if rp.ID == "" || client.CrossOrigin || !slices.Contains(rp.Origins, client.Origin) || !bytes.Equal(authData[:32], rpHash[:]) {
		return Approved{}, nil, ErrAssertionOrigin
	}
	digest := sha256.Sum256(message)
	challenge, err := b64url(client.Challenge)
	if err != nil || subtle.ConstantTimeCompare(challenge, digest[:]) != 1 {
		return Approved{}, nil, ErrAssertionAction
	}
	flags := authData[32]
	if flags&flagUserPresent == 0 || flags&flagUserVerified == 0 {
		return Approved{}, nil, ErrAssertionUser
	}
	pub, err := deviceKey(key.PublicKey)
	if err != nil {
		return Approved{}, nil, err
	}
	clientHash := sha256.Sum256(rawClient)
	signed := sha256.Sum256(append(append([]byte(nil), authData...), clientHash[:]...))
	if !ecdsa.VerifyASN1(pub, signed[:], sig) {
		return Approved{}, nil, ErrAssertionSigned
	}
	count := binary.BigEndian.Uint32(authData[33:37])
	if (count != 0 || key.SignCount != 0) && count <= key.SignCount {
		return Approved{}, nil, ErrAssertionCounter
	}
	used := sha256.Sum256(append(append(append([]byte("passkey\x00"), key.ID...), 0), message...))
	return Approved{SignCount: count, Synced: flags&flagBackedUp != 0, Used: used[:]}, used[:], nil
}

// VerifyRequestAssertion is VerifyRequestDigest for a passkey: the request
// message a device key would sign (kiff.request.v1, signed_at as sent),
// approved by the assertion, within MaxAnswerSkew of now.
func VerifyRequestAssertion(rp RelyingParty, key Passkey, tenantID, method, uri, bodySHA256, signedAt string, a Assertion, now time.Time) (Approved, []byte, error) {
	at, err := time.Parse(time.RFC3339, signedAt)
	if err != nil {
		return Approved{}, nil, ErrAnswerSignedAt
	}
	if d := now.Sub(at); d > MaxAnswerSkew || d < -MaxAnswerSkew {
		return Approved{}, nil, ErrAnswerSignedAt
	}
	return VerifyAssertion(rp, key, tenantID, a, requestMessage(tenantID, method, uri, bodySHA256, signedAt))
}
