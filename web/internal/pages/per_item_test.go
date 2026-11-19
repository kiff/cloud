package pages

import (
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// A per-item total ("1 a week for each invoice") has no single balance:
// it is neither unreadable nor read as spent, and its line names its item.
func TestAPerItemTotalIsShownByItsLimit(t *testing.T) {
	per := apiclient.MandateLimit{Quantity: "count", Limit: 1, Window: "rolling_7d", Status: "per_item", Per: "invoice_id"}
	day := apiclient.MandateLimit{Quantity: "count", Limit: 10, Window: "calendar_day", Status: "ok", Remaining: 10}
	b := CardBalance{Limits: []apiclient.MandateLimit{day, per}}
	if per.Unavailable() || !per.PerItem() || b.Unknown() {
		t.Fatal("a per-item total read as unreadable")
	}
	if ls := b.Readable(); len(ls) != 1 || ls[0].PerItem() {
		t.Fatalf("readable = %+v: a per-item total has no remaining to read as spent", ls)
	}
	line := cardLimitLine(apiclient.Mandate{}, b)
	if !strings.Contains(line, "1 in any 7 days for each invoice id") {
		t.Errorf("line = %q", line)
	}
	if got := windowLabel("rolling_7d"); got != "per rolling 7 days" {
		t.Errorf("label = %q", got)
	}
	if got := totalName("rolling_7d"); got != "total for any 7 days" {
		t.Errorf("total = %q", got)
	}
}
