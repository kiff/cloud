package components

import (
	"strconv"

	"github.com/kiff/cloud/web/internal/notify"
)

// notificationBellMax caps how many items the topbar popover shows; the
// full feed lives on /dashboard/notifications.
const notificationBellMax = 6

// notificationBellItems returns the newest items for the popover, capped
// at notificationBellMax. The summary is already newest-first.
func notificationBellItems(sum notify.Summary) []notify.Item {
	if len(sum.Items) <= notificationBellMax {
		return sum.Items
	}
	return sum.Items[:notificationBellMax]
}

// notificationBadgeText renders the badge number, clamping to "9+" so a
// busy queue never blows out the circle.
func notificationBadgeText(count int) string {
	if count > 9 {
		return "9+"
	}
	return strconv.Itoa(count)
}

// notificationBadgeLabel is the accessible label for the badge.
func notificationBadgeLabel(count int) string {
	return strconv.Itoa(count) + " notifications need attention"
}

// notificationRevision renders the revision "Clear all" acknowledges, so
// only the version the user saw is marked seen.
func notificationRevision(rev int64) string {
	return strconv.FormatInt(rev, 10)
}
