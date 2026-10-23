package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ApprovalDevice is a device a person approves with: its key lives
// in the device's own secure hardware (the Secure Enclave, Android's
// Keystore, a TPM), behind its biometric or PIN, and signs every answer
// that device gives. KIFF makes its id. A device without such
// hardware isn't meant to be enrolled, and approves nothing: it can read.
// Until KIFF verifies a key's attestation, that the key
// is in hardware is the app's word.
type ApprovalDevice struct {
	ID      string
	Tenant  string
	Subject string
	// Name is what the person calls it, as the device said at sign-in.
	Name string
	// Platform is ios, ipados, macos, android or windows.
	Platform string
	// PublicKey is the device's P-256 key (SPKI DER).
	PublicKey []byte
	// Attestation is the attestation format KIFF verified for the key, or
	// "" when none was (every device today).
	Attestation string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
}

// IsRevoked reports whether the device was removed.
func (d ApprovalDevice) IsRevoked() bool { return d.RevokedAt != nil }

// Platforms a device may name.
var devicePlatforms = map[string]bool{"ios": true, "ipados": true, "macos": true, "android": true, "windows": true}

// ValidDevicePlatform reports whether p is a platform KIFF knows.
func ValidDevicePlatform(p string) bool { return devicePlatforms[p] }

// NewDeviceID makes a device id: "dev_" and 26 base32 characters (130
// random bits).
func NewDeviceID() string {
	b := make([]byte, 17)
	if _, err := rand.Read(b); err != nil {
		panic("auth: no randomness for a device id: " + err.Error())
	}
	return "dev_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:26]
}

// Errors surfaced by DeviceStore implementations.
var (
	ErrDeviceNotFound  = errors.New("auth: device not found")
	ErrDeviceDuplicate = errors.New("auth: device already exists")
)

// DeviceStore keeps the approval devices.
type DeviceStore interface {
	// Insert stores a new device. ErrDeviceDuplicate on an id collision.
	Insert(ctx context.Context, d ApprovalDevice) error
	// Get returns a device, revoked or not. ErrDeviceNotFound when absent.
	Get(ctx context.Context, id string) (ApprovalDevice, error)
	// ListActive returns a person's devices on an account that aren't
	// revoked, newest first.
	ListActive(ctx context.Context, tenant, subject string) ([]ApprovalDevice, error)
	// Revoke removes a device. Idempotent; ErrDeviceNotFound when unknown.
	Revoke(ctx context.Context, id string, at time.Time) error
	// Touch records the device's latest use, by any of its sessions, at
	// most once a minute. Best-effort.
	Touch(ctx context.Context, id string, at time.Time) error
	// FindByKey returns a person's active device on an account with this
	// key, so a device signing in again is the same device.
	// ErrDeviceNotFound when there's none.
	FindByKey(ctx context.Context, tenant, subject string, key []byte) (ApprovalDevice, error)
	// Relabel sets a device's name and platform, as a later sign-in of the
	// same key says them. ErrDeviceNotFound when unknown.
	Relabel(ctx context.Context, id, name, platform string) error
}

// MemoryDeviceStore is the in-memory DeviceStore for tests and dev.
type MemoryDeviceStore struct {
	mu   sync.RWMutex
	byID map[string]ApprovalDevice
}

// NewMemoryDeviceStore returns an empty store.
func NewMemoryDeviceStore() *MemoryDeviceStore {
	return &MemoryDeviceStore{byID: map[string]ApprovalDevice{}}
}

func (s *MemoryDeviceStore) Insert(_ context.Context, d ApprovalDevice) error {
	if err := d.check(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byID[d.ID]; dup {
		return fmt.Errorf("%w: %q", ErrDeviceDuplicate, d.ID)
	}
	// One active device per person, account and key, as Postgres's index.
	for _, o := range s.byID {
		if o.Tenant == d.Tenant && o.Subject == d.Subject && !o.IsRevoked() && string(o.PublicKey) == string(d.PublicKey) {
			return fmt.Errorf("%w: this key is device %q", ErrDeviceDuplicate, o.ID)
		}
	}
	d.PublicKey = append([]byte(nil), d.PublicKey...)
	s.byID[d.ID] = d
	return nil
}

func (s *MemoryDeviceStore) Get(_ context.Context, id string) (ApprovalDevice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.byID[id]
	if !ok {
		return ApprovalDevice{}, ErrDeviceNotFound
	}
	return d, nil
}

func (s *MemoryDeviceStore) ListActive(_ context.Context, tenant, subject string) ([]ApprovalDevice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ApprovalDevice{}
	for _, d := range s.byID {
		if d.Tenant == tenant && d.Subject == subject && !d.IsRevoked() {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemoryDeviceStore) Revoke(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byID[id]
	if !ok {
		return ErrDeviceNotFound
	}
	if d.RevokedAt == nil {
		at = at.UTC()
		d.RevokedAt = &at
		s.byID[id] = d
	}
	return nil
}

func (s *MemoryDeviceStore) Touch(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byID[id]
	if !ok {
		return ErrDeviceNotFound
	}
	at = at.UTC()
	if d.LastUsedAt != nil && at.Sub(*d.LastUsedAt) < time.Minute {
		return nil
	}
	d.LastUsedAt = &at
	s.byID[id] = d
	return nil
}

func (s *MemoryDeviceStore) Relabel(_ context.Context, id, name, platform string) error {
	if !ValidDevicePlatform(platform) {
		return fmt.Errorf("auth: unknown device platform %q", platform)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byID[id]
	if !ok {
		return ErrDeviceNotFound
	}
	d.Name, d.Platform = name, platform
	s.byID[id] = d
	return nil
}

func (s *MemoryDeviceStore) FindByKey(_ context.Context, tenant, subject string, key []byte) (ApprovalDevice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found *ApprovalDevice
	for _, d := range s.byID {
		if d.Tenant == tenant && d.Subject == subject && !d.IsRevoked() && string(d.PublicKey) == string(key) &&
			(found == nil || d.CreatedAt.Before(found.CreatedAt)) {
			d := d
			found = &d
		}
	}
	if found == nil {
		return ApprovalDevice{}, ErrDeviceNotFound
	}
	return *found, nil
}

func (d ApprovalDevice) check() error {
	switch {
	case !strings.HasPrefix(d.ID, "dev_"):
		return errors.New("auth: a device id starts with dev_")
	case strings.TrimSpace(d.Tenant) == "" || strings.TrimSpace(d.Subject) == "":
		return errors.New("auth: a device needs its tenant and subject")
	case !ValidDevicePlatform(d.Platform):
		return fmt.Errorf("auth: unknown device platform %q", d.Platform)
	}
	if _, err := deviceKey(d.PublicKey); err != nil {
		return err
	}
	return nil
}
