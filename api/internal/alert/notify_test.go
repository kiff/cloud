package alert

import (
	"encoding/json"
	"strings"
	"testing"
)

// A held call's alert names who to tell when KIFF knows who asked; an
// empty list tells no one, which a receiver must tell apart from no list
// at all (the receiver's own choice, as before).
func TestTheAlertNamesWhoToTell(t *testing.T) {
	d := &Dispatcher{}
	for _, c := range []struct {
		notify *[]string
		want   string
	}{
		{nil, ""},
		{&[]string{}, `"notify":[]`},
		{&[]string{"bo", "carol"}, `"notify":["bo","carol"]`},
	} {
		body, err := json.Marshal(d.buildPayload(Event{Outcome: "owner_required", Hold: &Hold{ID: "exc_1"}, Notify: c.notify}))
		if err != nil {
			t.Fatal(err)
		}
		if c.want == "" && strings.Contains(string(body), `"notify"`) || c.want != "" && !strings.Contains(string(body), c.want) {
			t.Errorf("notify %v: %s", c.notify, body)
		}
	}
}
