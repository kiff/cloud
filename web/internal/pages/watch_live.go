package pages

// WatchPage is tafs on a watch: what it does, how to get it, and what it keeps.
const WatchPage = "/tafs/watch"

// WatchLive is whether WatchPage is public, which it is once the Wear OS watch is on Google Play (KIFF_WATCH_LIVE=on). Off, the
// page answers in no language, as any unknown path does, and nothing links it: not /support, the sitemap or llms.txt. The
// page is written and tested either way, so going live is a value, not a change.
var WatchLive = false

// HoldWatch takes the page out of the site while it isn't live: its translations are held back (no route, no hreflang, no
// sitemap entry), and callers leave out its English route and its links. Called once at start, before any route is built.
func HoldWatch(live bool) {
	WatchLive = live
	if live {
		delete(HeldBackTranslations, WatchPage)
	} else {
		HeldBackTranslations[WatchPage] = true
	}
}

// The page starts held back, in the binary and in tests: main sets the switch from KIFF_WATCH_LIVE before any route is built.
func init() { HoldWatch(false) }
