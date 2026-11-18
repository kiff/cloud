package newdevice

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

const testSecret = "whsec_dGVzdC1zZWNyZXQtZm9yLWtpZmY=" // a fake key, for tests only

// memStore is Store in memory, with the same rules as Postgres.
type memStore struct {
	mu      sync.Mutex
	devices map[string]time.Time // subject|hash → last seen
	rows    map[string]*row
}

type row struct {
	Due
	newDevice  bool
	due, until time.Time
}

func newMem() *memStore { return &memStore{devices: map[string]time.Time{}, rows: map[string]*row{}} }

func (m *memStore) Seen(_ context.Context, s SignIn, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := s.Subject + "|" + string(s.ClientHash)
	_, known := m.devices[key]
	other := false
	for k := range m.devices {
		if strings.HasPrefix(k, s.Subject+"|") && k != key {
			other = true
		}
	}
	m.devices[key] = now
	if known || (!other && s.NewAccount) {
		return false, nil
	}
	r := m.rows[s.SessionID]
	if r == nil {
		r = &row{until: now.Add(Window)}
		m.rows[s.SessionID] = r
	} else if r.Subject != s.Subject {
		return true, nil
	}
	tafs := r.Tafs
	r.Due = Due{SessionID: s.SessionID, Subject: s.Subject, At: s.At, Tafs: tafs, Until: r.until}
	r.newDevice, r.due = true, now.Add(Hold)
	return true, nil
}

func (m *memStore) Through(_ context.Context, sessionID, subject string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.rows[sessionID]; r != nil {
		if r.Subject == subject {
			r.Tafs = true
		}
		return nil
	}
	m.rows[sessionID] = &row{Due: Due{SessionID: sessionID, Subject: subject, Tafs: true, Until: now.Add(Window)}, until: now.Add(Window)}
	return nil
}

func (m *memStore) Again(_ context.Context, d Due, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if at.After(d.Until) || m.rows[d.SessionID] != nil {
		return nil
	}
	m.rows[d.SessionID] = &row{Due: d, newDevice: true, due: at, until: d.Until}
	return nil
}

func (m *memStore) TakeDue(_ context.Context, now time.Time) ([]Due, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Due
	for id, r := range m.rows {
		switch {
		case now.After(r.until):
			delete(m.rows, id)
		case r.newDevice && !now.Before(r.due):
			out = append(out, r.Due)
			delete(m.rows, id)
		}
	}
	for k, seen := range m.devices {
		if seen.Before(now.Add(-Unseen)) {
			delete(m.devices, k)
		}
	}
	return out, nil
}

type sent struct {
	to string
	d  notify.NewDevice
}

type fakeMailer struct {
	got  []sent
	fail int // the next sends that fail
}

func (f *fakeMailer) NewDeviceSignedIn(_ context.Context, to string, d notify.NewDevice) error {
	if f.fail > 0 {
		f.fail--
		return errors.New("the mail provider is down")
	}
	f.got = append(f.got, sent{to, d})
	return nil
}

type emails map[string]string

func (e emails) EmailForSubject(_ context.Context, s string) (string, error) { return e[s], nil }

type subjects map[string]string

func (m subjects) Get(_ context.Context, s string) (tenant.SubjectMapping, error) {
	t, ok := m[s]
	if !ok {
		return tenant.SubjectMapping{}, tenant.ErrSubjectNotMapped
	}
	return tenant.SubjectMapping{Subject: s, TenantID: t}, nil
}

type tenants map[string]tenant.Tenant

func (m tenants) Get(_ context.Context, id string) (tenant.Tenant, error) { return m[id], nil }

// describer is the provider's description of each session.
type describer struct {
	got  map[string]Description
	fail bool
}

func (f *describer) Describe(_ context.Context, sessionID, _ string) (Description, error) {
	if f.fail {
		return Description{}, errors.New("the provider is down")
	}
	return f.got[sessionID], nil
}

// accounts is when the provider made each account.
type accounts struct {
	made map[string]time.Time
	fail bool
}

func (a accounts) AccountMade(_ context.Context, subject string) (time.Time, error) {
	if a.fail {
		return time.Time{}, errors.New("the provider is down")
	}
	return a.made[subject], nil
}

type world struct {
	svc    *Service
	store  *memStore
	mailer *fakeMailer
	desc   *describer
	now    time.Time
}

func newWorld() *world {
	w := &world{store: newMem(), mailer: &fakeMailer{}, desc: &describer{got: map[string]Description{}}, now: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	w.svc = &Service{Store: w.store, Mailer: w.mailer, Emails: emails{"user_ana": "ana@example.com"}, Describer: w.desc,
		Subjects: subjects{"user_ana": "t_acme"},
		Tenants: tenants{"t_acme": {ID: "t_acme", Metadata: map[string]any{personlang.MetadataKey: map[string]any{"people": map[string]any{"user_ana": "fr"}},
			"time_zone": "Europe/Paris"}}},
		Secret: testSecret, SiteURL: "https://kiff.dev", SessionsURL: "https://accounts.kiff.dev/user", Now: func() time.Time { return w.now }}
	return w
}

// sessionBody is a session.created of an account made long before.
func sessionBody(session, user, client string) string {
	return sessionMade(session, user, client, 1700000000000)
}

// sessionMade is a session.created of an account made at made (ms), as
// Clerk sends it: no device and no place.
func sessionMade(session, user, client string, made int64) string {
	return `{"type":"session.created","data":{"id":"` + session + `","user_id":"` + user + `","client_id":"` + client +
		`","created_at":1793955600000,"user":{"id":"` + user + `","created_at":` + strconv.FormatInt(made, 10) + `}}}`
}

// signUp is an account's first sign-in, a minute after it was made.
func signUp(session, user, client string) string {
	return sessionMade(session, user, client, 1793955600000-60_000)
}

func (w *world) hook(t *testing.T, body string, signed bool) int {
	t.Helper()
	ts := strconv.FormatInt(w.now.Unix(), 10)
	key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(testSecret, "whsec_"))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("msg_" + ts + "." + ts + "." + body))
	sig := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !signed {
		sig = "v1,AAAA"
	}
	r := httptest.NewRequest(http.MethodPost, "/webhooks/clerk", strings.NewReader(body))
	r.Header.Set("svix-id", "msg_"+ts)
	r.Header.Set("svix-timestamp", ts)
	r.Header.Set("svix-signature", sig)
	rec := httptest.NewRecorder()
	w.svc.ServeHTTP(rec, r)
	return rec.Code
}

func (w *world) after(d time.Duration) { w.now = w.now.Add(d) }

// An account's first sign-in is recorded, not emailed; a new device is,
// two minutes later, in the person's language and the account's time zone,
// once, with what the provider says of it as the email goes.
func TestANewDeviceIsEmailedOnceInThePersonsLanguage(t *testing.T) {
	w := newWorld()
	w.desc.got["sess_3"] = Description{Device: "Android", Browser: "Chrome", City: "Lyon", Country: "FR", Method: "password", Password: true}
	if code := w.hook(t, signUp("sess_1", "user_ana", "client_laptop"), true); code != http.StatusNoContent {
		t.Fatalf("first sign-in: %d", code)
	}
	w.after(Hold)
	w.svc.Send(context.Background())
	if len(w.mailer.got) != 0 {
		t.Fatal("an account's first sign-in was emailed")
	}
	// The same device again is known.
	w.hook(t, sessionBody("sess_2", "user_ana", "client_laptop"), true)
	w.after(Hold)
	w.svc.Send(context.Background())
	if len(w.mailer.got) != 0 {
		t.Fatal("a known device was emailed")
	}
	// A new one waits two minutes, then goes once.
	w.hook(t, sessionBody("sess_3", "user_ana", "client_phone"), true)
	w.after(Hold - time.Second)
	w.svc.Send(context.Background())
	if len(w.mailer.got) != 0 {
		t.Fatal("the email went before the hold")
	}
	w.after(time.Second)
	w.svc.Send(context.Background())
	w.svc.Send(context.Background())
	if len(w.mailer.got) != 1 {
		t.Fatalf("emails sent: %d, want 1", len(w.mailer.got))
	}
	got := w.mailer.got[0]
	if got.to != "ana@example.com" || got.d.Lang != words.FR || got.d.Tafs || got.d.Browser != "Chrome" || got.d.Device != "Android" ||
		got.d.City != "Lyon" || got.d.Country != "FR" || got.d.Method != "password" || !got.d.Password || got.d.Zone == nil ||
		got.d.Zone.String() != "Europe/Paris" || got.d.Key != "new-device/sess_3" || got.d.SiteURL != "https://kiff.dev" || got.d.SessionsURL == "" {
		t.Fatalf("the email: %+v", got)
	}
	// The provider sending the same event again sends nothing more.
	w.hook(t, sessionBody("sess_3", "user_ana", "client_phone"), true)
	w.after(Hold)
	w.svc.Send(context.Background())
	if len(w.mailer.got) != 1 {
		t.Fatal("a retried webhook sent the email again")
	}
}

// A device approval in the tafs app from the same sign-in, before the
// webhook or after it, makes it a "tafs by KIFF" email; another person's
// approval marks nothing.
func TestADeviceApprovalInTafsMarksTheEmail(t *testing.T) {
	for _, first := range []string{"webhook", "approval"} {
		t.Run(first, func(t *testing.T) {
			w := newWorld()
			w.hook(t, signUp("sess_1", "user_ana", "client_laptop"), true)
			if first == "approval" {
				w.svc.Through(context.Background(), "sess_2", "user_ana")
				w.hook(t, sessionBody("sess_2", "user_ana", "client_phone"), true)
			} else {
				w.hook(t, sessionBody("sess_2", "user_ana", "client_phone"), true)
				w.svc.Through(context.Background(), "sess_2", "user_bob")
				w.svc.Through(context.Background(), "sess_2", "user_ana")
			}
			w.after(Hold)
			w.svc.Send(context.Background())
			if len(w.mailer.got) != 1 || !w.mailer.got[0].d.Tafs {
				t.Fatalf("emails: %+v", w.mailer.got)
			}
		})
	}
}

// A marker no new-device sign-in followed, and a device unseen for 180
// days, go on their own.
func TestWaitingRowsExpireAndUnseenDevicesArePruned(t *testing.T) {
	w := newWorld()
	w.svc.Through(context.Background(), "sess_x", "user_ana")
	w.hook(t, sessionBody("sess_1", "user_ana", "client_laptop"), true)
	w.after(Window + time.Second)
	w.svc.Send(context.Background())
	if len(w.store.rows) != 0 {
		t.Fatalf("waiting rows left after the window: %d", len(w.store.rows))
	}
	w.after(Unseen)
	w.svc.Send(context.Background())
	if len(w.store.devices) != 0 {
		t.Fatal("a device unseen for 180 days was kept")
	}
	// Forgotten, the laptop is a device KIFF doesn't know on an older
	// account: emailed.
	w.hook(t, sessionBody("sess_2", "user_ana", "client_laptop"), true)
	w.after(Hold)
	w.svc.Send(context.Background())
	if len(w.mailer.got) != 1 {
		t.Fatalf("a device unseen for 180 days: %d emails, want 1", len(w.mailer.got))
	}
}

// "First sign-in ever" is no device on record AND the account made with
// this sign-in. A person with no device on record and an older account is
// emailed: their first device KIFF sees may not be theirs.
func TestOnlyAnAccountsFirstSignInIsQuiet(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"signed up with this sign-in":    {signUp("sess_1", "user_ana", "client_laptop"), 0},
		"no device on record, older":     {sessionBody("sess_1", "user_ana", "client_laptop"), 1},
		"account made 16 minutes before": {sessionMade("sess_1", "user_ana", "client_laptop", 1793955600000-16*60_000), 1},
		"the event doesn't say its age":  {`{"type":"session.created","data":{"id":"sess_1","user_id":"user_ana","client_id":"client_laptop","created_at":1793955600000}}`, 1},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			w.hook(t, c.body, true)
			w.after(Hold)
			w.svc.Send(context.Background())
			if len(w.mailer.got) != c.want {
				t.Fatalf("emails: %d, want %d", len(w.mailer.got), c.want)
			}
		})
	}
}

// An event that doesn't say when the account was made: the provider says.
// A sign-up is quiet; an older account, or a provider that can't be read,
// is emailed.
func TestTheProviderSaysWhenTheEventDoesnt(t *testing.T) {
	const noUser = `{"type":"session.created","data":{"id":"sess_1","user_id":"user_ana","client_id":"client_laptop","created_at":1793955600000}}`
	signedIn := time.UnixMilli(1793955600000)
	for name, c := range map[string]struct {
		a    accounts
		want int
	}{
		"made a minute before": {accounts{made: map[string]time.Time{"user_ana": signedIn.Add(-time.Minute)}}, 0},
		"made a year before":   {accounts{made: map[string]time.Time{"user_ana": signedIn.AddDate(-1, 0, 0)}}, 1},
		"can't be read":        {accounts{fail: true}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			w.svc.Accounts = c.a
			w.hook(t, noUser, true)
			w.after(Hold)
			w.svc.Send(context.Background())
			if len(w.mailer.got) != c.want {
				t.Fatalf("emails: %d, want %d", len(w.mailer.got), c.want)
			}
		})
	}
}

// Without the provider's description the email still goes, with the time
// alone; without the account, in English and UTC.
func TestTheEmailGoesWithoutItsDetails(t *testing.T) {
	w := newWorld()
	w.desc.fail = true
	w.hook(t, signUp("sess_1", "user_ana", "client_laptop"), true)
	w.hook(t, sessionBody("sess_2", "user_ana", "client_phone"), true)
	w.after(Hold)
	w.svc.Send(context.Background())
	if len(w.mailer.got) != 1 {
		t.Fatalf("emails: %d", len(w.mailer.got))
	}
	if d := w.mailer.got[0].d; d.Device != "" || d.Browser != "" || d.City != "" || d.Method != "" || d.Password || d.At.IsZero() {
		t.Fatalf("the email: %+v", d)
	}
	if lang, zone := w.svc.account(context.Background(), "user_nobody"); lang != words.EN || zone != time.UTC {
		t.Fatalf("no account: %s %s", lang, zone)
	}
}

// Only a signed event counts; other events are acknowledged and dropped.
func TestTheWebhookTakesOnlySignedSessionEvents(t *testing.T) {
	w := newWorld()
	if code := w.hook(t, sessionBody("sess_1", "user_ana", "client_laptop"), false); code != http.StatusUnauthorized {
		t.Fatalf("unsigned: %d", code)
	}
	if len(w.store.devices) != 0 {
		t.Fatal("an unsigned event was recorded")
	}
	if code := w.hook(t, `{"type":"user.updated","data":{"id":"user_ana"}}`, true); code != http.StatusNoContent {
		t.Fatalf("another event: %d", code)
	}
	if code := w.hook(t, `{"type":"session.created","data":{"id":"sess_1"}}`, true); code != http.StatusBadRequest {
		t.Fatalf("a session without its user and client: %d", code)
	}
}

// A send that fails is tried again a minute later, until the window
// closes: a passing error never loses the notice, and it still goes once.
func TestAFailedSendIsTriedAgainWithinTheWindow(t *testing.T) {
	w := newWorld()
	w.hook(t, signUp("sess_1", "user_ana", "client_laptop"), true)
	w.hook(t, sessionBody("sess_2", "user_ana", "client_phone"), true)
	w.mailer.fail = 2
	for i := 0; i < 2; i++ {
		w.after(Hold)
		w.svc.Send(context.Background())
	}
	if len(w.mailer.got) != 0 {
		t.Fatal("a failing send counted as sent")
	}
	w.after(RetryAfter)
	w.svc.Send(context.Background())
	w.after(RetryAfter)
	w.svc.Send(context.Background())
	if len(w.mailer.got) != 1 {
		t.Fatalf("emails sent after the provider came back: %d, want 1", len(w.mailer.got))
	}

	// Failing past the window, it stops.
	w = newWorld()
	w.hook(t, signUp("sess_1", "user_ana", "client_laptop"), true)
	w.hook(t, sessionBody("sess_2", "user_ana", "client_phone"), true)
	w.mailer.fail = 1000
	for i := 0; i < 20; i++ {
		w.after(RetryAfter)
		w.svc.Send(context.Background())
	}
	if len(w.store.rows) != 0 {
		t.Fatalf("rows still waiting after the window: %d", len(w.store.rows))
	}
}
