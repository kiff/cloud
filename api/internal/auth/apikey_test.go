package auth

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewAPIKey_ProducesValidShape(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	key, err := NewAPIKey("01H0test01", now)
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if !strings.HasPrefix(key.Plaintext, APIKeyPrefix) {
		t.Errorf("Plaintext %q missing prefix %q", key.Plaintext, APIKeyPrefix)
	}
	if !strings.Contains(key.Plaintext, "_01H0test01_") {
		t.Errorf("Plaintext %q missing embedded tenant", key.Plaintext)
	}
	if key.Tenant != "01H0test01" {
		t.Errorf("Tenant: got %q, want 01H0test01", key.Tenant)
	}
	if !key.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt: got %v, want %v", key.CreatedAt, now)
	}
	wantHash := sha256.Sum256([]byte(key.Plaintext))
	if key.Hash != wantHash {
		t.Errorf("Hash mismatch: got %x, want %x", key.Hash, wantHash)
	}
	if got := key.HexHash(); len(got) != 64 {
		t.Errorf("HexHash length: got %d, want 64", len(got))
	}
}

func TestNewAPIKey_RoundtripsThroughParse(t *testing.T) {
	t.Parallel()
	key, err := NewAPIKey("acme", time.Time{})
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	parsed, err := ParseAPIKey(key.Plaintext)
	if err != nil {
		t.Fatalf("ParseAPIKey: %v", err)
	}
	if parsed.Tenant != "acme" {
		t.Errorf("Tenant: got %q, want acme", parsed.Tenant)
	}
	if parsed.Hash != key.Hash {
		t.Errorf("Hash mismatch: parsed=%x generated=%x", parsed.Hash, key.Hash)
	}
}

func TestNewAPIKey_UniqueAcrossCalls(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		key, err := NewAPIKey("acme", time.Time{})
		if err != nil {
			t.Fatalf("NewAPIKey: %v", err)
		}
		if _, dup := seen[key.Plaintext]; dup {
			t.Fatalf("duplicate key %q at i=%d", key.Plaintext, i)
		}
		seen[key.Plaintext] = struct{}{}
	}
}

func TestNewAPIKey_RejectsBadTenants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		tenant string
		want   string
	}{
		{"empty", "", "empty tenant"},
		{"underscore", "ten_ant", "must not contain '_'"},
		{"whitespace", "te ant", "url-reserved"},
		{"slash", "ten/ant", "url-reserved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewAPIKey(c.tenant, time.Time{})
			if err == nil {
				t.Fatalf("expected error for %q", c.tenant)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestParseAPIKey_RejectsBadShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", "missing prefix"},
		{"wrong prefix", "wrong_live_acme_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "missing prefix"},
		{"no random", "kiff_live_acme_", "missing tenant or random portion"},
		{"no separator", "kiff_live_acmeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "missing tenant"},
		{"random too short", "kiff_live_acme_abc", "random portion length"},
		{"random non-base32", "kiff_live_acme_!@#$%^&*()01234567890123456789ab", "not valid base32"},
		{"upper-case random", "kiff_live_acme_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "not valid base32"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseAPIKey(c.raw)
			if !errors.Is(err, ErrAPIKeyMalformed) {
				t.Fatalf("expected ErrAPIKeyMalformed for %q, got %v", c.raw, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestParseAPIKey_TrimsBearer(t *testing.T) {
	t.Parallel()
	key, err := NewAPIKey("acme", time.Time{})
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	for _, prefix := range []string{"", "Bearer ", "  bearer ", "BEARER "} {
		parsed, err := ParseAPIKey(prefix + key.Plaintext)
		if err != nil {
			t.Errorf("prefix %q: ParseAPIKey: %v", prefix, err)
		}
		if parsed.Hash != key.Hash {
			t.Errorf("prefix %q: hash mismatch", prefix)
		}
	}
}

func TestVerifyAPIKey(t *testing.T) {
	t.Parallel()
	a := sha256.Sum256([]byte("a"))
	b := sha256.Sum256([]byte("a"))
	c := sha256.Sum256([]byte("c"))
	if !VerifyAPIKey(a[:], b[:]) {
		t.Errorf("equal hashes should verify true")
	}
	if VerifyAPIKey(a[:], c[:]) {
		t.Errorf("different hashes should verify false")
	}
	if VerifyAPIKey(a[:5], b[:]) {
		t.Errorf("short input should verify false")
	}
	if VerifyAPIKey(nil, nil) {
		t.Errorf("nil input should verify false")
	}
}
