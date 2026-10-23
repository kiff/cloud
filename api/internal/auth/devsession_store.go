package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// DeveloperSession is one issued developer session. It is a stored row so revocation is per-device: a
// leaked laptop's session can be killed by deleting its row without
// disturbing the subject's other machines. A stateless signed token
// was the alternative, but a developer session can mint runtime keys
// and drive the operator CLI, so its blast radius warrants per-device
// revoke over all-or-nothing.
//
// Only TokenHash is stored; the plaintext token is delivered once
// through the device-token poll and never persisted. Tenant is bound
// at approval and carried here (not embedded in the token).
type DeveloperSession struct {
	ID         string
	Subject    string
	Tenant     string
	Label      string // device label, e.g. the CLI host, for the user to recognize
	TokenHash  [32]byte
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt *time.Time
	RevokedAt  *time.Time
	// DevicePublicKey is the phone's P-256 key (SPKI DER), bound when the
	// device flow that minted this session was approved. An answer
	// to a held call through this session counts only with its signature.
	// Nil for a session without one, such as the CLI's.
	DevicePublicKey []byte
	// DeviceID names the approval device the session signed in from,
	// whose key it is. "" for a session without a device key, and
	// for one from before approval devices until LinkLegacyDevices gives it one.
	DeviceID string
}

// IsRevoked reports whether the session has been revoked.
func (s DeveloperSession) IsRevoked() bool { return s.RevokedAt != nil }

// IsExpired reports whether the session's TTL has elapsed by now.
func (s DeveloperSession) IsExpired(now time.Time) bool {
	return !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt)
}

// HexHash returns the token hash in lowercase hex (storage/log form).
func (s DeveloperSession) HexHash() string { return hex.EncodeToString(s.TokenHash[:]) }

// Errors surfaced by DeveloperSessionStore implementations.
var (
	ErrDevSessionNotFound  = errors.New("auth: developer session not found")
	ErrDevSessionDuplicate = errors.New("auth: developer session already exists")
	ErrDevSessionRevoked   = errors.New("auth: developer session has been revoked")
	ErrDevSessionExpired   = errors.New("auth: developer session has expired")
)

// DeveloperSessionStore is the persistence boundary for developer
// sessions. The middleware uses GetByHash + Touch on every
// developer-session request; `kiff auth logout` uses RevokeByHash;
// `kiff auth status` uses ListActiveForSubject; tenant soft-delete
// uses RevokeAllForTenant (mirroring the API-key store).
type DeveloperSessionStore interface {
	// Insert persists a fresh session. ErrDevSessionDuplicate on an
	// id or hash collision.
	Insert(ctx context.Context, s DeveloperSession) error

	// GetByHash looks up a session by token hash. Returns
	// ErrDevSessionNotFound when absent, ErrDevSessionRevoked when
	// revoked, ErrDevSessionExpired when past its TTL. The `now` is
	// supplied so the store does not couple to a clock.
	GetByHash(ctx context.Context, hash [32]byte, now time.Time) (DeveloperSession, error)

	// Touch records the most recent use. Best-effort for callers.
	Touch(ctx context.Context, id string, at time.Time) error

	// Revoke soft-deletes one session by id (per-device logout).
	// Idempotent; ErrDevSessionNotFound when the id is unknown.
	Revoke(ctx context.Context, id string, at time.Time) error

	// RevokeByHash soft-deletes the session presenting this token
	// (the shape `kiff auth logout` uses — it holds the token, not
	// the id). Idempotent; ErrDevSessionNotFound when unknown.
	RevokeByHash(ctx context.Context, hash [32]byte, at time.Time) error

	// ListActiveForSubject returns the subject's non-revoked,
	// non-expired sessions, newest first.
	ListActiveForSubject(ctx context.Context, subject string, now time.Time) ([]DeveloperSession, error)

	// RevokeAllForSubject revokes every active session for a subject —
	// the "log out everywhere" shape (`kiff auth logout --all`).
	// Idempotent. Returns the number of sessions revoked.
	RevokeAllForSubject(ctx context.Context, subject string, at time.Time) (int, error)

	// RevokeAllForTenant revokes every active session for a tenant
	// (tenant soft-delete). Idempotent.
	RevokeAllForTenant(ctx context.Context, tenant string, at time.Time) error
}

// MemoryDeveloperSessionStore is the in-memory implementation for
// tests and dev. Concurrent-safe.
type MemoryDeveloperSessionStore struct {
	mu     sync.RWMutex
	byID   map[string]DeveloperSession
	byHash map[[32]byte]string
}

// NewMemoryDeveloperSessionStore returns an empty store.
func NewMemoryDeveloperSessionStore() *MemoryDeveloperSessionStore {
	return &MemoryDeveloperSessionStore{
		byID:   make(map[string]DeveloperSession),
		byHash: make(map[[32]byte]string),
	}
}

func (s *MemoryDeveloperSessionStore) Insert(_ context.Context, sess DeveloperSession) error {
	if strings.TrimSpace(sess.ID) == "" {
		return errors.New("auth: developer session id required")
	}
	if strings.TrimSpace(sess.Subject) == "" || strings.TrimSpace(sess.Tenant) == "" {
		return errors.New("auth: developer session subject and tenant required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byID[sess.ID]; dup {
		return fmt.Errorf("%w: id %q", ErrDevSessionDuplicate, sess.ID)
	}
	if _, dup := s.byHash[sess.TokenHash]; dup {
		return fmt.Errorf("%w: hash collision", ErrDevSessionDuplicate)
	}
	sess.DevicePublicKey = append([]byte(nil), sess.DevicePublicKey...)
	if len(sess.DevicePublicKey) == 0 {
		sess.DevicePublicKey = nil
	}
	s.byID[sess.ID] = sess
	s.byHash[sess.TokenHash] = sess.ID
	return nil
}

func (s *MemoryDeveloperSessionStore) GetByHash(_ context.Context, hash [32]byte, now time.Time) (DeveloperSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byHash[hash]
	if !ok {
		return DeveloperSession{}, ErrDevSessionNotFound
	}
	sess := s.byID[id]
	if sess.IsRevoked() {
		return sess, ErrDevSessionRevoked
	}
	if sess.IsExpired(now) {
		return sess, ErrDevSessionExpired
	}
	return sess, nil
}

func (s *MemoryDeveloperSessionStore) Touch(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return ErrDevSessionNotFound
	}
	at = at.UTC()
	sess.LastSeenAt = &at
	s.byID[id] = sess
	return nil
}

func (s *MemoryDeveloperSessionStore) Revoke(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return ErrDevSessionNotFound
	}
	if sess.RevokedAt != nil {
		return nil
	}
	at = at.UTC()
	sess.RevokedAt = &at
	s.byID[id] = sess
	return nil
}

func (s *MemoryDeveloperSessionStore) RevokeByHash(_ context.Context, hash [32]byte, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byHash[hash]
	if !ok {
		return ErrDevSessionNotFound
	}
	sess := s.byID[id]
	if sess.RevokedAt != nil {
		return nil
	}
	at = at.UTC()
	sess.RevokedAt = &at
	s.byID[id] = sess
	return nil
}

func (s *MemoryDeveloperSessionStore) ListActiveForSubject(_ context.Context, subject string, now time.Time) ([]DeveloperSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeveloperSession, 0)
	for _, sess := range s.byID {
		if sess.Subject != subject || sess.IsRevoked() || sess.IsExpired(now) {
			continue
		}
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemoryDeveloperSessionStore) RevokeAllForSubject(_ context.Context, subject string, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at = at.UTC()
	n := 0
	for id, sess := range s.byID {
		if sess.Subject == subject && sess.RevokedAt == nil {
			sess.RevokedAt = &at
			s.byID[id] = sess
			n++
		}
	}
	return n, nil
}

func (s *MemoryDeveloperSessionStore) RevokeAllForTenant(_ context.Context, tenant string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at = at.UTC()
	for id, sess := range s.byID {
		if sess.Tenant == tenant && sess.RevokedAt == nil {
			sess.RevokedAt = &at
			s.byID[id] = sess
		}
	}
	return nil
}
