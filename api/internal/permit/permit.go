// Package permit mints execution permits: a short-lived Ed25519
// JWT that authorizes one execution of one tool call, which a tool or a
// customer-run relay verifies itself, so it needs no KIFF-held key.
package permit

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Type is the permit's JOSE typ (RFC 8725 §3.11): verifiers refuse any
// other, so a permit is never mistaken for another JWT.
const Type = "kiff-permit+jwt"

// Alg is the fully specified algorithm name (RFC 9864). EdDSA is refused.
const Alg = "Ed25519"

// DetailType is the authorization_details type KIFF defines (RFC 9396).
// Only KIFF verifiers understand it.
const DetailType = "https://kiff.dev/permits/tool-call"

// DefaultLifetime is exp - iat. MaxLifetime is the most a
// verifier accepts.
const (
	DefaultLifetime = 60 * time.Second
	MaxLifetime     = 300 * time.Second
)

// Detail is the one authorization_details entry: what exactly is
// authorized.
type Detail struct {
	Type       string `json:"type"`
	Tenant     string `json:"tenant"`
	Tool       string `json:"tool"`
	ArgsSHA256 string `json:"args_sha256"`
	Op         string `json:"op"`
	Card       string `json:"card"`
	Decision   string `json:"decision"`
}

// Claims are the permit's JWT claims.
type Claims struct {
	Iss     string   `json:"iss"`
	Aud     string   `json:"aud"`
	Sub     string   `json:"sub"`
	Iat     int64    `json:"iat"`
	Exp     int64    `json:"exp"`
	Jti     string   `json:"jti"`
	Details []Detail `json:"authorization_details"`
}

// Key is a signing key and its id.
type Key struct {
	ID   string
	Priv ed25519.PrivateKey
}

// PublicKey is a published verification key.
type PublicKey struct {
	ID  string
	Pub ed25519.PublicKey
}

// ParseKey reads a signing key as "kid:base64url(32-byte seed)".
func ParseKey(s string) (Key, error) {
	id, enc, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || !validKID(id) {
		return Key{}, errors.New("permit: the signing key must be kid:base64url(seed)")
	}
	seed, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(enc, "="))
	if err != nil || len(seed) != ed25519.SeedSize {
		return Key{}, errors.New("permit: the signing key seed must be 32 bytes, base64url")
	}
	return Key{ID: id, Priv: ed25519.NewKeyFromSeed(seed)}, nil
}

// ParsePublicKeys reads extra published keys, "kid:base64url(pub),…":
// the next key before it signs, and a retired one until its permits have
// expired.
func ParsePublicKeys(s string) ([]PublicKey, error) {
	var out []PublicKey
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, enc, ok := strings.Cut(part, ":")
		pub, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(enc, "="))
		if !ok || !validKID(id) || err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("permit: published key %q must be kid:base64url(32-byte public key)", id)
		}
		out = append(out, PublicKey{ID: id, Pub: pub})
	}
	return out, nil
}

func validKID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// Public returns the key's public half.
func (k Key) Public() PublicKey {
	return PublicKey{ID: k.ID, Pub: k.Priv.Public().(ed25519.PublicKey)}
}

// Sign returns claims as a compact JWS signed with k.
func (k Key) Sign(c Claims) (string, error) { return k.sign(Type, c) }

// sign returns claims as a compact JWS of type typ signed with k.
func (k Key) sign(typ string, claims any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": Alg, "typ": typ, "kid": k.ID})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64(header) + "." + b64(payload)
	return input + "." + b64(ed25519.Sign(k.Priv, []byte(input))), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// NewJTI returns a random permit id.
func NewJTI() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "pmt_" + hex.EncodeToString(b[:])
}

// JWK is one key of the JWKS (RFC 8037 OKP, crv Ed25519).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

// JWKS is the published key set, the signing key first.
func JWKS(signing PublicKey, others []PublicKey) map[string][]JWK {
	keys := []JWK{jwk(signing)}
	for _, p := range others {
		if p.ID != signing.ID {
			keys = append(keys, jwk(p))
		}
	}
	return map[string][]JWK{"keys": keys}
}

func jwk(p PublicKey) JWK {
	return JWK{Kty: "OKP", Crv: "Ed25519", X: b64(p.Pub), Kid: p.ID, Alg: Alg, Use: "sig"}
}
