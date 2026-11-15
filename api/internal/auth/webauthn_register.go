package auth

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// A passkey's registration proves its possession (the desktop client
// contract, §2.6): the device creating it runs navigator.credentials.create
// (or its platform's API) for kiff.dev, with a challenge KIFF can rebuild and
// attestation "none", and KIFF reads the key from what the authenticator
// signed rather than from the client. It proves the key is a WebAuthn
// credential for kiff.dev, user-verified when made; it doesn't prove the key
// is in hardware (attestation "none" proves nothing about the authenticator),
// so the approval key's signature on the registration stays the gate.
//
// The challenge is stateless: base64url(sha256(RegistrationMessage)), over
// the account, the person and when it was issued, accepted for
// RegistrationMaxAge either side of KIFF's clock. A replay can only register
// the same credential again, which the store already refuses.

// RegistrationMaxAge is how far a registration's issued_at may be from now.
const RegistrationMaxAge = 10 * time.Minute

// RegistrationMessage is what a passkey registration's challenge is the
// SHA-256 of.
func RegistrationMessage(tenant, subject, issuedAt string) []byte {
	return []byte("kiff.passkey.register.v1\n" + tenant + "\n" + subject + "\n" + issuedAt)
}

// Registered is a registration that verified: the credential, its public key
// (SPKI DER, as a Passkey keeps it), its sign counter, and its backup flags.
type Registered struct {
	CredentialID   []byte
	PublicKey      []byte
	SignCount      uint32
	Synced         bool
	BackupEligible bool
}

var (
	ErrRegistrationShape       = errors.New("auth: the registration is malformed")
	ErrRegistrationIssued      = errors.New("auth: the registration's issued_at is too far from now")
	ErrRegistrationType        = errors.New("auth: the registration isn't a WebAuthn create")
	ErrRegistrationOrigin      = errors.New("auth: the registration isn't for kiff.dev")
	ErrRegistrationChallenge   = errors.New("auth: the registration's challenge isn't this person's")
	ErrRegistrationAttestation = errors.New(`auth: the registration's attestation isn't "none"`)
	ErrRegistrationUser        = errors.New("auth: the registration has no user verification")
	ErrRegistrationKey         = errors.New("auth: the registration's key isn't a P-256 ES256 key")
)

const (
	flagAttested   = 0x40
	flagExtensions = 0x80
	// The longest credential id WebAuthn allows.
	maxCredentialID = 1023
	// The most a registration's two fields may be, decoded.
	maxClientData  = 4 << 10
	maxAttestation = 4 << 10
)

// VerifyRegistration checks a passkey's registration for the person, each
// check with its own error:
//
//  1. clientDataJSON's type is "webauthn.create", its origin is one of the
//     RP's and not framed (crossOrigin), and its challenge is
//     base64url(sha256(RegistrationMessage)) with issuedAt within
//     RegistrationMaxAge of now;
//  2. the attestationObject's fmt is "none" with an empty attStmt, and it has
//     no other field;
//  3. authData's rpIdHash is the RP ID's, UP and UV are set, AT is set with
//     one attested credential, and nothing follows it but the extensions ED
//     says are there;
//  4. the credential's COSE key is EC2 (kty 2), ES256 (alg -7), P-256 (crv
//     1), with 32-byte x and y on the curve, and nothing else;
//  5. the credential id is authData's, never the client's;
//  6. the backup flags are what it says of being synced.
func VerifyRegistration(rp RelyingParty, tenant, subject, issuedAt string, clientDataJSON, attestationObject []byte, now time.Time) (Registered, error) {
	if tenant == "" || subject == "" || len(clientDataJSON) == 0 || len(clientDataJSON) > maxClientData ||
		len(attestationObject) == 0 || len(attestationObject) > maxAttestation {
		return Registered{}, ErrRegistrationShape
	}
	at, err := time.Parse(time.RFC3339, issuedAt)
	if err != nil {
		return Registered{}, ErrRegistrationIssued
	}
	if d := now.Sub(at); d > RegistrationMaxAge || d < -RegistrationMaxAge {
		return Registered{}, ErrRegistrationIssued
	}
	var client struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}
	if json.Unmarshal(clientDataJSON, &client) != nil {
		return Registered{}, ErrRegistrationShape
	}
	if client.Type != "webauthn.create" {
		return Registered{}, ErrRegistrationType
	}
	if rp.ID == "" || client.CrossOrigin || !slices.Contains(rp.Origins, client.Origin) {
		return Registered{}, ErrRegistrationOrigin
	}
	digest := sha256.Sum256(RegistrationMessage(tenant, subject, issuedAt))
	challenge, err := b64url(client.Challenge)
	if err != nil || subtle.ConstantTimeCompare(challenge, digest[:]) != 1 {
		return Registered{}, ErrRegistrationChallenge
	}

	decoded, err := decodeCBORWhole(attestationObject)
	if err != nil {
		return Registered{}, ErrRegistrationShape
	}
	obj, ok := decoded.(cborMap)
	if !ok || len(obj) != 3 {
		return Registered{}, ErrRegistrationShape
	}
	format, _ := obj["fmt"].(string)
	stmt, stmtOK := obj["attStmt"].(cborMap)
	if format != "none" || !stmtOK || len(stmt) != 0 {
		return Registered{}, ErrRegistrationAttestation
	}
	authData, ok := obj["authData"].([]byte)
	if !ok || len(authData) < authDataMin {
		return Registered{}, ErrRegistrationShape
	}

	rpHash := sha256.Sum256([]byte(rp.ID))
	if !bytes.Equal(authData[:32], rpHash[:]) {
		return Registered{}, ErrRegistrationOrigin
	}
	flags := authData[32]
	if flags&flagUserPresent == 0 || flags&flagUserVerified == 0 {
		return Registered{}, ErrRegistrationUser
	}
	if flags&flagAttested == 0 {
		return Registered{}, ErrRegistrationShape
	}
	rest := authData[authDataMin:]
	// aaguid (16), the credential id's length (2), the id, then its COSE key.
	if len(rest) < 18 {
		return Registered{}, ErrRegistrationShape
	}
	idLen := int(binary.BigEndian.Uint16(rest[16:18]))
	rest = rest[18:]
	if idLen == 0 || idLen > maxCredentialID || len(rest) < idLen {
		return Registered{}, ErrRegistrationShape
	}
	credential := append([]byte(nil), rest[:idLen]...)
	rest = rest[idLen:]
	coseKey, n, err := decodeCBOR(rest)
	if err != nil {
		return Registered{}, ErrRegistrationKey
	}
	rest = rest[n:]
	if flags&flagExtensions != 0 {
		ext, err := decodeCBORWhole(rest)
		if _, isMap := ext.(cborMap); err != nil || !isMap {
			return Registered{}, ErrRegistrationShape
		}
	} else if len(rest) != 0 {
		return Registered{}, ErrRegistrationShape
	}
	spki, err := es256Key(coseKey)
	if err != nil {
		return Registered{}, err
	}
	return Registered{CredentialID: credential, PublicKey: spki, SignCount: binary.BigEndian.Uint32(authData[33:37]),
		Synced: flags&flagBackedUp != 0, BackupEligible: flags&flagBackupElig != 0}, nil
}

// es256Key is a COSE key as SPKI DER, when it is exactly an EC2 P-256 key for
// ES256 whose point is on the curve.
func es256Key(v any) ([]byte, error) {
	m, ok := v.(cborMap)
	if !ok || len(m) != 5 {
		return nil, ErrRegistrationKey
	}
	kty, _ := m[int64(1)].(int64)
	alg, _ := m[int64(3)].(int64)
	crv, _ := m[int64(-1)].(int64)
	x, _ := m[int64(-2)].([]byte)
	y, _ := m[int64(-3)].([]byte)
	if kty != 2 || alg != -7 || crv != 1 || len(x) != 32 || len(y) != 32 {
		return nil, ErrRegistrationKey
	}
	// ecdh refuses a point that isn't on P-256.
	pub, err := ecdh.P256().NewPublicKey(append(append([]byte{4}, x...), y...))
	if err != nil {
		return nil, ErrRegistrationKey
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, ErrRegistrationKey
	}
	if _, err := deviceKey(der); err != nil {
		return nil, ErrRegistrationKey
	}
	return der, nil
}

// AndroidApp is an Android app allowed to make and use kiff.dev's passkeys:
// its package name and the SHA-256 of its signing certificate, as
// KIFF_ANDROID_APPS gives them ("<package>=<AA:BB:…>", one per app, comma
// separated). kiff.dev's assetlinks.json names the same apps.
type AndroidApp struct {
	Package     string
	Fingerprint []byte
}

// Origin is the app's WebAuthn origin, as Credential Manager puts it in
// clientDataJSON: android:apk-key-hash: and its certificate's SHA-256,
// base64url.
func (a AndroidApp) Origin() string {
	return "android:apk-key-hash:" + base64.RawURLEncoding.EncodeToString(a.Fingerprint)
}

// ParseAndroidApps reads KIFF_ANDROID_APPS: "" is none; anything else must be
// "<package>=<the certificate's SHA-256, colon-separated hex>", comma
// separated.
func ParseAndroidApps(s string) ([]AndroidApp, error) {
	var out []AndroidApp
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pkg, fp, ok := strings.Cut(part, "=")
		pkg, fp = strings.TrimSpace(pkg), strings.TrimSpace(fp)
		raw, err := hex.DecodeString(strings.ReplaceAll(fp, ":", ""))
		if !ok || !androidPackage(pkg) || err != nil || len(raw) != sha256.Size || strings.Count(fp, ":") != sha256.Size-1 {
			return nil, fmt.Errorf("KIFF_ANDROID_APPS: %q is not <package>=<SHA-256 as AA:BB:…>", part)
		}
		out = append(out, AndroidApp{Package: pkg, Fingerprint: raw})
	}
	return out, nil
}

// androidPackage reports whether s is an Android package name: two or more
// dot-separated parts, each a letter then letters, digits or underscores.
func androidPackage(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(s) > 255 {
		return false
	}
	for _, p := range parts {
		if p == "" || !(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z') {
			return false
		}
		for _, r := range p {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
				return false
			}
		}
	}
	return true
}

// PasskeyRelyingParty is KiffRelyingParty with the Android apps' origins
// added: the fixed list of origins a registration or an assertion may come
// from (https://kiff.dev for the web, Windows, and Apple's apps through their
// associated domain; each Android app's apk-key-hash).
func PasskeyRelyingParty(apps []AndroidApp) RelyingParty {
	rp := RelyingParty{ID: KiffRelyingParty.ID, Origins: slices.Clone(KiffRelyingParty.Origins)}
	for _, a := range apps {
		if o := a.Origin(); !slices.Contains(rp.Origins, o) {
			rp.Origins = append(rp.Origins, o)
		}
	}
	return rp
}
