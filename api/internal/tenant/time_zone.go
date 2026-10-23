package tenant

import (
	"strings"
	"time"
	_ "time/tzdata"
)

const MetadataKeyTimeZone = "time_zone"

// ParseTimeZone accepts named zones, never the server's local zone.
func ParseTimeZone(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "Local" {
		return "", false
	}
	_, err := time.LoadLocation(value)
	return value, err == nil
}

// TimeZoneFor is the default for newly issued Cards, not a live override.
func TimeZoneFor(t Tenant) string {
	if value, ok := t.Metadata[MetadataKeyTimeZone].(string); ok {
		if zone, valid := ParseTimeZone(value); valid {
			return zone
		}
	}
	return "UTC"
}
