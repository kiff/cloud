// Package clerkhook reads the sign-in provider's (Clerk's) webhooks: it
// checks each one's signature, as Clerk signs them (through Svix), and
// reads the one event KIFF acts on, session.created. KIFF takes nothing
// from a webhook it can't verify.
package clerkhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Tolerance is how far a webhook's timestamp may be from now: an older
// one, replayed, is refused.
const Tolerance = 5 * time.Minute

// ErrSignature is a webhook whose signature, or timestamp, doesn't check.
var ErrSignature = errors.New("clerkhook: the webhook's signature doesn't check")

// Verify checks body against its svix-id, svix-timestamp and
// svix-signature headers with secret ("whsec_" and the key, base64): one
// of the header's "v1,<base64>" signatures must be the HMAC-SHA256 of
// "<id>.<timestamp>.<body>", and the timestamp within Tolerance of now.
func Verify(secret, id, timestamp, signature string, body []byte, now time.Time) error {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(secret), "whsec_"))
	if err != nil || len(key) == 0 || id == "" {
		return ErrSignature
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return ErrSignature
	}
	if d := now.Sub(time.Unix(ts, 0)); d > Tolerance || d < -Tolerance {
		return ErrSignature
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + strconv.FormatInt(ts, 10) + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, part := range strings.Fields(signature) {
		version, sig, ok := strings.Cut(part, ",")
		if !ok || version != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(sig)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrSignature
}

// Session is a session.created event's session, as KIFF reads it: whose
// it is, the browser or app it's on, and when its person's account was
// made. The event carries no device or place (no latest_activity): the
// new-device email reads those from the provider when it goes.
type Session struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	ClientID string `json:"client_id"`
	// CreatedAt is milliseconds since the epoch.
	CreatedAt int64 `json:"created_at"`
	User      struct {
		// CreatedAt is when the account was made, in milliseconds.
		CreatedAt int64 `json:"created_at"`
	} `json:"user"`
}

// FirstSignIn is how soon after an account is made its sign-in counts as
// the account's first: signing up signs in, a minute or so later.
const FirstSignIn = 15 * time.Minute

// NewAccount reports whether the session is its account's first sign-in:
// the account was made at most FirstSignIn before it. known is false when
// the event doesn't say when the account was made.
func (s Session) NewAccount() (isNew, known bool) {
	if s.User.CreatedAt <= 0 {
		return false, false
	}
	return MadeWith(time.UnixMilli(s.User.CreatedAt), s.At(time.Time{})), true
}

// MadeWith reports whether an account made at made was made with the
// sign-in at signedIn.
func MadeWith(made, signedIn time.Time) bool {
	if made.IsZero() || signedIn.IsZero() {
		return false
	}
	gap := signedIn.Sub(made)
	return gap >= -time.Minute && gap <= FirstSignIn
}

// Event is a webhook's type and, for session.created, its session.
type Event struct {
	Type    string
	Session Session
}

// Parse reads a verified webhook body. A session.created without a
// session id, user or client is refused: there's nothing to act on.
func Parse(body []byte) (Event, error) {
	var raw struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Event{}, err
	}
	e := Event{Type: raw.Type}
	if raw.Type != "session.created" {
		return e, nil
	}
	if err := json.Unmarshal(raw.Data, &e.Session); err != nil {
		return Event{}, err
	}
	if e.Session.ID == "" || e.Session.UserID == "" || e.Session.ClientID == "" {
		return Event{}, errors.New("clerkhook: a session.created without its session, user or client")
	}
	return e, nil
}

// At is when the session was created, or now when it doesn't say.
func (s Session) At(now time.Time) time.Time {
	if s.CreatedAt <= 0 {
		return now
	}
	return time.UnixMilli(s.CreatedAt).UTC()
}
