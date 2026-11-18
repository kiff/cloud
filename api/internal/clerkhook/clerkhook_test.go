package clerkhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"testing"
	"time"
)

const testSecret = "whsec_dGVzdC1zZWNyZXQtZm9yLWtpZmY=" // a fake key, for tests only

func sign(t *testing.T, id string, ts int64, body []byte) string {
	t.Helper()
	key, _ := base64.StdEncoding.DecodeString("dGVzdC1zZWNyZXQtZm9yLWtpZmY=")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + strconv.FormatInt(ts, 10) + "." + string(body)))
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// A webhook checks only with its own signature, body, id and a fresh
// timestamp; any change, or a replay after the window, is refused.
func TestVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"type":"session.created","data":{"id":"sess_1"}}`)
	ts := now.Unix()
	good := sign(t, "msg_1", ts, body)
	if err := Verify(testSecret, "msg_1", strconv.FormatInt(ts, 10), "v1,AAAA "+good, body, now); err != nil {
		t.Fatalf("a good webhook, among other signatures: %v", err)
	}
	for name, c := range map[string]struct {
		secret, id, ts, sig string
		body                []byte
		now                 time.Time
	}{
		"another body":           {testSecret, "msg_1", strconv.FormatInt(ts, 10), good, []byte(`{"type":"session.created","data":{"id":"sess_2"}}`), now},
		"another id":             {testSecret, "msg_2", strconv.FormatInt(ts, 10), good, body, now},
		"another secret":         {"whsec_b3RoZXI=", "msg_1", strconv.FormatInt(ts, 10), good, body, now},
		"a replay, later":        {testSecret, "msg_1", strconv.FormatInt(ts, 10), good, body, now.Add(6 * time.Minute)},
		"from the future":        {testSecret, "msg_1", strconv.FormatInt(ts, 10), good, body, now.Add(-6 * time.Minute)},
		"no signature":           {testSecret, "msg_1", strconv.FormatInt(ts, 10), "", body, now},
		"another version":        {testSecret, "msg_1", strconv.FormatInt(ts, 10), "v2," + good[3:], body, now},
		"no secret":              {"", "msg_1", strconv.FormatInt(ts, 10), good, body, now},
		"a timestamp that isn't": {testSecret, "msg_1", "soon", good, body, now},
	} {
		if err := Verify(c.secret, c.id, c.ts, c.sig, c.body, c.now); !errors.Is(err, ErrSignature) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// session.created gives its session, client, user and when the account
// was made; another event, nothing to act on.
func TestParse(t *testing.T) {
	e, err := Parse([]byte(`{"type":"session.created","data":{"id":"sess_1","user_id":"user_1","client_id":"client_1","created_at":1800000000000,
"user":{"id":"user_1","created_at":1799999943000}}}`))
	if err != nil || e.Session.ClientID != "client_1" || !e.Session.At(time.Time{}).Equal(time.UnixMilli(1800000000000)) {
		t.Fatalf("%+v %v", e, err)
	}
	if e, err := Parse([]byte(`{"type":"user.updated","data":{}}`)); err != nil || e.Type != "user.updated" {
		t.Errorf("another event: %+v %v", e, err)
	}
	if _, err := Parse([]byte(`{"type":"session.created","data":{"id":"sess_1","user_id":"user_1"}}`)); err == nil {
		t.Error("a session with no client was read")
	}
}

// An account's first sign-in comes with its sign-up; a later one, or one
// that doesn't say when the account was made, isn't its first.
func TestNewAccount(t *testing.T) {
	const at = int64(1800000000000)
	for name, c := range map[string]struct {
		made        int64
		want, known bool
	}{
		"signed up a minute before": {at - 57_000, true, true},
		"made a day before":         {at - 24*3600_000, false, true},
		"made 16 minutes before":    {at - 16*60_000, false, true},
		"doesn't say":               {0, false, false},
	} {
		s := Session{CreatedAt: at}
		s.User.CreatedAt = c.made
		if got, known := s.NewAccount(); got != c.want || known != c.known {
			t.Errorf("%s: %v %v", name, got, known)
		}
	}
}
