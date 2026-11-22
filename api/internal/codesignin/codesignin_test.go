package codesignin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/signintoken"

	"github.com/kiff/cloud/api/internal/words"
)

// people is the sign-in provider: verified addresses by person, and the
// tokens it gave.
type people struct {
	mu       sync.Mutex
	verified map[string]string // address → person
	extra    map[string][]string
	tickets  []string
	fail     error
	two      map[string]bool
}

func (p *people) SecondFactor(_ context.Context, s string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.two[s], nil
}

func (p *people) ByVerifiedEmail(_ context.Context, a string) (string, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return "", false, p.fail
	}
	s, ok := p.verified[a]
	return s, ok, nil
}

func (p *people) VerifiedEmails(_ context.Context, s string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for a, who := range p.verified {
		if who == s {
			out = append(out, a)
		}
	}
	return append(out, p.extra[s]...), nil
}

func (p *people) Ticket(_ context.Context, s string, ttl time.Duration) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ttl != TicketTTL {
		return "", errors.New("wrong ttl")
	}
	p.tickets = append(p.tickets, s)
	return "ticket-for-" + s, nil
}

type sent struct {
	kind, to, code string
	on             bool
}

// mailer keeps what was sent.
type mailer struct {
	mu   sync.Mutex
	list []sent
}

func (m *mailer) SignInCode(_ context.Context, to, code string, _ words.Lang, _ Asked) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.list = append(m.list, sent{kind: "code", to: to, code: code})
	return nil
}

func (m *mailer) SignInCodeOff(_ context.Context, to string, _ words.Lang, _ Asked) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.list = append(m.list, sent{kind: "off", to: to})
	return nil
}

func (m *mailer) SignInCodeChanged(_ context.Context, to string, on bool, _ words.Lang, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.list = append(m.list, sent{kind: "changed", to: to, on: on})
	return nil
}

func (m *mailer) all() []sent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sent(nil), m.list...)
}

type rig struct {
	t     *testing.T
	s     *Service
	store *Memory
	p     *people
	m     *mailer
	now   time.Time
	done  chan struct{}
	logs  *bytes.Buffer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, store: NewMemory(), m: &mailer{}, now: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
		p:    &people{verified: map[string]string{"on@example.com": "user_on", "off@example.com": "user_off"}},
		done: make(chan struct{}, 64), logs: &bytes.Buffer{}}
	s, err := New(bytes.Repeat([]byte{7}, KeyBytes), r.store, r.p, r.m, slog.New(slog.NewTextHandler(r.logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return r.now }
	s.sent = func() { r.done <- struct{}{} }
	r.s = s
	ctx := context.Background()
	if err := r.store.SetSetting(ctx, "user_on", Setting{EmailCode: true, ChangedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	return r
}

// send asks for a code and waits for its job.
func (r *rig) send(address, flow, ip string) {
	r.t.Helper()
	if err := r.s.Send(context.Background(), SendRequest{Email: address, Flow: flow, IP: ip}); err != nil {
		r.t.Fatalf("send %s: %v", address, err)
	}
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.t.Fatal("the send job never ran")
	}
}

// lastCode is the latest code mailed to address.
func (r *rig) lastCode(address string) string {
	r.t.Helper()
	for _, m := range slicesReverse(r.m.all()) {
		if m.kind == "code" && m.to == address {
			return m.code
		}
	}
	r.t.Fatalf("no code went to %s: %v", address, r.m.all())
	return ""
}

func slicesReverse(s []sent) []sent {
	out := make([]sent, len(s))
	for i := range s {
		out[len(s)-1-i] = s[i]
	}
	return out
}

func (r *rig) verify(address, flow, code, ip string) (string, error) {
	return r.s.Verify(context.Background(), VerifyRequest{Email: address, Flow: flow, Code: code, IP: ip})
}

func wrongOf(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

// The answer is the same for every address, and only a person with codes
// on gets one: one with them off gets "not on", and an address no one has
// verified gets nothing.
func TestSendAnswersTheSameAndMailsOnlyPeople(t *testing.T) {
	r := newRig(t)
	for _, a := range []string{"on@example.com", "off@example.com", "nobody@example.com", " ON@example.com "} {
		r.send(a, "flow-"+a, "198.51.100.7")
	}
	got := r.m.all()
	if len(got) != 3 || got[0].kind != "code" || got[0].to != "on@example.com" || got[1].kind != "off" || got[1].to != "off@example.com" ||
		got[2].kind != "code" || got[2].to != "on@example.com" {
		t.Fatalf("mails: %+v", got)
	}
	if len(got[0].code) != 6 || strings.Trim(got[0].code, "0123456789") != "" {
		t.Errorf("a code is six digits: %q", got[0].code)
	}
	if err := r.s.Send(context.Background(), SendRequest{Email: "not an address", Flow: "f"}); !errors.Is(err, ErrBadRequest) {
		t.Errorf("a bad address: %v", err)
	}
}

// An unverified address on a person's account never gets a code: the
// provider's lookup is by verified address only.
func TestAnUnverifiedAddressGetsNothing(t *testing.T) {
	r := newRig(t)
	r.p.extra = map[string][]string{"user_on": {"unverified@example.com"}}
	r.send("unverified@example.com", "f1", "198.51.100.7")
	if got := r.m.all(); len(got) != 0 {
		t.Fatalf("mailed: %+v", got)
	}
}

// A code works once, only in the browser that asked, and lapses after ten
// minutes.
func TestACodeWorksOnceInItsBrowserThenLapses(t *testing.T) {
	r := newRig(t)
	r.send("on@example.com", "flow-a", "198.51.100.7")
	code := r.lastCode("on@example.com")
	if _, err := r.verify("on@example.com", "flow-b", code, "198.51.100.7"); !errors.Is(err, ErrWrong) {
		t.Fatalf("another browser: %v", err)
	}
	ticket, err := r.verify("on@example.com", "flow-a", code[:3]+" "+code[3:], "198.51.100.7")
	if err != nil || ticket != "ticket-for-user_on" {
		t.Fatalf("the right code: %q %v", ticket, err)
	}
	if _, err := r.verify("on@example.com", "flow-a", code, "198.51.100.7"); !errors.Is(err, ErrWrong) {
		t.Fatalf("used twice: %v", err)
	}
	if at, ok, _ := r.s.SignedInWithCode(context.Background(), "user_on"); !ok || !at.Equal(r.now) {
		t.Errorf("the code sign-in isn't noted: %v %v", at, ok)
	}
	r.send("on@example.com", "flow-a", "198.51.100.7")
	code = r.lastCode("on@example.com")
	r.now = r.now.Add(CodeTTL + time.Second)
	if _, err := r.verify("on@example.com", "flow-a", code, "198.51.100.7"); !errors.Is(err, ErrWrong) {
		t.Fatalf("lapsed: %v", err)
	}
	if len(r.p.tickets) != 1 {
		t.Errorf("tickets: %v", r.p.tickets)
	}
}

// Five wrong codes end the person's codes, the right one too, and a new
// code doesn't start the count again.
func TestFiveWrongCodesEndThemAndANewCodeDoesntReset(t *testing.T) {
	r := newRig(t)
	r.send("on@example.com", "flow-a", "198.51.100.7")
	code := r.lastCode("on@example.com")
	for i := 0; i < wrongHour; i++ {
		if _, err := r.verify("on@example.com", "flow-a", wrongOf(code), "198.51.100.7"); !errors.Is(err, ErrWrong) {
			t.Fatalf("wrong %d: %v", i, err)
		}
	}
	if _, err := r.verify("on@example.com", "flow-a", code, "198.51.100.7"); !errors.Is(err, ErrWrong) {
		t.Fatalf("the right code after five wrong: %v", err)
	}
	r.send("on@example.com", "flow-c", "198.51.100.8")
	code = r.lastCode("on@example.com")
	if _, err := r.verify("on@example.com", "flow-c", code, "198.51.100.8"); !errors.Is(err, ErrWrong) {
		t.Fatalf("a new code reset the count: %v", err)
	}
	r.now = r.now.Add(time.Hour)
	r.send("on@example.com", "flow-d", "198.51.100.8")
	code = r.lastCode("on@example.com")
	if _, err := r.verify("on@example.com", "flow-d", code, "198.51.100.8"); err != nil {
		t.Fatalf("an hour later: %v", err)
	}
}

// Sends are limited per address and per IP address; past a limit the
// answer is the same and nothing goes.
func TestSendsAreLimitedPerAddressAndPerIP(t *testing.T) {
	r := newRig(t)
	for i := 0; i < sendAddrHour+1; i++ {
		r.send("on@example.com", "flow", "198.51.100.7")
	}
	if n := len(r.m.all()); n != sendAddrHour {
		t.Fatalf("per address: %d mails", n)
	}
	// Per IP: each send from another address in the same IPv6 /64.
	r3 := newRig(t)
	for i := 0; i < sendIPHour+1; i++ {
		r3.p.verified["p"+string(rune('a'+i))+"@example.com"] = "user_on"
		r3.send("p"+string(rune('a'+i))+"@example.com", "flow", fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	if n := len(r3.m.all()); n != sendIPHour {
		t.Fatalf("per IP /64: %d mails", n)
	}
}

// Verifies are limited per IP address.
func TestVerifiesAreLimitedPerIP(t *testing.T) {
	r := newRig(t)
	var err error
	for i := 0; i <= verifyIPHour; i++ {
		_, err = r.verify("x"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.com", "flow", "123456", "198.51.100.9")
	}
	if !errors.Is(err, ErrSlowDown) {
		t.Fatalf("past the IP limit: %v", err)
	}
}

// The token is only ever the person's whose code matched: an address that
// moved to someone else, or codes turned off, give none.
func TestTheTicketIsOnlyForThePersonWhoseCodeMatched(t *testing.T) {
	r := newRig(t)
	r.send("on@example.com", "flow-a", "198.51.100.7")
	code := r.lastCode("on@example.com")
	r.p.verified["on@example.com"] = "user_someone_else"
	if _, err := r.verify("on@example.com", "flow-a", code, "198.51.100.7"); !errors.Is(err, ErrWrong) {
		t.Fatalf("the address is someone else's now: %v", err)
	}
	r.p.verified["on@example.com"] = "user_on"
	r.send("on@example.com", "flow-a", "198.51.100.7")
	code = r.lastCode("on@example.com")
	if _, err := r.s.SetEmailCode(context.Background(), "user_on", false, words.EN); err != nil {
		t.Fatal(err)
	}
	<-r.done // the "turned off" emails
	if _, err := r.verify("on@example.com", "flow-a", code, "198.51.100.7"); !errors.Is(err, ErrWrong) {
		t.Fatalf("codes turned off: %v", err)
	}
	if len(r.p.tickets) != 0 {
		t.Errorf("tickets: %v", r.p.tickets)
	}
}

// Turning codes on or off tells every verified address of the person.
func TestAChangeTellsEveryVerifiedAddress(t *testing.T) {
	r := newRig(t)
	r.p.verified["second@example.com"] = "user_off"
	if _, err := r.s.SetEmailCode(context.Background(), "user_off", true, words.EN); err != nil {
		t.Fatal(err)
	}
	<-r.done
	to := map[string]bool{}
	for _, m := range r.m.all() {
		if m.kind == "changed" && m.on {
			to[m.to] = true
		}
	}
	if !to["off@example.com"] || !to["second@example.com"] || len(to) != 2 {
		t.Fatalf("told: %v", r.m.all())
	}
}

// Nothing secret is logged: no code, no address.
func TestNoCodeOrAddressIsLogged(t *testing.T) {
	r := newRig(t)
	for i := 0; i < sendAddrHour+1; i++ {
		r.send("on@example.com", "flow-a", "198.51.100.7")
	}
	code := r.lastCode("on@example.com")
	_, _ = r.verify("on@example.com", "flow-a", code, "198.51.100.7")
	logs := r.logs.String()
	if strings.Contains(logs, code) || strings.Contains(logs, "on@example.com") || strings.Contains(logs, "198.51.100.7") {
		t.Fatalf("a secret in the logs:\n%s", logs)
	}
	if !strings.Contains(logs, "past a limit") {
		t.Errorf("the refused send isn't logged:\n%s", logs)
	}
}

func TestTheKeyMustBeLongEnough(t *testing.T) {
	if _, err := New(make([]byte, KeyBytes-1), NewMemory(), &people{}, &mailer{}, nil); err == nil {
		t.Error("a short key")
	}
	if k, err := ParseKey(strings.Repeat("ab", KeyBytes)); err != nil || len(k) != KeyBytes {
		t.Errorf("hex: %v", err)
	}
	if _, err := ParseKey("short"); err == nil {
		t.Error("a short value")
	}
	if k, err := ParseKey(""); err != nil || k != nil {
		t.Error("unset is no key")
	}
}

// Wrong tries count per person, across all their addresses: five on one
// address end the codes sent to another.
func TestWrongTriesCountPerPersonAcrossAddresses(t *testing.T) {
	r := newRig(t)
	r.p.verified["second@example.com"] = "user_on"
	r.send("on@example.com", "flow-a", "198.51.100.7")
	code := r.lastCode("on@example.com")
	for i := 0; i < wrongHour; i++ {
		_, _ = r.verify("on@example.com", "flow-a", wrongOf(code), "198.51.100.7")
	}
	r.send("second@example.com", "flow-b", "198.51.100.8")
	other := r.lastCode("second@example.com")
	if _, err := r.verify("second@example.com", "flow-b", other, "198.51.100.8"); !errors.Is(err, ErrWrong) {
		t.Fatalf("the person's other address got fresh tries: %v", err)
	}
}

// Past the wrong-try limit, every code of the person is dropped.
func TestPastTheLimitThePersonsCodesAreDropped(t *testing.T) {
	r := newRig(t)
	r.send("on@example.com", "flow-a", "198.51.100.7")
	r.send("on@example.com", "flow-b", "198.51.100.7")
	code := r.lastCode("on@example.com")
	if r.store.Codes() != 2 {
		t.Fatalf("codes before: %d", r.store.Codes())
	}
	for i := 0; i < wrongHour; i++ {
		_, _ = r.verify("on@example.com", "flow-b", wrongOf(code), "198.51.100.7")
	}
	if n := r.store.Codes(); n != 0 {
		t.Fatalf("codes left past the limit: %d", n)
	}
}

// Codes turned off after the send refuse the code, even when the code is
// still in the store.
func TestCodesTurnedOffAfterTheSendRefuseIt(t *testing.T) {
	r := newRig(t)
	r.send("on@example.com", "flow-a", "198.51.100.7")
	code := r.lastCode("on@example.com")
	if err := r.store.SetSetting(context.Background(), "user_on", Setting{EmailCode: false, ChangedAt: r.now}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.verify("on@example.com", "flow-a", code, "198.51.100.7"); !errors.Is(err, ErrWrong) || len(r.p.tickets) != 0 {
		t.Fatalf("codes off: %v, tickets %v", err, r.p.tickets)
	}
}

// failNote can't record a code sign-in.
type failNote struct{ *Memory }

func (failNote) NoteSignIn(context.Context, string, time.Time) error { return errors.New("down") }

// The code sign-in is noted before the token exists: if it can't be, no
// token is made.
func TestNoTicketWithoutTheNote(t *testing.T) {
	r := newRig(t)
	r.s.Store = failNote{r.store}
	r.send("on@example.com", "flow-a", "198.51.100.7")
	code := r.lastCode("on@example.com")
	if _, err := r.verify("on@example.com", "flow-a", code, "198.51.100.7"); !errors.Is(err, ErrUnavailable) || len(r.p.tickets) != 0 {
		t.Fatalf("a token without the note: %v, tickets %v", err, r.p.tickets)
	}
}

// A code never stands in for a second factor: a person with one gets no
// token.
func TestASecondFactorIsNeverSkipped(t *testing.T) {
	r := newRig(t)
	r.p.two = map[string]bool{"user_on": true}
	r.send("on@example.com", "flow-a", "198.51.100.7")
	code := r.lastCode("on@example.com")
	if _, err := r.verify("on@example.com", "flow-a", code, "198.51.100.7"); !errors.Is(err, ErrSecondFactor) || len(r.p.tickets) != 0 {
		t.Fatalf("second factor: %v, tickets %v", err, r.p.tickets)
	}
}

// The provider's token must be for the matched person.
func TestTheProvidersTokenMustBeThePersons(t *testing.T) {
	c := &ClerkPeople{token: func(context.Context, *signintoken.CreateParams) (*clerk.SignInToken, error) {
		return &clerk.SignInToken{UserID: "user_other", Token: "tok"}, nil
	}}
	if tok, err := c.Ticket(context.Background(), "user_on", TicketTTL); err == nil || tok != "" {
		t.Fatalf("a token for someone else: %q %v", tok, err)
	}
	c.token = func(_ context.Context, p *signintoken.CreateParams) (*clerk.SignInToken, error) {
		return &clerk.SignInToken{UserID: *p.UserID, Token: "tok"}, nil
	}
	if tok, err := c.Ticket(context.Background(), "user_on", TicketTTL); err != nil || tok != "tok" {
		t.Fatalf("the person's token: %q %v", tok, err)
	}
}
