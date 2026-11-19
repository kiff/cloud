package proofs

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// TestKeys are the proof runner's test phone keys (KIFF_PROOF_TEST_KEYS):
// a hold answer signed by one counts only on a managed test tenant, and
// one never reaches an override. Each entry is a key's
// fingerprint: the SHA-256 of its DER public key in hex (64 characters),
// or the 16-character short form KIFF shows (auth.DeviceKeyFingerprint);
// spaces and colons are ignored.
type TestKeys struct {
	full  map[string]bool
	short map[string]bool
}

// ParseTestKeys reads the fingerprints, returning the ones it couldn't
// read so the caller can log them (never silently drop a key).
func ParseTestKeys(list []string) (TestKeys, []string) {
	k := TestKeys{full: map[string]bool{}, short: map[string]bool{}}
	var bad []string
	for _, raw := range list {
		f := strings.ToLower(strings.NewReplacer(" ", "", ":", "").Replace(strings.TrimSpace(raw)))
		if f == "" {
			continue
		}
		if _, err := hex.DecodeString(f); err != nil {
			bad = append(bad, raw)
			continue
		}
		switch len(f) {
		case 64:
			k.full[f] = true
		case 16:
			k.short[f] = true
		default:
			bad = append(bad, raw)
		}
	}
	return k, bad
}

// Empty reports whether no test key is configured.
func (k TestKeys) Empty() bool { return len(k.full) == 0 && len(k.short) == 0 }

// Has reports whether der (a DER public key) is a test key.
func (k TestKeys) Has(der []byte) bool {
	if len(der) == 0 || k.Empty() {
		return false
	}
	sum := sha256.Sum256(der)
	f := hex.EncodeToString(sum[:])
	return k.full[f] || k.short[f[:16]]
}
