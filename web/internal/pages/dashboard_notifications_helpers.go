package pages

import (
	"context"

	"github.com/kiff/cloud/web/internal/notify"
)

// notificationsFiltered returns the feed items matching the active
// filter ("" = all), newest-first. The summary is read from the request
// context (the notifications middleware composed it for this GET).
func notificationsFiltered(ctx context.Context, filter string) []notify.Item {
	sum := notify.SummaryFrom(ctx)
	if filter == "" {
		return sum.Items
	}
	out := make([]notify.Item, 0, len(sum.Items))
	for _, it := range sum.Items {
		if string(it.Kind) == filter {
			out = append(out, it)
		}
	}
	return out
}

// notificationsCount returns how many items match a filter ("" = all),
// for the filter chips' counts.
func notificationsCount(ctx context.Context, filter string) int {
	if filter == "" {
		return len(notify.SummaryFrom(ctx).Items)
	}
	return len(notificationsFiltered(ctx, filter))
}

// notificationsFilterHref builds a filter chip's href, keeping the page
// canonical (no ?filter= for "all").
func notificationsFilterHref(filter string) string {
	if filter == "" {
		return "/dashboard/notifications"
	}
	return "/dashboard/notifications?filter=" + filter
}

// notificationsChipClass marks the active filter chip.
func notificationsChipClass(current, chip string) string {
	if current == chip {
		return "dash-filter dash-filter-active"
	}
	return "dash-filter"
}

// notificationKindLabel is the human label for an item's kind, used as
// the row's scope tag.
func notificationKindLabel(k notify.Kind) string {
	switch k {
	case notify.KindBudget:
		return "Card budget"
	case notify.KindApproval:
		return "approval"
	case notify.KindRuntime:
		return "runtime"
	default:
		return string(k)
	}
}
