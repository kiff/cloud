package session

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// revealTTL bounds how long a minted key waits for its one reveal. It
// matches the flash cookie's lifetime: long enough to survive the
// redirect, short enough that an unread reveal does not linger.
const revealTTL = 5 * time.Minute

// revealVault holds a freshly minted API key between the handler that
// minted it and the page that shows it once.
//
// The flash cookie is signed, not encrypted: anything in it can be
// read by whoever holds the cookie value — a browser export, a
// captured header, a log line. A key placed there stays usable until
// it is revoked, long after the cookie expires. So the key stays on
// the server and the cookie carries only an opaque reference, which
// is worthless once the reveal has been taken or has expired, and
// which only the session that minted the key can redeem.
//
// In memory, on purpose: a reveal lives for one redirect. If the web
// process restarts inside that window the reveal is lost, and the key
// has to be minted again, which is the safe direction to fail in.
type revealVault struct {
	mu      sync.Mutex
	entries map[string]revealEntry
}

type revealEntry struct {
	plaintext string
	owner     string
	expires   time.Time
}

func newRevealVault() *revealVault {
	return &revealVault{entries: map[string]revealEntry{}}
}

// put stores plaintext for owner and returns the reference to carry
// in the cookie.
func (v *revealVault) put(plaintext, owner string, now time.Time) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(b[:])
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, e := range v.entries {
		if !now.Before(e.expires) {
			delete(v.entries, k)
		}
	}
	v.entries[ref] = revealEntry{plaintext: plaintext, owner: owner, expires: now.Add(revealTTL)}
	return ref, nil
}

// take returns the plaintext for ref once. It is removed on the first
// attempt whether or not that attempt succeeds, so a reference can be
// tried at most once: a wrong owner or an expired entry burns it.
func (v *revealVault) take(ref, owner string, now time.Time) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.entries[ref]
	if !ok {
		return "", false
	}
	delete(v.entries, ref)
	if !now.Before(e.expires) || (e.owner != "" && e.owner != owner) {
		return "", false
	}
	return e.plaintext, true
}
