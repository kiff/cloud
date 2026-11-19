package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

// a review: extra room is typed in the limit's own terms. On a EUR
// limit at two decimals, 10 is 1000 stored, never 10 cents.
func TestParseRoomAmount(t *testing.T) {
	eur := apiclient.MandateLimit{Quantity: "sum(amount_cents)", Unit: "EUR", Scale: 2}
	count := apiclient.MandateLimit{Quantity: "count"}
	for _, tc := range []struct {
		raw  string
		l    apiclient.MandateLimit
		want int64
	}{
		{"10", eur, 1000}, {"12.5", eur, 1250}, {"12,50", eur, 1250}, {" 0.01 ", eur, 1}, {"3", count, 3},
		{"1", apiclient.MandateLimit{Quantity: "sum(amount_jpy)", Unit: "JPY"}, 1},
	} {
		if got, err := ParseRoomAmount(tc.raw, tc.l); err != nil || got != tc.want {
			t.Errorf("%q: %d, %v; want %d", tc.raw, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		raw string
		l   apiclient.MandateLimit
	}{
		{"0", eur}, {"1.234", eur}, {"-5", eur}, {"1e3", eur}, {"", eur}, {"1.", eur}, {"1,000.00", eur},
		{"1.5", count}, {"99999999999999999999", count},
	} {
		if got, err := ParseRoomAmount(tc.raw, tc.l); err == nil {
			t.Errorf("%q accepted as %d", tc.raw, got)
		}
	}
}

// The end date is read in the account's zone, and around a clock change
// the review shows the instant with its zone abbreviation.
func TestParseRoomUntilInTheAccountZone(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRoomUntil("2026-11-02T09:00", paris)
	if err != nil || !got.Equal(time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("winter: %s %v", got.UTC(), err)
	}
	got, _ = ParseRoomUntil("2026-07-02T09:00", paris)
	if !got.Equal(time.Date(2026, 7, 2, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("summer: %s", got.UTC())
	}
	// 02:30 on 25 Oct 2026 happens twice in Paris; whichever is read, its
	// zone is named on the review.
	got, _ = ParseRoomUntil("2026-10-25T02:30", paris)
	if s := got.In(paris).Format("15:04 MST"); s != "02:30 CEST" && s != "02:30 CET" {
		t.Fatalf("repeated hour: %s", s)
	}
	// The confirmation's instant is read as itself, whatever the zone.
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	if got, _ := ParseRoomUntil(at.Format(time.RFC3339Nano), paris); !got.Equal(at) {
		t.Fatalf("instant: %s", got)
	}
}

func TestRoomLimitLabelIsPlain(t *testing.T) {
	daily := apiclient.MandateLimit{Quantity: "count", Window: "calendar_day", Measure: "attempts", Limit: 2, BaseLimit: 2}
	if got := roomLimitLabel([]apiclient.MandateLimit{daily}, daily); got != "2 calls per calendar day" {
		t.Fatalf("label = %q", got)
	}
	other := daily
	other.Measure = "effects"
	if got := roomLimitLabel([]apiclient.MandateLimit{daily, other}, other); !strings.Contains(got, "room back only when nothing happened is proven") {
		t.Fatalf("two limits alike are told apart: %q", got)
	}
}

// The statement names who added extra room and when, in the
// account's zone, not by sign-in id and revision id.
func TestStatementNamesExtraRoom(t *testing.T) {
	at := time.Date(2026, 10, 13, 19, 4, 0, 0, time.UTC)
	st := &apiclient.MandateStatement{Limits: []apiclient.MandateLimit{{Quantity: "count", Window: "calendar_day", Status: "ok", Limit: 3, BaseLimit: 2, ExtraRoom: 1,
		RoomGrants: []apiclient.RoomGrant{
			{ID: "room-AKQ1", Amount: 1, Duration: "this_window", By: "user_3KQe", ConfirmedAt: at, ExpiresAt: at.Add(3 * time.Hour)},
			{ID: "room-AKQ2", Amount: 1, Duration: "until_date", By: "user_gone", At: at},
			{ID: "room-AKQ3", Amount: 1, Duration: "permanent", By: "user_me", At: at},
		}}}}
	if !RoomGrantsByOthers(st, "user_me") {
		t.Fatal("others added room")
	}
	NameRoomGrants(st, "user_me", "me@acme.test", []apiclient.Member{{Subject: "user_3KQe", Email: "ana@acme.test"}})
	got := []string{st.Limits[0].RoomGrants[0].By, st.Limits[0].RoomGrants[1].By, st.Limits[0].RoomGrants[2].By}
	if got[0] != "ana@acme.test" || got[1] != "an account member" || got[2] != "you (me@acme.test)" {
		t.Fatalf("names: %q", got)
	}
	m := apiclient.Mandate{ID: "card-a", Aggregates: []apiclient.MandateAggregate{{Quantity: "count", Limit: 2, Window: "calendar_day"}}, Location: "Europe/Paris"}
	var b strings.Builder
	if err := cardStatementPage(CardDetailView{Card: &m, Statement: st}).Render(components.WithTimeZone(context.Background(), "Europe/Paris"), &b); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	for _, want := range []string{"Extra room +1 call, until the window ends, added by ana@acme.test", "on 13 Oct 2026, 21:04 CEST", "ends 14 Oct 2026, 00:04 CEST", `title="Revision room-AKQ1"`} {
		if !strings.Contains(page, want) {
			t.Errorf("statement lacks %q", want)
		}
	}
	if strings.Contains(page, "user_3KQe") || strings.Contains(page, ">Revision room-") {
		t.Errorf("statement shows internal ids: %s", page)
	}
}
