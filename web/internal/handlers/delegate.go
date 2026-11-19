package handlers

import (
	"net/http"
	"net/url"

	"github.com/kiff/cloud/web/internal/delegate"
	"github.com/kiff/cloud/web/internal/pages"
)

// Delegate serves /delegate and its one action endpoint.
//
// State is the session's action log, carried in the form (htmx) or the
// query string (no JavaScript). Every request replays it through the
// delegate engine from scratch, so the server never trusts a balance or
// a holder sent by the browser.
func Delegate() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /delegate", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		s := replayDelegate(q.Get("card"), q.Get("log"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Delegate(pages.DelegateView{Session: s}).Render(r.Context(), w)
	})
	mux.HandleFunc("POST /delegate/step", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", http.StatusBadRequest)
			return
		}
		log := r.PostFormValue("log")
		if step := r.PostFormValue("step"); step != "" {
			if log != "" {
				log += ","
			}
			log += step
		}
		s := replayDelegate(r.PostFormValue("card"), log)
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = pages.DelegateLive(s).Render(r.Context(), w)
			return
		}
		q := url.Values{"card": {s.Card.ID}, "log": {s.CurrentLog()}}
		http.Redirect(w, r, "/delegate?"+q.Encode()+"#try", http.StatusSeeOther)
	})
	return mux
}

func replayDelegate(card, log string) delegate.Session {
	b := delegate.Find(card)
	return delegate.Replay(b, delegate.AgentsFor(b), log)
}
