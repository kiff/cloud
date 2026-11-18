// Package newdevice sends the "new device signed in" email: the sign-in
// provider (Clerk) tells KIFF of each sign-in (session.created, signed,
// clerkhook), KIFF tells a device the person has used from a new one by a
// hash of its client id, and a sign-in on a new device is emailed to the
// person, in their language, a moment later. A device approval in the tafs
// app within that moment makes it a "tafs by KIFF" email.
//
// An account's first sign-in is recorded and not emailed: no device on
// record and the account made with this sign-in (clerkhook.Session.
// NewAccount) is no news. A person with no device on record and an older
// account is emailed: KIFF can't tell that device from a stranger's.
//
// The email names the device, the browser, the sign-in method when it's
// certain, and the network's guess of the place. The provider's webhook
// carries none of them, so KIFF reads them from the provider as the email
// goes (Describer) and keeps them nowhere: not in the store, not in logs.
package newdevice

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/clerkhook"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

const (
	// Hold is how long a new-device email waits for a device approval in
	// the tafs app from the same sign-in.
	Hold = 2 * time.Minute
	// Window is the device-approval window: a waiting row is gone after it,
	// sent or not.
	Window = 10 * time.Minute
	// Unseen is how long a device is remembered after it was last seen.
	Unseen = 180 * 24 * time.Hour
	// RetryAfter is how long a failed send waits before it is tried again,
	// within Window.
	RetryAfter = time.Minute
	// maxBody bounds a webhook's body.
	maxBody = 64 << 10
)

// SignIn is a sign-in as the provider described it.
type SignIn struct {
	SessionID, Subject string
	// ClientHash is sha256 of the provider's client id (one per browser or
	// app); the id itself is never kept.
	ClientHash []byte
	// NewAccount: the account was made with this sign-in.
	NewAccount bool
	At         time.Time
}

// Due is a new-device email to send now.
type Due struct {
	SessionID, Subject string
	At                 time.Time
	Tafs               bool
	// Until is when the waiting row expires: a send that failed is tried
	// again until then (Store.Again).
	Until time.Time
}

// Store keeps the known devices and the waiting emails.
type Store interface {
	// Seen records the sign-in's client as one of its person's devices. When
	// the client is new to its person and this isn't the account's first
	// sign-in (the person had another device, or the account is older than
	// this sign-in), it holds the email until now+Hold, in the same
	// transaction, and reports true.
	Seen(ctx context.Context, s SignIn, now time.Time) (bool, error)
	// Through marks the sign-in session as one a device approval in the tafs
	// app came from, until now+Window.
	Through(ctx context.Context, sessionID, subject string, now time.Time) error
	// TakeDue removes and returns the emails due by now. It also removes the
	// waiting rows that expired and the devices unseen since now-Unseen.
	TakeDue(ctx context.Context, now time.Time) ([]Due, error)
	// Again puts back an email whose send failed, due at at, unless at is
	// past d.Until: a security notice lost to a passing error is worse than
	// a late one.
	Again(ctx context.Context, d Due, at time.Time) error
}

// Mailer sends the email (notify.ResendNotifier).
type Mailer interface {
	NewDeviceSignedIn(ctx context.Context, to string, d notify.NewDevice) error
}

// Emails finds a person's address (owneremail.ClerkEmailLookup).
type Emails interface {
	EmailForSubject(ctx context.Context, subject string) (string, error)
}

// Subjects maps a person to their account (tenant.PostgresSubjectStore).
type Subjects interface {
	Get(ctx context.Context, subject string) (tenant.SubjectMapping, error)
}

// Tenants reads an account, for the person's language (personlang) and
// the account's time zone.
type Tenants interface {
	Get(ctx context.Context, id string) (tenant.Tenant, error)
}

// Description is a sign-in as the provider describes it when the email
// goes. Any of it may be empty; the email then leaves its row out.
type Description struct {
	// Device is the device ("iPhone", "Android"), Browser its browser.
	Device, Browser string
	// City and Country are the network's guess.
	City, Country string
	// Method is how the person signed in, when it's certain: "password",
	// or a provider's name ("Google").
	Method string
	// Password: the person signs in with a password.
	Password bool
}

// Accounts says when the provider made a person's account
// (ClerkDescriber), for a sign-in event that doesn't say.
type Accounts interface {
	AccountMade(ctx context.Context, subject string) (time.Time, error)
}

// Describer reads a sign-in's description from the provider
// (ClerkDescriber).
type Describer interface {
	Describe(ctx context.Context, sessionID, subject string) (Description, error)
}

// Service is the webhook, the tafs marker and the sender.
type Service struct {
	Store  Store
	Mailer Mailer
	Emails Emails
	// Describer gives the device, the place and the method; the email goes
	// without them when it's nil or fails.
	Describer Describer
	// Accounts tells an account's first sign-in when the event doesn't say
	// when the account was made. Without it, or when it fails, the sign-in
	// isn't taken as the account's first: an email too many, never a
	// silent one.
	Accounts Accounts
	// Subjects and Tenants give the person's language and the account's
	// time zone; English and UTC without them.
	Subjects Subjects
	Tenants  Tenants
	// Secret is the webhook's signing secret ("whsec_…").
	Secret string
	// SiteURL is kiff.dev, for the logos; SessionsURL is where the person
	// sees their devices and signs one out.
	SiteURL, SessionsURL string
	Logger               *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Hash is what a client id is kept as.
func Hash(clientID string) []byte {
	h := sha256.Sum256([]byte(clientID))
	return h[:]
}

// ServeHTTP is the provider's webhook: a signed session.created records the
// sign-in; any other signed event is acknowledged and dropped. A bad
// signature is 401, so the provider's retries stop only on a good one.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	now := s.now()
	if err := clerkhook.Verify(s.Secret, r.Header.Get("svix-id"), r.Header.Get("svix-timestamp"), r.Header.Get("svix-signature"), body, now); err != nil {
		http.Error(w, "signature", http.StatusUnauthorized)
		return
	}
	ev, err := clerkhook.Parse(body)
	if err != nil {
		http.Error(w, "payload", http.StatusBadRequest)
		return
	}
	if ev.Type != "session.created" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	in := SignIn{SessionID: ev.Session.ID, Subject: ev.Session.UserID, ClientHash: Hash(ev.Session.ClientID), At: ev.Session.At(now)}
	in.NewAccount = s.newAccount(r.Context(), ev.Session, in.At)
	if _, err := s.Store.Seen(r.Context(), in, now); err != nil {
		// 500: the provider sends it again.
		s.logger().ErrorContext(r.Context(), "newdevice: recording a sign-in failed", slog.String("err", err.Error()))
		http.Error(w, "store", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// newAccount reports whether the sign-in is its account's first, from the
// event, else from the provider.
func (s *Service) newAccount(ctx context.Context, sess clerkhook.Session, at time.Time) bool {
	if isNew, known := sess.NewAccount(); known {
		return isNew
	}
	if s.Accounts == nil {
		return false
	}
	made, err := s.Accounts.AccountMade(ctx, sess.UserID)
	if err != nil {
		s.logger().WarnContext(ctx, "newdevice: when the account was made couldn't be read; not taken as its first sign-in", slog.String("err", err.Error()))
		return false
	}
	return clerkhook.MadeWith(made, at)
}

// clean keeps a provider's field to one short line.
func clean(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if r := []rune(v); len(r) > 80 {
		v = string(r[:80])
	}
	return v
}

// Through marks the sign-in session a device approval in the tafs app came
// from, so its new-device email says "tafs by KIFF". A failure is only
// logged: the email then names KIFF alone.
func (s *Service) Through(ctx context.Context, sessionID, subject string) {
	if s == nil || s.Store == nil || strings.TrimSpace(sessionID) == "" || subject == "" {
		return
	}
	if err := s.Store.Through(ctx, sessionID, subject, s.now()); err != nil {
		s.logger().WarnContext(ctx, "newdevice: marking a tafs sign-in failed", slog.String("err", err.Error()))
	}
}

// Send sends the emails due now. Each is taken off the store before it
// goes, so none goes twice; one that can't be sent is logged and dropped.
func (s *Service) Send(ctx context.Context) {
	due, err := s.Store.TakeDue(ctx, s.now())
	if err != nil {
		s.logger().WarnContext(ctx, "newdevice: reading due emails failed", slog.String("err", err.Error()))
		return
	}
	for _, d := range due {
		if err := s.send(ctx, d); err != nil {
			s.logger().WarnContext(ctx, "newdevice: an email didn't go; trying again in a minute", slog.String("err", err.Error()))
			if err := s.Store.Again(ctx, d, s.now().Add(RetryAfter)); err != nil {
				s.logger().WarnContext(ctx, "newdevice: an email couldn't be put back", slog.String("err", err.Error()))
			}
		}
	}
}

func (s *Service) send(ctx context.Context, d Due) error {
	to, err := s.Emails.EmailForSubject(ctx, d.Subject)
	if err != nil {
		return err
	}
	if strings.TrimSpace(to) == "" {
		return errors.New("newdevice: the person has no address")
	}
	desc := s.describe(ctx, d)
	lang, zone := s.account(ctx, d.Subject)
	return s.Mailer.NewDeviceSignedIn(ctx, to, notify.NewDevice{Device: clean(desc.Device), Browser: clean(desc.Browser),
		Method: clean(desc.Method), Password: desc.Password, City: clean(desc.City), Country: clean(desc.Country),
		At: d.At, Zone: zone, Tafs: d.Tafs, SessionsURL: s.SessionsURL, SiteURL: s.SiteURL, Lang: lang, Key: "new-device/" + d.SessionID})
}

// describe asks the provider for the sign-in's description. Without it the
// email still goes, with only the time: a late or bare notice is better
// than none. The failure is logged without the description.
func (s *Service) describe(ctx context.Context, d Due) Description {
	if s.Describer == nil {
		return Description{}
	}
	desc, err := s.Describer.Describe(ctx, d.SessionID, d.Subject)
	if err != nil {
		s.logger().WarnContext(ctx, "newdevice: the sign-in's description couldn't be read; sending without it", slog.String("err", err.Error()))
		return Description{}
	}
	return desc
}

// account is the person's last language on their account and the
// account's time zone, else English and UTC.
func (s *Service) account(ctx context.Context, subject string) (words.Lang, *time.Location) {
	if s.Subjects == nil || s.Tenants == nil {
		return words.EN, time.UTC
	}
	m, err := s.Subjects.Get(ctx, subject)
	if err != nil {
		return words.EN, time.UTC
	}
	t, err := s.Tenants.Get(ctx, m.TenantID)
	if err != nil {
		return words.EN, time.UTC
	}
	zone, err := time.LoadLocation(tenant.TimeZoneFor(t))
	if err != nil {
		zone = time.UTC
	}
	return personlang.Of(t, subject), zone
}

// Run sends what's due every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Send(ctx)
		}
	}
}
