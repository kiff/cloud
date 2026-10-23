// Package secret encrypts the tool credentials the gateway stores.
// Keys come from one Secrets Manager secret that only the
// gateway's process environment receives; apps/api and apps/web never
// hold them, and the ciphertext in Postgres is useless without them.
package secret

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Keyring holds the encryption keys by id. The first is current: new
// credentials are sealed with it; older ones still open until rotation
// re-seals them.
type Keyring struct {
	current string
	keys    map[string]cipher.AEAD
}

var keyID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ParseKeyring reads "id:base64key,id:base64key", current first. Each
// key is 32 random bytes.
func ParseKeyring(spec string) (*Keyring, error) {
	k := &Keyring{keys: map[string]cipher.AEAD{}}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, b64, ok := strings.Cut(part, ":")
		if !ok || !keyID.MatchString(id) {
			return nil, errors.New("secret: each key must be id:base64, with a short id")
		}
		if _, dup := k.keys[id]; dup {
			return nil, fmt.Errorf("secret: key id %q appears twice", id)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("secret: key %q must be 32 bytes, base64", id)
		}
		block, _ := aes.NewCipher(raw)
		aead, _ := cipher.NewGCM(block)
		k.keys[id] = aead
		if k.current == "" {
			k.current = id
		}
	}
	if k.current == "" {
		return nil, errors.New("secret: no keys")
	}
	return k, nil
}

// Current is the id new credentials are sealed with.
func (k *Keyring) Current() string { return k.current }

// aad binds a ciphertext to its account and tool: a row copied to another
// account or tool does not open.
func aad(tenantID, tool string) []byte {
	return []byte("kiff-gateway/tool-credential\x00" + tenantID + "\x00" + tool)
}

// Seal encrypts plaintext for (tenantID, tool) under the current key.
func (k *Keyring) Seal(tenantID, tool, plaintext string) (string, []byte, error) {
	aead := k.keys[k.current]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	return k.current, aead.Seal(nonce, nonce, []byte(plaintext), aad(tenantID, tool)), nil
}

// ErrUnreadable: the key is unknown, or the ciphertext was not sealed
// for this account and tool.
var ErrUnreadable = errors.New("secret: credential cannot be decrypted for this account and tool")

// Open decrypts a credential sealed for (tenantID, tool).
func (k *Keyring) Open(tenantID, tool, id string, sealed []byte) (string, error) {
	aead, ok := k.keys[id]
	if !ok || len(sealed) < aead.NonceSize() {
		return "", ErrUnreadable
	}
	n := aead.NonceSize()
	pt, err := aead.Open(nil, sealed[:n], sealed[n:], aad(tenantID, tool))
	if err != nil {
		return "", ErrUnreadable
	}
	return string(pt), nil
}

// NewKey returns a fresh key spec entry, "id:base64".
func NewKey(id string) (string, error) {
	if !keyID.MatchString(id) {
		return "", errors.New("secret: invalid key id")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return id + ":" + base64.StdEncoding.EncodeToString(raw), nil
}

// Redact replaces every occurrence of each secret in s.
func Redact(s string, secrets ...string) string {
	for _, v := range secrets {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

// RedactJSON removes every occurrence of each secret from every string
// in a JSON document, object keys included, so an escaped form cannot
// slip past a byte-level replace. Invalid JSON is redacted as text.
func RedactJSON(raw []byte, secrets ...string) []byte {
	var live []string
	for _, v := range secrets {
		if len(v) >= 4 {
			live = append(live, v)
		}
	}
	if len(live) == 0 || len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return []byte(Redact(string(raw), live...))
	}
	out, err := json.Marshal(redactValue(v, live))
	if err != nil {
		return []byte(Redact(string(raw), live...))
	}
	return out
}

func redactValue(v any, secrets []string) any {
	switch t := v.(type) {
	case string:
		return Redact(t, secrets...)
	case []any:
		for i := range t {
			t[i] = redactValue(t[i], secrets)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[Redact(k, secrets...)] = redactValue(x, secrets)
		}
		return out
	}
	return v
}
