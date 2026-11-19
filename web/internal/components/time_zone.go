package components

import (
	"context"
	"time"

	// The zone database ships in the binary, so an account's time zone
	// reads the same on a host without one.
	_ "time/tzdata"
)

type timeZoneKey struct{}

// WithTimeZone carries the account's time zone (from /v1/me), so pages
// show times the way the owner reads a clock. An empty or unknown zone
// is UTC.
func WithTimeZone(ctx context.Context, zone string) context.Context {
	loc, err := time.LoadLocation(zone)
	if zone == "" || err != nil {
		loc = time.UTC
	}
	return context.WithValue(ctx, timeZoneKey{}, loc)
}

// TimeZone is the account's time zone, UTC when it was not read.
func TimeZone(ctx context.Context) *time.Location {
	if loc, ok := ctx.Value(timeZoneKey{}).(*time.Location); ok {
		return loc
	}
	return time.UTC
}

// LocalTime is t in the account's time zone, labelled with the zone so it
// never reads as another: "14 Oct 2026, 00:00 CEST".
func LocalTime(ctx context.Context, t time.Time) string {
	return InZone(t, TimeZone(ctx))
}

// InZone is t in loc, labelled with the zone.
func InZone(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2 Jan 2006, 15:04 MST")
}
