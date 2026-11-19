package handlers

import (
	"net/http"
	"time"

	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// AccountDeleted serves /account/deleted, where Settings → Delete
// account lands. It says the deletion was accepted and when the grace
// window ends, read from the one-shot flash the delete set. With no
// such flash (a reload, a shared link) it sends the visitor home, so
// the page never claims a deletion it didn't see.
//
// Only the dashboard's delete sets that flash, so the restore line it
// offers is for those deletions alone. A deletion from the tafs app
// (/v1/account-deletions) can't be undone and never lands here.
func AccountDeleted(sessions *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessions == nil {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		f, ok := sessions.ReadFlash(w, r)
		if !ok || f.Kind != session.FlashAccountDeleted {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		ends := ""
		if t, err := time.Parse(time.RFC3339, f.Message); err == nil {
			ends = t.UTC().Format("2 January 2006")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("Cache-Control", "no-store")
		_ = pages.AccountDeleted(ends).Render(r.Context(), w)
	})
}
