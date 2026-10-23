package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Developer-session token. Format:
//
//	kiff_dev_<random32>
//
// Unlike an API key (`kiff_live_<tenant>_<random>`), a developer
// session does NOT embed the tenant: the tenant is bound at approval
// time and lives on the DeveloperSession record, not in the token.
// This keeps the token opaque — it reveals only that it is a KIFF
// developer session, nothing about the workspace it unlocks.
//
//   - Prefix `kiff_dev_` makes a leaked session recognizable in logs
//     and distinct from a runtime key at a glance (and lets the auth
//     middleware route it without a store round-trip).
//   - The random suffix is 32 base32 characters from 20 bytes of
//     crypto/rand — the same entropy budget as an API key.
//   - Only the SHA-256 hash is ever stored; the plaintext is shown to
//     the CLI once (via the device-token poll) and never persisted.

const (
	// DevSessionPrefix is the developer-session token prefix.
	DevSessionPrefix = "kiff_dev_"

	// DevSessionRandomBytes is the entropy budget for the random
	// portion. 20 bytes -> 32 base32 characters.
	DevSessionRandomBytes = 20

	// DevSessionRandomLen is the encoded length of the random suffix.
	DevSessionRandomLen = 32
)

// ErrDevSessionMalformed is returned by ParseDevSessionToken when the
// raw value does not match the expected shape. Distinct from the
// store-side not-found/revoked/expired errors so the middleware can
// map a malformed token to 401 with its own log line.
var ErrDevSessionMalformed = errors.New("auth: developer session token is malformed")

// ParsedDevSession is the result of ParseDevSessionToken: the
// SHA-256 hash of the full token. No tenant is embedded (see the
// format note above); tenant resolution happens against the store
// record. The plaintext is intentionally not retained.
type ParsedDevSession struct {
	Hash [sha256.Size]byte
}

// HexHash returns the lowercase hex encoding of the parsed hash.
func (p ParsedDevSession) HexHash() string { return hex.EncodeToString(p.Hash[:]) }

// ParseDevSessionToken validates the structural shape of a raw
// developer-session token and extracts its SHA-256 hash without
// authorizing it. The store still looks the hash up; this only
// gatekeeps obviously-bad inputs before any store call.
func ParseDevSessionToken(raw string) (ParsedDevSession, error) {
	raw = trimBearer(strings.TrimSpace(raw))
	if !strings.HasPrefix(raw, DevSessionPrefix) {
		return ParsedDevSession{}, fmt.Errorf("%w: missing prefix %q", ErrDevSessionMalformed, DevSessionPrefix)
	}
	random := raw[len(DevSessionPrefix):]
	if len(random) != DevSessionRandomLen {
		return ParsedDevSession{}, fmt.Errorf("%w: random portion length %d, want %d", ErrDevSessionMalformed, len(random), DevSessionRandomLen)
	}
	if !isCrockfordBase32(random) {
		return ParsedDevSession{}, fmt.Errorf("%w: random portion is not valid base32", ErrDevSessionMalformed)
	}
	return ParsedDevSession{Hash: sha256.Sum256([]byte(raw))}, nil
}

// GeneratedDevSession is the result of NewDevSessionToken: the
// plaintext token (delivered to the CLI exactly once, via the
// device-token poll) and the hash to persist. Discard Plaintext
// after delivery.
type GeneratedDevSession struct {
	Plaintext string
	Hash      [sha256.Size]byte
}

// HexHash returns the lowercase hex encoding of the hash.
func (g GeneratedDevSession) HexHash() string { return hex.EncodeToString(g.Hash[:]) }

// NewDevSessionToken generates a fresh developer-session token. The
// only error path is an OS-level crypto/rand failure.
func NewDevSessionToken() (GeneratedDevSession, error) {
	buf := make([]byte, DevSessionRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return GeneratedDevSession{}, fmt.Errorf("auth: generate developer session: %w", err)
	}
	// Lowercase: ParseDevSessionToken accepts only the lowercase
	// base32 alphabet (matching the API-key convention), so a
	// visually-distinct uppercase variant never verifies.
	random := strings.ToLower(devSessionEncoder.EncodeToString(buf))
	plaintext := DevSessionPrefix + random
	return GeneratedDevSession{
		Plaintext: plaintext,
		Hash:      sha256.Sum256([]byte(plaintext)),
	}, nil
}

// devSessionEncoder matches the API-key encoder: base32, no padding,
// so the token stays URL-safe and copy-pastable.
var devSessionEncoder = base32.StdEncoding.WithPadding(base32.NoPadding)
