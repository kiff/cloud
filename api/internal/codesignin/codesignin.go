// Package codesignin signs a person in with a code sent by email, when they
// turned that on (README.md). KIFF owns the setting, the codes and the
// emails; the sign-in provider still makes every session, from a one-time
// sign-in token KIFF asks it for once a code matches.
package codesignin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/mail"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/words"
)

const (
	// CodeTTL is how long a code works.
	CodeTTL = 10 * time.Minute
	// TicketTTL is how long the provider's one-time sign-in token lives:
	// the page uses it at once.
	TicketTTL = 60 * time.Second
	// UseKept is how long a code sign-in is kept for the step-up check: past
	// it, the session is too old to pass the check anyway.
	UseKept = 15 * time.Minute
	// CountsKept is how long the rate limits' counts are kept.
	CountsKept = 25 * time.Hour
	// KeyBytes is the least KIFF_SIGNIN_CODE_KEY holds.
	KeyBytes = 32
)

// The limits. Sends: per address, an hour and a day; per IP address, an
// hour. Verifies: per address and per IP address, an hour. Wrong codes, per
// person (so a new code doesn't reset them): an hour and a day.
const (
	sendAddrHour   = 3
	sendAddrDay    = 10
	sendIPHour     = 10
	verifyAddrHour = 10
	verifyIPHour   = 30
	wrongHour      = 5
	wrongDay       = 20
)

var (
	// ErrWrong: the code doesn't sign anyone in. The same for an unknown
	// address, an address with codes off, a lapsed or used code, and a
	// wrong one.
	ErrWrong = errors.New("codesignin: the code is wrong, used or lapsed")
	// ErrSlowDown: too many tries from this address or IP address.
	ErrSlowDown = errors.New("codesignin: too many tries")
	// ErrUnavailable: KIFF couldn't finish; nothing was signed in.
	ErrUnavailable = errors.New("codesignin: unavailable")
	// ErrBadRequest: the request isn't one (an address that isn't one).
	ErrBadRequest = errors.New("codesignin: bad request")
	// ErrSecondFactor: the person signs in with a second factor, which a
	// code never stands in for.
	ErrSecondFactor = errors.New("codesignin: the person signs in with a second factor")
)

// People is the sign-in provider's side.
type People interface {
	// ByVerifiedEmail is the person one of whose verified addresses is
	// address; found is false for no one, or an unverified address.
	ByVerifiedEmail(ctx context.Context, address string) (subject string, found bool, err error)
	// VerifiedEmails are the person's verified addresses.
	VerifiedEmails(ctx context.Context, subject string) ([]string, error)
	// Ticket is a one-time sign-in token for the person, living ttl.
	Ticket(ctx context.Context, subject string, ttl time.Duration) (string, error)
	// SecondFactor reports whether the person signs in with a second
	// factor. A code never stands in for one, so such a person can't use
	// codes: whether the provider's token would skip it isn't KIFF's to
	// rely on.
	SecondFactor(ctx context.Context, subject string) (bool, error)
}

// Asked is where a request came from, for the email: the browser and the
// network's guess of the country, as the web read them.
type Asked = notify.CodeAsked

// Mailer sends the three emails.
type Mailer interface {
	SignInCode(ctx context.Context, to, code string, lang words.Lang, asked Asked) error
	SignInCodeOff(ctx context.Context, to string, lang words.Lang, asked Asked) error
	SignInCodeChanged(ctx context.Context, to string, on bool, lang words.Lang, at time.Time) error
}

// Service is code sign-in. Start runs its sender.
type Service struct {
	Store  Store
	People People
	Mail   Mailer
	Logger *slog.Logger
	Now    func() time.Time

	codeKey, hashKey []byte
	jobs             chan job
	start            sync.Once
	lastSweep        time.Time
	// sent, when set, is called after each job (tests).
	sent func()
}

// New is the service with key (KIFF_SIGNIN_CODE_KEY): the codes' HMAC and
// the counts' hashes each use their own key drawn from it.
func New(key []byte, store Store, people People, mailer Mailer, logger *slog.Logger) (*Service, error) {
	if len(key) < KeyBytes {
		return nil, fmt.Errorf("codesignin: the key needs %d bytes or more", KeyBytes)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{Store: store, People: people, Mail: mailer, Logger: logger,
		codeKey: derive(key, "kiff code sign-in: codes"), hashKey: derive(key, "kiff code sign-in: lookups"),
		jobs: make(chan job, 256)}, nil
}

// ParseKey reads KIFF_SIGNIN_CODE_KEY: hex or base64, at least KeyBytes.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, dec := range []func(string) ([]byte, error){hex.DecodeString, base64.StdEncoding.DecodeString, base64.RawURLEncoding.DecodeString} {
		if b, err := dec(s); err == nil && len(b) >= KeyBytes {
			return b, nil
		}
	}
	return nil, fmt.Errorf("codesignin: KIFF_SIGNIN_CODE_KEY is not %d random bytes in hex or base64", KeyBytes)
}

func derive(key []byte, purpose string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose))
	return m.Sum(nil)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// hash is a keyed hash of a value of a kind: what the counts and codes keep
// in place of an address, an IP address, a flow or a person.
func (s *Service) hash(kind, value string) string {
	m := hmac.New(sha256.New, s.hashKey)
	m.Write([]byte(kind + "\x00" + value))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:24])
}

// mac is the code's HMAC, bound to its id and its person.
func (s *Service) mac(id, subject, code string) []byte {
	m := hmac.New(sha256.New, s.codeKey)
	m.Write([]byte(id + "\x00" + subject + "\x00" + code))
	return m.Sum(nil)
}

// Address is a request's address, trimmed and lower-cased, or "" when it
// isn't one.
func Address(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 254 || strings.ContainsAny(s, " <>\"") {
		return ""
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s || !strings.Contains(s[strings.LastIndex(s, "@")+1:], ".") {
		return ""
	}
	return s
}

// ipGroup is the IP address a limit counts: an IPv6 address by its /64,
// which one connection can rotate through.
func ipGroup(ip string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "unknown"
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

type counter struct {
	key   string
	day   bool
	limit int
}

func window(now time.Time, day bool) time.Time {
	if day {
		return now.UTC().Truncate(24 * time.Hour)
	}
	return now.UTC().Truncate(time.Hour)
}

// add counts one more against each counter and reports whether any is past
// its limit. A count that can't be kept is past it: no limit, no send.
func (s *Service) add(ctx context.Context, now time.Time, cs ...counter) bool {
	over := false
	for _, c := range cs {
		n, err := s.Store.Add(ctx, c.key, window(now, c.day))
		if err != nil {
			s.Logger.WarnContext(ctx, "code sign-in: count failed", slog.String("err", err.Error()))
			over = true
		} else if n > c.limit {
			over = true
		}
	}
	return over
}

// SendRequest is a browser asking for a code.
type SendRequest struct {
	Email string
	// Flow is the browser's own id for this sign-in (the web's sealed
	// cookie): a code works only with it.
	Flow string
	IP   string
	Lang words.Lang
	Asked
}

type job struct {
	address, addrHash, flowHash string
	over                        bool
	lang                        words.Lang
	asked                       Asked
}

// Send asks for a code for an address. It does the same work whatever the
// address (the hashes, the counts, one job queued) and answers before
// anything is looked up, so its answer and its time say nothing of who has
// an account or codes on. The job sends a code to a person with codes on,
// "codes aren't on" to a person with them off, and nothing to an address no
// one has verified.
func (s *Service) Send(ctx context.Context, r SendRequest) error {
	address := Address(r.Email)
	if address == "" || strings.TrimSpace(r.Flow) == "" {
		return ErrBadRequest
	}
	s.start.Do(s.run)
	now := s.now()
	ah, ih := s.hash("address", address), s.hash("ip", ipGroup(r.IP))
	over := s.add(ctx, now,
		counter{"send-address-h:" + ah, false, sendAddrHour},
		counter{"send-address-d:" + ah, true, sendAddrDay},
		counter{"send-ip-h:" + ih, false, sendIPHour})
	r.Asked.At = now
	j := job{address: address, addrHash: ah, flowHash: s.hash("flow", r.Flow), over: over, lang: r.Lang, asked: r.Asked}
	select {
	case s.jobs <- j:
	default:
		s.Logger.WarnContext(ctx, "code sign-in: send queue full; dropped", slog.String("address", ah[:8]))
	}
	return nil
}

func (s *Service) run() {
	go func() {
		for j := range s.jobs {
			s.do(j)
			if s.sent != nil {
				s.sent()
			}
		}
	}()
}

// do is one queued send, after the answer.
func (s *Service) do(j job) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := s.now()
	s.sweep(ctx, now)
	// Never the address: its hash's start says which request it was.
	log := s.Logger.With(slog.String("address", j.addrHash[:8]))
	if j.over {
		log.InfoContext(ctx, "code sign-in: send refused, past a limit")
		return
	}
	subject, found, err := s.People.ByVerifiedEmail(ctx, j.address)
	if err != nil {
		log.WarnContext(ctx, "code sign-in: lookup failed", slog.String("err", err.Error()))
		return
	}
	if !found {
		return
	}
	set, err := s.Store.Setting(ctx, subject)
	if err != nil {
		log.WarnContext(ctx, "code sign-in: read setting failed", slog.String("err", err.Error()))
		return
	}
	if !set.EmailCode {
		if err := s.Mail.SignInCodeOff(ctx, j.address, j.lang, j.asked); err != nil {
			log.WarnContext(ctx, "code sign-in: off email failed", slog.String("err", err.Error()))
		}
		return
	}
	code, err := newCode()
	if err != nil {
		log.ErrorContext(ctx, "code sign-in: no randomness", slog.String("err", err.Error()))
		return
	}
	id := rand.Text()
	c := Code{ID: id, Flow: j.flowHash, Address: j.addrHash, Subject: subject, MAC: s.mac(id, subject, code), ExpiresAt: now.Add(CodeTTL)}
	if err := s.Store.PutCode(ctx, c); err != nil {
		log.WarnContext(ctx, "code sign-in: keep code failed", slog.String("err", err.Error()))
		return
	}
	if err := s.Mail.SignInCode(ctx, j.address, code, j.lang, j.asked); err != nil {
		log.WarnContext(ctx, "code sign-in: code email failed", slog.String("err", err.Error()))
	}
}

func (s *Service) sweep(ctx context.Context, now time.Time) {
	if now.Sub(s.lastSweep) < time.Minute {
		return
	}
	s.lastSweep = now
	if err := s.Store.Sweep(ctx, now); err != nil {
		s.Logger.WarnContext(ctx, "code sign-in: sweep failed", slog.String("err", err.Error()))
	}
}

// newCode is six random digits.
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// digits is the code as typed, without spaces or dashes.
func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == ' ':
		default:
			return ""
		}
	}
	return b.String()
}

// VerifyRequest is a browser entering a code.
type VerifyRequest struct {
	Email, Flow, Code, IP string
}

// Verify checks a code and answers the provider's one-time sign-in token
// for its person. It does the same work for an address with no code as for
// a wrong code, and its errors don't say which.
func (s *Service) Verify(ctx context.Context, r VerifyRequest) (string, error) {
	address := Address(r.Email)
	if address == "" || strings.TrimSpace(r.Flow) == "" {
		return "", ErrBadRequest
	}
	now := s.now()
	ah, ih := s.hash("address", address), s.hash("ip", ipGroup(r.IP))
	if s.add(ctx, now, counter{"verify-address-h:" + ah, false, verifyAddrHour}, counter{"verify-ip-h:" + ih, false, verifyIPHour}) {
		return "", ErrSlowDown
	}
	code := digits(r.Code)
	c, found, err := s.Store.FindCode(ctx, s.hash("flow", r.Flow), ah, now)
	if err != nil {
		s.Logger.WarnContext(ctx, "code sign-in: find code failed", slog.String("err", err.Error()))
		return "", ErrUnavailable
	}
	// Who the wrong tries count against: the person, so a new code doesn't
	// reset them; with no code, the address, for the same work.
	who := "address:" + ah
	if found {
		who = "person:" + s.hash("person", c.Subject)
	}
	wrongH, errH := s.Store.Count(ctx, "wrong-h:"+who, window(now, false))
	wrongD, errD := s.Store.Count(ctx, "wrong-d:"+who, window(now, true))
	if errH != nil || errD != nil {
		return "", ErrUnavailable
	}
	want := c.MAC
	if !found {
		want = make([]byte, sha256.Size)
	}
	match := hmac.Equal(want, s.mac(c.ID, c.Subject, code)) && found && len(code) == 6
	if wrongH >= wrongHour || wrongD >= wrongDay {
		// Past the person's wrong tries: their codes end, the right one too.
		if found {
			_ = s.Store.DropCodes(ctx, c.Subject)
		}
		return "", ErrWrong
	}
	if !match {
		if s.add(ctx, now, counter{"wrong-h:" + who, false, wrongHour - 1}, counter{"wrong-d:" + who, true, wrongDay - 1}) && found {
			_ = s.Store.DropCodes(ctx, c.Subject)
		}
		return "", ErrWrong
	}
	used, err := s.Store.UseCode(ctx, c.ID, now)
	if err != nil {
		return "", ErrUnavailable
	}
	if !used {
		return "", ErrWrong
	}
	// The person from KIFF's record of the code, never the request: still
	// with codes on, and still the one whose verified address this is.
	set, err := s.Store.Setting(ctx, c.Subject)
	if err != nil {
		return "", ErrUnavailable
	}
	if !set.EmailCode {
		return "", ErrWrong
	}
	subject, found, err := s.People.ByVerifiedEmail(ctx, address)
	if err != nil {
		return "", ErrUnavailable
	}
	if !found || subject != c.Subject {
		return "", ErrWrong
	}
	// A second factor is never skipped: such a person signs in another way.
	if two, err := s.People.SecondFactor(ctx, c.Subject); err != nil {
		return "", ErrUnavailable
	} else if two {
		return "", ErrSecondFactor
	}
	// Noted before the token exists: a session it makes is never a recent
	// sign-in for the changes that need one.
	if err := s.Store.NoteSignIn(ctx, c.Subject, now); err != nil {
		return "", ErrUnavailable
	}
	ticket, err := s.People.Ticket(ctx, c.Subject, TicketTTL)
	if err != nil || ticket == "" {
		s.Logger.WarnContext(ctx, "code sign-in: ticket failed", slog.Any("err", err))
		return "", ErrUnavailable
	}
	return ticket, nil
}

// SignedInWithCode is when the person last signed in with a code, if within
// UseKept: auth.StepUp's CodeSignedIn.
func (s *Service) SignedInWithCode(ctx context.Context, subject string) (time.Time, bool, error) {
	at, ok, err := s.Store.LastSignIn(ctx, subject)
	if err != nil || !ok || s.now().Sub(at) > UseKept {
		return time.Time{}, false, err
	}
	return at, true, nil
}

// SetEmailCode turns codes on or off for a person and tells each of their
// verified addresses. The caller checked the recent sign-in for on.
func (s *Service) SetEmailCode(ctx context.Context, subject string, on bool, lang words.Lang) (Setting, error) {
	now := s.now()
	old, err := s.Store.Setting(ctx, subject)
	if err != nil {
		return Setting{}, err
	}
	if old.EmailCode == on && !old.ChangedAt.IsZero() {
		return old, nil
	}
	set := Setting{EmailCode: on, ChangedAt: now}
	if err := s.Store.SetSetting(ctx, subject, set); err != nil {
		return Setting{}, err
	}
	if !on {
		if err := s.Store.DropCodes(ctx, subject); err != nil {
			s.Logger.WarnContext(ctx, "code sign-in: drop codes failed", slog.String("err", err.Error()))
		}
	}
	if old.EmailCode == on {
		// First write of the default: nothing changed for the person.
		return set, nil
	}
	go s.tellChanged(subject, on, lang, now)
	return set, nil
}

func (s *Service) tellChanged(subject string, on bool, lang words.Lang, at time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addrs, err := s.People.VerifiedEmails(ctx, subject)
	if err != nil {
		s.Logger.WarnContext(ctx, "code sign-in: changed email: lookup failed", slog.String("err", err.Error()))
		return
	}
	for _, a := range addrs {
		if err := s.Mail.SignInCodeChanged(ctx, a, on, lang, at); err != nil {
			s.Logger.WarnContext(ctx, "code sign-in: changed email failed", slog.String("err", err.Error()))
		}
	}
	if s.sent != nil {
		s.sent()
	}
}
