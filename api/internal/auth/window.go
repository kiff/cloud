package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// An approval window (kiff/tafs#494): one fingerprint, then taps. The
// phone's approval key, whose own access control asks for the person's
// fingerprint or face on every use, signs kiff.window.v1 once; KIFF records
// the window, server side. While it is open, a routine action (one that
// widens nothing a Card in force allows: connecting an app, a tools Card,
// a held call within its Card, a version) is signed with the device key,
// which asks for nothing, with the window's id inside the signed message.
// A critical action (anything that widens, extra room, resume, adding a
// person) never accepts a window: the approval key, every time.
//
// The window is the server's, not the phone's: a hardware timeout on either
// platform counts any fingerprint on the phone, unlocking it included, so
// only a record opened by KIFF's own prompt proves the fingerprint was the
// app's. It ends on expiry, at its cap, on sign-out, when the session or
// device is revoked, on a halt, and when the app closes it (backgrounded
// over 30 seconds, or the screen locked).

const (
	// WindowLength is how long a window lasts from its last approval.
	WindowLength = 5 * time.Minute
	// WindowCap is the most a window lasts from the fingerprint that
	// opened it, however many approvals restart it.
	WindowCap = 15 * time.Minute
	// WindowMessage names the message the approval key signs to open one.
	WindowMessage = "kiff.window.v1"
	// windowExpiresSkew is how far past now + WindowLength a phone's
	// `expires` may fall, for its clock; the server's own expiry never does.
	windowExpiresSkew = 30 * time.Second
)

// The keys that sign a phone's request, as KIFF names them to the services
// that ask (SignedWith, /v1/devices/verify, /v1/me/device-signature).
const (
	// KeyDevice is the device key: no prompt. Alone it signs the safe
	// direction; inside a window, routine actions too.
	KeyDevice = "device"
	// KeyCritical is the approval key, behind the person's fingerprint or
	// face on every use: what the words call the critical key.
	KeyCritical = "critical"
)

// Why a window closed, as its record says.
const (
	WindowClosedByApp      = "app"      // the app's own Close (backgrounded, locked, signing out)
	WindowClosedBySignOut  = "sign_out" // the session that opened it ended
	WindowClosedByHalt     = "halt"
	WindowClosedByRevoked  = "revoked"  // the device, or the session, was revoked
	WindowClosedByReplaced = "replaced" // a new window on the same device took its place
	// WindowClosedByKeyChange: the session's approval key was set or
	// re-linked, so the fingerprint that opened the window is no longer its key.
	WindowClosedByKeyChange = "key_changed"
)

// ApprovalWindow is one window, as KIFF keeps it.
type ApprovalWindow struct {
	ID       string
	Tenant   string
	Subject  string
	DeviceID string
	// SessionID is the phone's session that opened it: sign-out of that
	// session ends it.
	SessionID string
	OpenedAt  time.Time
	// ExpiresAt moves forward with each approval inside the window, never
	// past CapAt.
	ExpiresAt time.Time
	CapAt     time.Time
	// OpenedBy is the fingerprint of the approval key that opened it, as
	// receipts name keys: a window counts only while that key is the
	// session's, so a new enrolment and re-link end it.
	OpenedBy string
	ClosedAt *time.Time
	ClosedBy string
}

// OpenAt reports whether the window is open at now.
func (w ApprovalWindow) OpenAt(now time.Time) bool {
	return w.ClosedAt == nil && now.Before(w.ExpiresAt) && now.Before(w.CapAt)
}

// Restarted is the expiry an approval at now gives the window: WindowLength
// ahead, never past the cap.
func (w ApprovalWindow) Restarted(now time.Time) time.Time {
	e := now.Add(WindowLength)
	if e.After(w.CapAt) {
		return w.CapAt
	}
	return e
}

// NewWindowID makes a window id: "win_" and 26 base32 characters.
func NewWindowID() string {
	b := make([]byte, 17)
	if _, err := rand.Read(b); err != nil {
		panic("auth: no randomness for a window id: " + err.Error())
	}
	return "win_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:26]
}

// ErrWindowNotFound is a window that isn't there, or isn't the device's.
var ErrWindowNotFound = errors.New("auth: no such window")

// OpenWindow is the body of POST /v1/me/window: the kiff.window.v1 message
// the approval key signed (the request's signature covers the body). The
// session and device, when the phone names them, are the signer's own,
// which KIFF checks against the session; a phone that doesn't know its
// session's id leaves them out, and KIFF takes its own. The nonce keeps two
// openings apart; expires says how long the phone asked for, which the
// server caps at WindowLength from its own clock.
type OpenWindow struct {
	Message string `json:"message"`
	Session string `json:"session"`
	Device  string `json:"device"`
	Expires string `json:"expires"`
	Nonce   string `json:"nonce"`
}

// ErrOpenWindow is an opening that isn't kiff.window.v1 for this session.
var ErrOpenWindow = errors.New("auth: not a kiff.window.v1 opening for this session")

// Check validates the opening for the session id at now and returns the
// expiry the window gets: the phone's, or now + WindowLength when the
// phone's is later. An `expires` in the past, malformed, or more than
// WindowLength (and a little skew) ahead is refused.
func (o OpenWindow) Check(id Identity, now time.Time) (time.Time, error) {
	if o.Message != WindowMessage || id.DeviceSessionID == "" || id.DeviceID == "" ||
		(o.Session != "" && o.Session != id.DeviceSessionID) || (o.Device != "" && o.Device != id.DeviceID) {
		return time.Time{}, ErrOpenWindow
	}
	if n := len(strings.TrimSpace(o.Nonce)); n < 16 || n > 128 {
		return time.Time{}, errors.New("auth: the opening's nonce is 16 to 128 characters")
	}
	at, err := time.Parse(time.RFC3339, o.Expires)
	if err != nil {
		return time.Time{}, errors.New("auth: expires must be RFC 3339")
	}
	if !at.After(now) {
		return time.Time{}, errors.New("auth: expires is in the past")
	}
	if at.After(now.Add(WindowLength + windowExpiresSkew)) {
		return time.Time{}, errors.New("auth: expires is more than 5 minutes ahead")
	}
	if most := now.Add(WindowLength); at.After(most) {
		at = most
	}
	return at, nil
}

// WindowStore keeps the windows, shared by every API process.
type WindowStore interface {
	// Open records w, closing any other open window of its device
	// (WindowClosedByReplaced).
	Open(ctx context.Context, w ApprovalWindow) error
	// Get returns a window, closed or not. ErrWindowNotFound when absent.
	Get(ctx context.Context, id string) (ApprovalWindow, error)
	// OpenFor returns the device's window that is open at now.
	// ErrWindowNotFound when there's none.
	OpenFor(ctx context.Context, device string, now time.Time) (ApprovalWindow, error)
	// Restart moves an open window's expiry to expires. A closed window
	// stays closed; ErrWindowNotFound when absent.
	Restart(ctx context.Context, id string, expires time.Time) error
	// Close ends a window at at, for why. Idempotent: a closed one keeps
	// its first closing. ErrWindowNotFound when absent.
	Close(ctx context.Context, id string, at time.Time, why string) error
	// CloseAll ends every open window matching the fields that are set
	// (tenant, subject, device, session; "" matches any) and reports how
	// many it closed. A halt passes the tenant; a sign-out, the session; a
	// revoked device, the device.
	CloseAll(ctx context.Context, match WindowMatch, at time.Time, why string) (int, error)
}

// WindowMatch names the windows CloseAll ends: each field set narrows it.
type WindowMatch struct {
	Tenant, Subject, Device, Session string
}

func (m WindowMatch) matches(w ApprovalWindow) bool {
	return (m.Tenant == "" || m.Tenant == w.Tenant) && (m.Subject == "" || m.Subject == w.Subject) &&
		(m.Device == "" || m.Device == w.DeviceID) && (m.Session == "" || m.Session == w.SessionID)
}

// MemoryWindowStore is the WindowStore for one process: tests and dev.
type MemoryWindowStore struct {
	mu   sync.Mutex
	byID map[string]ApprovalWindow
}

// NewMemoryWindowStore returns an empty store.
func NewMemoryWindowStore() *MemoryWindowStore {
	return &MemoryWindowStore{byID: map[string]ApprovalWindow{}}
}

func (m *MemoryWindowStore) Open(_ context.Context, w ApprovalWindow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.byID[w.ID]; dup {
		return errors.New("auth: window id already used")
	}
	at := w.OpenedAt
	for id, o := range m.byID {
		if o.DeviceID == w.DeviceID && o.ClosedAt == nil {
			o.ClosedAt, o.ClosedBy = &at, WindowClosedByReplaced
			m.byID[id] = o
		}
	}
	m.byID[w.ID] = w
	return nil
}

func (m *MemoryWindowStore) Get(_ context.Context, id string) (ApprovalWindow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.byID[id]
	if !ok {
		return ApprovalWindow{}, ErrWindowNotFound
	}
	return w, nil
}

func (m *MemoryWindowStore) OpenFor(_ context.Context, device string, now time.Time) (ApprovalWindow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var open []ApprovalWindow
	for _, w := range m.byID {
		if w.DeviceID == device && w.OpenAt(now) {
			open = append(open, w)
		}
	}
	if len(open) == 0 {
		return ApprovalWindow{}, ErrWindowNotFound
	}
	sort.Slice(open, func(i, j int) bool { return open[i].OpenedAt.After(open[j].OpenedAt) })
	return open[0], nil
}

func (m *MemoryWindowStore) Restart(_ context.Context, id string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.byID[id]
	if !ok {
		return ErrWindowNotFound
	}
	if w.ClosedAt == nil {
		w.ExpiresAt = expires
		m.byID[id] = w
	}
	return nil
}

func (m *MemoryWindowStore) Close(_ context.Context, id string, at time.Time, why string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.byID[id]
	if !ok {
		return ErrWindowNotFound
	}
	if w.ClosedAt == nil {
		w.ClosedAt, w.ClosedBy = &at, why
		m.byID[id] = w
	}
	return nil
}

func (m *MemoryWindowStore) CloseAll(_ context.Context, match WindowMatch, at time.Time, why string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, w := range m.byID {
		if w.ClosedAt == nil && match.matches(w) {
			w.ClosedAt, w.ClosedBy = &at, why
			m.byID[id] = w
			n++
		}
	}
	return n, nil
}
