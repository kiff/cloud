package pages

import (
	"sort"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// cardDayZone reports the actual calendar-day boundary of this Card,
// never the account's current default. An empty location means UTC.
func cardDayZone(m apiclient.Mandate) string {
	for _, a := range m.Aggregates {
		if a.Window == "calendar_day" {
			if m.Location == "" {
				return "UTC"
			}
			return m.Location
		}
	}
	return ""
}

func accountTimeZone(me *apiclient.Me) string {
	if me.TimeZone == "" {
		return "UTC"
	}
	return me.TimeZone
}

type zoneOption struct{ Value, Label string }

type zoneGroup struct {
	Region string
	Zones  []zoneOption
}

// timeZoneGroups lists every zone by region, cities in alphabetical
// order. A saved zone missing from the list (an older alias) is kept as
// an option so the form never silently changes it.
func timeZoneGroups(current string) []zoneGroup {
	var groups []zoneGroup
	found := current == "UTC"
	for _, z := range ianaZones {
		region, city, _ := strings.Cut(z, "/")
		if len(groups) == 0 || groups[len(groups)-1].Region != region {
			groups = append(groups, zoneGroup{Region: region})
		}
		g := &groups[len(groups)-1]
		g.Zones = append(g.Zones, zoneOption{Value: z, Label: zoneLabel(city)})
		found = found || z == current
	}
	for i := range groups {
		sort.Slice(groups[i].Zones, func(a, b int) bool { return groups[i].Zones[a].Label < groups[i].Zones[b].Label })
	}
	if !found {
		groups = append([]zoneGroup{{Region: "Current", Zones: []zoneOption{{Value: current, Label: current}}}}, groups...)
	}
	return groups
}

// zoneLabel reads "Argentina/Buenos_Aires" as "Buenos Aires, Argentina".
func zoneLabel(city string) string {
	parts := strings.Split(strings.ReplaceAll(city, "_", " "), "/")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ", ")
}
