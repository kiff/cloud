package mandate_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

func TestCardTimeZoneStoreDefaultsAndPreservation(t *testing.T) {
	run := func(t *testing.T, s mandate.Store, tenantID string) {
		ctx := context.Background()
		m := pgMandate(tenantID, "tz")
		m.DefaultLocation = "Europe/Paris"
		check := func(want string) {
			t.Helper()
			if err := s.Issue(ctx, m, "owner"); err != nil {
				t.Fatal(err)
			}
			list, err := s.List(ctx, tenantID)
			if err != nil {
				t.Fatal(err)
			}
			for _, saved := range list {
				if saved.ID == m.ID {
					if saved.Location != want || saved.DefaultLocation != "" {
						t.Fatalf("location %q transient %q", saved.Location, saved.DefaultLocation)
					}
					return
				}
			}
			t.Fatal("Card not found")
		}
		check("Europe/Paris")
		original := m
		m.ID = "legacy-utc"
		m.DefaultLocation = ""
		check("")
		m.DefaultLocation = "America/Bogota"
		check("")
		m = original
		m.DefaultLocation = "America/Bogota"
		check("Europe/Paris")
		m.Location = "Asia/Tokyo"
		check("Asia/Tokyo")
		m.Location = ""
		check("Asia/Tokyo")
	}
	t.Run("memory", func(t *testing.T) { run(t, mandate.NewMemoryStore(), "tz-memory") })
	t.Run("postgres", func(t *testing.T) {
		pool := requirePostgres(t)
		seedTenant(t, pool, "tz-pg")
		run(t, mandate.NewPostgresStore(pool), "tz-pg")
	})
}

func TestCardTimeZoneCalendarMidnightAndDST(t *testing.T) {
	m := mandate.Mandate{Location: "Europe/Paris"}
	for _, tc := range []struct {
		day   string
		hours int
	}{{"2026-03-29", 23}, {"2026-10-25", 25}} {
		t.Run(tc.day, func(t *testing.T) {
			now, err := time.ParseInLocation("2006-01-02 15:04", tc.day+" 12:00", m.Loc())
			if err != nil {
				t.Fatal(err)
			}
			start, err := mandate.WindowCalendarDay.Start(now, m.Loc())
			if err != nil {
				t.Fatal(err)
			}
			if start.In(m.Loc()).Hour() != 0 {
				t.Fatal("not local midnight")
			}
			next := start.AddDate(0, 0, 1)
			if next.Sub(start) != time.Duration(tc.hours)*time.Hour {
				t.Fatal("wrong DST day duration")
			}
			before, _ := mandate.WindowCalendarDay.Start(start.Add(-time.Nanosecond), m.Loc())
			if !before.Before(start) {
				t.Fatal("window did not reset at midnight")
			}
			rolling, _ := mandate.WindowRollingDay.Start(now, m.Loc())
			if now.Sub(rolling) != 24*time.Hour {
				t.Fatal("rolling window changed")
			}
		})
	}
}
