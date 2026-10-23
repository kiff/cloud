package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// API key format: `kiff_live_<tenant>_<random32>`. The shape is
// the one the auth design recommended.
//
//   - Prefix `kiff_live_` makes a leaked key recognizable in logs
//     and trivial to spot in monitoring (GitHub secret scanning,
//     etc.).
//   - The tenant id is embedded so a leaked key reveals scope, but
//     the cloud's authorization layer still verifies tenant
//     membership on every call. The format is informational, not
//     trusted.
//   - The random suffix is 32 base32-Crockford-style characters
//     drawn from 20 bytes of crypto/rand. base32 picked over base64
//     so the value stays URL-safe and copy-pastable without
//     needing quoting in shells.

const (
	// APIKeyPrefix is the live-key prefix. Test keys (a future v0.2
	// concern) would use a different prefix; the format is otherwise
	// identical.
	APIKeyPrefix = "kiff_live_"

	// APIKeyRandomBytes is the entropy budget for the random
	// portion of a key. 20 bytes -> 32 base32 characters.
	APIKeyRandomBytes = 20

	// APIKeyRandomLen is the encoded length of the random suffix.
	APIKeyRandomLen = 32

	// APIKeyHashSize is sha256.Size for callers that allocate
	// fixed-size buffers without importing the crypto package.
	APIKeyHashSize = sha256.Size
)

// crockfordEncoder is base32 with Crockford's alphabet (no I, L,
// O, U; less ambiguous when read aloud or transcribed) and no
// padding. We use the standard encoder for v0.1 since the alphabet
// difference is cosmetic; revisiting if customer demand surfaces.
var crockfordEncoder = base32.StdEncoding.WithPadding(base32.NoPadding)

// ErrAPIKeyMalformed is returned by ParseAPIKey when the raw value
// does not match the expected shape. Distinct from
// ErrAPIKeyNotFound (Store-side) so middleware can map malformed
// keys to 401 with a different log line than unknown keys.
var ErrAPIKeyMalformed = errors.New("auth: api key is malformed")

// ParsedAPIKey is what callers get back from ParseAPIKey: the
// tenant id embedded in the key plus the SHA-256 hash of the full
// key string. The plaintext is intentionally not retained on the
// struct so that tests, logs, and downstream code do not retain
// the secret beyond the parse boundary.
type ParsedAPIKey struct {
	Tenant string
	Hash   [sha256.Size]byte
}

// HexHash returns the lowercase hex encoding of the parsed hash.
// Convenience for stores and logs that want a printable form.
func (p ParsedAPIKey) HexHash() string {
	return hex.EncodeToString(p.Hash[:])
}

// ParseAPIKey validates the structural shape of a raw key string
// and extracts the tenant id and SHA-256 hash without authorizing
// it. Stores still need to look the hash up; this function only
// gatekeeps obviously-bad inputs before any DB call.
func ParseAPIKey(raw string) (ParsedAPIKey, error) {
	raw = trimBearer(strings.TrimSpace(raw))
	if !strings.HasPrefix(raw, APIKeyPrefix) {
		return ParsedAPIKey{}, fmt.Errorf("%w: missing prefix %q", ErrAPIKeyMalformed, APIKeyPrefix)
	}
	rest := raw[len(APIKeyPrefix):]
	// rest = <tenant>_<random32>
	sep := strings.LastIndex(rest, "_")
	if sep <= 0 || sep == len(rest)-1 {
		return ParsedAPIKey{}, fmt.Errorf("%w: missing tenant or random portion", ErrAPIKeyMalformed)
	}
	tenant := rest[:sep]
	random := rest[sep+1:]
	if tenant == "" {
		return ParsedAPIKey{}, fmt.Errorf("%w: empty tenant", ErrAPIKeyMalformed)
	}
	if len(random) != APIKeyRandomLen {
		return ParsedAPIKey{}, fmt.Errorf("%w: random portion length %d, want %d", ErrAPIKeyMalformed, len(random), APIKeyRandomLen)
	}
	if !isCrockfordBase32(random) {
		return ParsedAPIKey{}, fmt.Errorf("%w: random portion is not valid base32", ErrAPIKeyMalformed)
	}
	return ParsedAPIKey{
		Tenant: tenant,
		Hash:   sha256.Sum256([]byte(raw)),
	}, nil
}

// VerifyAPIKey compares two SHA-256 hashes in constant time. Both
// arguments are full digests; callers that have a parsed key and a
// stored hash both pass byte slices directly. Returns false on any
// length mismatch or hash mismatch.
func VerifyAPIKey(a, b []byte) bool {
	if len(a) != sha256.Size || len(b) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

// GeneratedAPIKey is the result of NewAPIKey: the plaintext key
// (returned to the caller exactly once) and the metadata needed to
// persist the record. The Plaintext field MUST be discarded by the
// caller after the value has been delivered to the user — it is
// kept only on this struct for the brief moment between
// generation and storage.
type GeneratedAPIKey struct {
	// Plaintext is the full `kiff_live_<tenant>_<random32>` string.
	// The caller delivers this once and never persists it.
	Plaintext string
	// Hash is the SHA-256 of Plaintext. Persist this; never the
	// plaintext.
	Hash [sha256.Size]byte
	// Tenant is the tenant id the key is scoped to.
	Tenant string
	// CreatedAt is the wall-clock time of generation.
	CreatedAt time.Time
}

// HexHash returns the lowercase hex encoding of the hash.
func (g GeneratedAPIKey) HexHash() string {
	return hex.EncodeToString(g.Hash[:])
}

// NewAPIKey generates a key for the given tenant. Returns the
// GeneratedAPIKey on success or an error when the tenant id is
// empty / contains the separator character or when crypto/rand
// fails (which would only happen on an OS-level RNG failure).
//
// The tenant id is restricted to characters that are safe in URLs
// and unambiguous in the key format: the underscore is reserved as
// the separator between tenant and random, so embedded underscores
// are rejected. Most tenant ids in this repo are ULIDs (lowercase),
// which fit cleanly.
func NewAPIKey(tenant string, now time.Time) (GeneratedAPIKey, error) {
	if tenant == "" {
		return GeneratedAPIKey{}, errors.New("auth: empty tenant")
	}
	if strings.Contains(tenant, "_") {
		return GeneratedAPIKey{}, fmt.Errorf("auth: tenant %q must not contain '_' (reserved as key separator)", tenant)
	}
	if strings.ContainsAny(tenant, " \t\n\r/?#&=") {
		return GeneratedAPIKey{}, fmt.Errorf("auth: tenant %q contains whitespace or url-reserved characters", tenant)
	}
	buf := make([]byte, APIKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return GeneratedAPIKey{}, fmt.Errorf("auth: read random: %w", err)
	}
	random := strings.ToLower(crockfordEncoder.EncodeToString(buf))
	plaintext := APIKeyPrefix + tenant + "_" + random
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return GeneratedAPIKey{
		Plaintext: plaintext,
		Hash:      sha256.Sum256([]byte(plaintext)),
		Tenant:    tenant,
		CreatedAt: now.UTC(),
	}, nil
}

// isCrockfordBase32 reports whether s consists only of the
// lowercase characters base32 produces. Length is checked
// separately by the caller. We accept lower-case only because
// NewAPIKey emits lower-case; case-insensitive matching here would
// allow visually-distinct inputs to verify, which we don't want.
func isCrockfordBase32(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '2' && c <= '7') {
			continue
		}
		return false
	}
	return true
}
