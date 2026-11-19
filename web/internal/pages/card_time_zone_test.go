package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestAccountTimeZoneSettingsAndCardBoundary(t *testing.T) {
	for _, tc := range []struct {
		role string
		caps []string
		edit bool
	}{{"admin", []string{"account.settings"}, true}, {"viewer", []string{}, false}} {
		t.Run(tc.role, func(t *testing.T) {
			var b bytes.Buffer
			me := &apiclient.Me{TimeZone: "Europe/Paris", Capabilities: tc.caps}
			if err := settingsTimeZone(me).Render(context.Background(), &b); err != nil {
				t.Fatal(err)
			}
			html := b.String()
			if !strings.Contains(html, "Europe/Paris") || strings.Contains(html, `action="/dashboard/time-zone"`) != tc.edit {
				t.Fatalf("incorrect setting permissions: %s", html)
			}
		})
	}
	card := apiclient.Mandate{Location: "America/Bogota", Aggregates: []apiclient.MandateAggregate{{Window: "calendar_day"}}}
	if cardDayZone(card) != "America/Bogota" {
		t.Fatal("used account zone instead of Card zone")
	}
	card.Location = ""
	if cardDayZone(card) != "UTC" {
		t.Fatal("legacy Card must use UTC")
	}
	card.Aggregates[0].Window = "rolling_24h"
	if cardDayZone(card) != "" {
		t.Fatal("rolling limit must not claim local midnight")
	}
}

func TestTimeZoneDropdownOffersOnlyZonesTheAPIAccepts(t *testing.T) {
	if len(ianaZones) < 400 {
		t.Fatalf("only %d zones listed", len(ianaZones))
	}
	for _, z := range ianaZones {
		if _, err := time.LoadLocation(z); err != nil {
			t.Errorf("%s is listed but Go's tzdata can't load it: %v", z, err)
		}
	}
}

func TestTimeZoneDropdownSelectsTheSavedZone(t *testing.T) {
	render := func(zone string) string {
		var b bytes.Buffer
		me := &apiclient.Me{TimeZone: zone, Capabilities: []string{"account.settings"}}
		if err := settingsTimeZone(me).Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	html := render("America/Argentina/Buenos_Aires")
	if !strings.Contains(html, `<select id="account-time-zone"`) || strings.Contains(html, "Use a named time zone") {
		t.Fatalf("expected a plain dropdown: %s", html)
	}
	if !strings.Contains(html, `<option value="America/Argentina/Buenos_Aires" selected>Buenos Aires, Argentina</option>`) {
		t.Fatal("saved zone is not preselected")
	}
	if strings.Count(html, " selected") != 1 {
		t.Fatalf("want exactly one selected option, got %d", strings.Count(html, " selected"))
	}
	if html = render("Asia/Calcutta"); !strings.Contains(html, `<option value="Asia/Calcutta" selected>Asia/Calcutta</option>`) {
		t.Fatal("a saved zone missing from the list must stay selected, not fall back")
	}
	if html = render(""); !strings.Contains(html, `<option value="UTC" selected>UTC</option>`) {
		t.Fatal("no saved zone must read as UTC")
	}
}
