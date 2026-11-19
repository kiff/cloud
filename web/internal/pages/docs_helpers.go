package pages

import "github.com/kiff/cloud/web/internal/content/docs"

// navLabel returns the short label for a docs page in the sidebar.
// Most pages use their title verbatim; the quickstart's title is a
// full sentence, so the nav shows a compact form instead.
func navLabel(p docs.Page) string {
	switch p.Slug {
	case "quickstart":
		return "Quickstart"
	default:
		return p.Title
	}
}
