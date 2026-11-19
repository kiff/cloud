package pages

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// roomLimitLabel names a limit the way the Card reads it: "2 calls per
// calendar day". The measure is added only when two limits would read
// the same.
func roomLimitLabel(ls []apiclient.MandateLimit, l apiclient.MandateLimit) string {
	label := roomValue(l, roomBase(l)) + " " + windowLabel(l.Window)
	for _, o := range ls {
		if roomKey(o) != roomKey(l) && o.Quantity == l.Quantity && o.Window == l.Window {
			return label + " · " + measureLabel(l.Measure)
		}
	}
	return label
}

func measureLabel(m string) string {
	switch m {
	case "attempts":
		return "refused calls still count"
	case "effects":
		return "room back only when nothing happened is proven"
	}
	return m
}

// roomDurationLabel is how long extra room lasts, in words.
func roomDurationLabel(d string) string {
	switch d {
	case "this_window":
		return "until the window ends"
	case "until_date":
		return "until a date"
	case "permanent":
		return "permanent"
	}
	return d
}

// ParseRoomAmount reads how much room the owner typed, in the limit's own
// terms: a number of calls for a call limit, and for a limit in a
// currency an amount such as 10 or 12.50, which is stored as the tool's
// whole units (1250 at two decimals). Typing 10 never adds 10 cents.
func ParseRoomAmount(raw string, l apiclient.MandateLimit) (int64, error) {
	scale := 0
	if l.Unit != "" {
		scale = l.Scale
	}
	bad := errors.New("Enter a whole number above zero.")
	if scale > 0 {
		bad = errors.New("Enter an amount above zero, such as 10 or 12.50, with up to " + strconv.Itoa(scale) + " decimals.")
	}
	whole, frac, dotted := strings.Cut(strings.ReplaceAll(strings.TrimSpace(raw), ",", "."), ".")
	if whole == "" || !digits(whole) || (dotted && (frac == "" || !digits(frac) || len(frac) > scale)) {
		return 0, bad
	}
	frac += strings.Repeat("0", scale-len(frac))
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil || n <= 0 || n == math.MaxInt64 {
		return 0, bad
	}
	return n, nil
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParseRoomUntil reads the "until a date" field, a datetime-local value,
// as a time in the account's zone. The review screen then shows the
// instant with its zone, so a time a clock change skips or repeats is
// never confirmed unseen.
func ParseRoomUntil(raw string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", raw, loc)
}

// roomDayZoneNote says whose midnight ends a daily limit's extra room,
// when the Card's days run on another zone than the account's.
func roomDayZoneNote(m apiclient.Mandate, window string, account *time.Location) string {
	zone := cardDayZone(m)
	if window != "calendar_day" || zone == "" || zone == account.String() {
		return ""
	}
	return "This Card's days start at midnight in " + zone + ", the zone it was made with."
}

// RoomGrantsByOthers reports whether someone other than the viewer added
// extra room on this Card, so their email has to be looked up.
func RoomGrantsByOthers(st *apiclient.MandateStatement, mySubject string) bool {
	for _, l := range st.Limits {
		for _, g := range l.RoomGrants {
			if g.By != mySubject {
				return true
			}
		}
	}
	return false
}

// NameRoomGrants names who added each extra room by email rather than
// sign-in id: "you (ana@acme.test)", a member's email, or, when the member
// can't be found, plainly "an account member".
func NameRoomGrants(st *apiclient.MandateStatement, mySubject, myEmail string, members []apiclient.Member) {
	for i := range st.Limits {
		for j := range st.Limits[i].RoomGrants {
			g := &st.Limits[i].RoomGrants[j]
			e := apiclient.CardException{ResolvedBy: g.By}
			NameAnswerer(&e, mySubject, myEmail, members)
			if e.ResolvedBy == g.By && !strings.Contains(g.By, "@") {
				e.ResolvedBy = "an account member"
			}
			g.By = e.ResolvedBy
		}
	}
}
