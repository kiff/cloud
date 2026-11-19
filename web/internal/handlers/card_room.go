package handlers

import (
	"crypto/rand"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

func (h *DashboardHandler) cardRoom(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	if !components.Can(r.Context(), "cards.manage") {
		http.Error(w, "Management authority is required", http.StatusForbidden)
		return
	}
	v := pages.CardRoomView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName}
	if h.api == nil {
		v.Error = "The API is not configured."
		_ = pages.CardRoom(v).Render(r.Context(), w)
		return
	}
	st, err := h.api.MandateStatementBearer(r.Context(), bearer, r.PathValue("id"))
	if err != nil {
		v.Error = "This Card could not be read. No room was added."
		_ = pages.CardRoom(v).Render(r.Context(), w)
		return
	}
	v.Statement = st
	if r.Method == http.MethodGet {
		_ = pages.CardRoom(v).Render(r.Context(), w)
		return
	}
	if !toolsForm(w, r) {
		return
	}
	fail := func(msg string) {
		v.Error = msg
		w.WriteHeader(http.StatusBadRequest)
		_ = pages.CardRoom(v).Render(r.Context(), w)
	}
	duration := r.PostFormValue("duration")
	if duration == "" {
		duration = "this_window"
	}
	if duration != "this_window" && duration != "until_date" && duration != "permanent" {
		fail("Choose until the window ends, until a date, or for good.")
		return
	}
	// The end date is read only when it is the one chosen, as a time in
	// the account's zone; the confirmation carries the instant.
	var until time.Time
	if duration == "until_date" {
		var err error
		until, err = pages.ParseRoomUntil(r.PostFormValue("until"), components.TimeZone(r.Context()))
		if err != nil {
			fail("Enter the date and time the extra room ends.")
			return
		}
		if !until.After(time.Now()) {
			fail("Choose an end in the future.")
			return
		}
	}
	if r.PathValue("step") == "preview" {
		var chosen *apiclient.MandateLimit
		for i, l := range st.Limits {
			if l.Quantity+"|"+l.Window+"|"+l.Measure == r.PostFormValue("limit") && !l.Unavailable() {
				chosen = &st.Limits[i]
			}
		}
		if chosen == nil {
			fail("Choose one of the Card's limits. If none is listed, its usage could not be read: reload.")
			return
		}
		// Typed in the limit's own terms: 12.50 on a EUR limit at two
		// decimals is 1250.
		amount, err := pages.ParseRoomAmount(r.PostFormValue("amount"), *chosen)
		if err != nil {
			fail(err.Error())
			return
		}
		if chosen.Limit < 0 || amount > math.MaxInt64-chosen.Limit {
			fail("That would make the limit too large.")
			return
		}
		base := chosen.BaseLimit
		if base == 0 && chosen.ExtraRoom == 0 {
			base = chosen.Limit
		}
		req := apiclient.ExtraRoomRequest{ID: "room-" + rand.Text(), Quantity: chosen.Quantity, Window: chosen.Window, Measure: chosen.Measure, Amount: amount, Duration: duration, ExpectedBase: &base}
		if duration == "this_window" {
			if chosen.RoomWindowEnd.IsZero() || !chosen.RoomWindowEnd.After(time.Now()) {
				fail("When this window ends could not be read. Reload and try again.")
				return
			}
			req.WindowEnd = chosen.RoomWindowEnd
			v.End = req.WindowEnd
		} else if duration == "until_date" {
			req.Until = until
			v.End = until
		}
		if st.Mandate.ValidUntil != nil && !v.End.IsZero() && v.End.After(*st.Mandate.ValidUntil) {
			v.End = *st.Mandate.ValidUntil
			if duration == "this_window" {
				req.WindowEnd = v.End
			}
		}
		v.Preview = &req
		_ = pages.CardRoom(v).Render(r.Context(), w)
		return
	}
	// The confirmation carries the stored amount the review showed.
	amount, err := strconv.ParseInt(r.PostFormValue("amount"), 10, 64)
	if err != nil || amount <= 0 {
		fail("The confirmation could not be read. Review the extra room again.")
		return
	}
	windowEnd, err := time.Parse(time.RFC3339Nano, r.PostFormValue("window_end"))
	if err != nil {
		fail("The confirmation could not be read. Review the extra room again.")
		return
	}
	base, err := strconv.ParseInt(r.PostFormValue("base_limit"), 10, 64)
	if err != nil {
		fail("The confirmation could not be read. Review the extra room again.")
		return
	}
	req := apiclient.ExtraRoomRequest{ID: r.PostFormValue("revision_id"), Quantity: r.PostFormValue("quantity"), Window: r.PostFormValue("window"), Measure: r.PostFormValue("measure"), Amount: amount, Duration: duration, Until: until, WindowEnd: windowEnd, ExpectedBase: &base}
	if _, err := h.api.GrantExtraRoomBearer(r.Context(), bearer, st.Mandate.ID, req); err != nil {
		fail("The extra room was not added: " + err.Error())
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashMandateIssued, Message: "Extra room added. What was already used still counts, and a held call is checked against the Card again when the agent collects it."})
	http.Redirect(w, r, "/cards/"+url.PathEscape(st.Mandate.ID), http.StatusSeeOther)
}
