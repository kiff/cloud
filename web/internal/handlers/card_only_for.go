package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// Resource scope bounds, as apps/api checks them (mandate.MaxResourceValues,
// MaxResourceLength). Checked here too so the owner reads why on the sheet.
const (
	maxScopeValues = 100
	maxScopeLength = 256
)

// scopeParam is the argument the tool's connection names as what each call
// is on, or "" when it names none or the gateway cannot be read.
func (h *DashboardHandler) scopeParam(ctx context.Context, bearer, tool string) string {
	if h.gateway == nil {
		return ""
	}
	conns, err := h.gateway.List(ctx, bearer)
	if err != nil {
		return ""
	}
	for _, c := range conns {
		if c.Name == tool {
			return c.ResourceArg
		}
	}
	return ""
}

// fillOnlyFor gives the Only for sheet the tool, its resource argument and
// the values the Card covers now.
func (h *DashboardHandler) fillOnlyFor(ctx context.Context, bearer string, v *pages.CardSheetView) {
	v.ScopeTool = pages.ToolCardTool(v.Card)
	v.ScopeParam = h.scopeParam(ctx, bearer, v.ScopeTool)
	if r := v.Card.Grants[0].Resources; r != nil {
		if r.Parameter == v.ScopeParam {
			v.ScopeValues = r.Values
		} else {
			// A scope on an argument the tool no longer names: shown as
			// it is, never as an unrestricted Card.
			v.OldScopeParam, v.OldScopeValues = r.Parameter, r.Values
		}
	}
}

// cardOnlyFor limits a tool Card to named values of its tool's resource
// argument, or lifts the limit. The Card is reissued under its own id with
// every other term as it is; apps/api keeps its holders, location and
// extra room.
func (h *DashboardHandler) cardOnlyFor(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.cardSession(w, r)
	if !ok || !toolsForm(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	sheet := "/cards/" + url.PathEscape(id) + "/only-for"
	fail := func(msg string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
		http.Redirect(w, r, sheet, http.StatusSeeOther)
	}
	if h.api == nil {
		fail("apps/api is not configured.")
		return
	}
	cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
	if err != nil {
		fail("This Card could not be read. Reload and try again.")
		return
	}
	i := slices.IndexFunc(cards, func(m apiclient.Mandate) bool { return m.ID == id })
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	m := cards[i]
	if m.RevokedAt != nil || !pages.IsToolCard(m) {
		fail("Only a tool Card in force can be limited here.")
		return
	}
	param := h.scopeParam(r.Context(), bearer, pages.ToolCardTool(m))
	if param == "" {
		fail(pages.ToolCardTool(m) + " doesn’t say what each call is on yet. Set it in the tool’s Settings first.")
		return
	}
	grant := m.Grants[0]
	switch r.PostFormValue("scope") {
	case "any":
		grant.Resources = nil
	case "only":
		values, msg := scopeValues(r.PostForm["keep"], r.PostFormValue("add"), grant.Resources, param)
		if msg != "" {
			fail(msg)
			return
		}
		grant.Resources = &apiclient.MandateResources{Parameter: param, Values: values}
	default:
		fail("Choose any value, or only the ones you list.")
		return
	}
	req := reissueRequest(m)
	req.Grants = []apiclient.MandateGrant{grant}
	if _, err := h.api.IssueMandateBearer(r.Context(), bearer, req); err != nil {
		var rejected *apiclient.MandateRejectedError
		if errors.As(err, &rejected) {
			fail("The Card was not changed: " + rejected.Message)
			return
		}
		h.logger.WarnContext(r.Context(), "cards: only-for failed", slog.String("card", id), slog.String("err", err.Error()))
		fail("The Card could not be changed. Nothing was changed; try again in a moment.")
		return
	}
	msg := "This Card now covers any " + param + "."
	if grant.Resources != nil {
		msg = fmt.Sprintf("This Card now covers only %d %s value(s). A call on any other is outside the Card.", len(grant.Resources.Values), param)
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashMandateIssued, Message: msg})
	http.Redirect(w, r, "/cards/"+url.PathEscape(id)+"/settings", http.StatusSeeOther)
}

// scopeValues is the kept values that the Card already covered, plus the
// added ones (comma or line separated), trimmed and without repeats.
func scopeValues(keep []string, add string, current *apiclient.MandateResources, param string) ([]string, string) {
	var out []string
	push := func(v string) string {
		v = strings.TrimSpace(v)
		if v == "" || slices.Contains(out, v) {
			return ""
		}
		if len(v) > maxScopeLength {
			return fmt.Sprintf("Each value can be at most %d characters.", maxScopeLength)
		}
		out = append(out, v)
		return ""
	}
	for _, k := range keep {
		// Only values the Card covered: a kept box cannot add one.
		if current != nil && current.Parameter == param && slices.Contains(current.Values, k) {
			push(k)
		}
	}
	for _, a := range strings.FieldsFunc(add, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if msg := push(a); msg != "" {
			return nil, msg
		}
	}
	switch {
	case len(out) == 0:
		return nil, "Add at least one value, or choose any " + param + "."
	case len(out) > maxScopeValues:
		return nil, fmt.Sprintf("A Card can name at most %d values.", maxScopeValues)
	}
	return out, ""
}

// reissueRequest is the Card as it stands, ready to issue again under its
// own id. Holders, location and extra room are omitted: apps/api keeps them
// on a reissue. The start, expiry, past-the-limit setting and hold time are
// sent: an omitted start would make a future Card effective now, and an
// omitted expiry would end it never.
func reissueRequest(m apiclient.Mandate) apiclient.IssueMandateRequest {
	req := apiclient.IssueMandateRequest{ID: m.ID, Subject: m.Subject, Domain: m.Domain, Environment: m.Environment,
		Grants: slices.Clone(m.Grants), ValidFrom: m.ValidFrom, ValidUntil: m.ValidUntil, OnExceed: m.OnExceed}
	for _, a := range m.Aggregates {
		req.Aggregates = append(req.Aggregates, apiclient.MandateAggregate{Quantity: a.Quantity, Limit: a.Limit, Window: a.Window, Measure: a.Measure})
	}
	if m.HoldExpirySeconds > 0 {
		req.HoldExpiry = fmt.Sprintf("%ds", m.HoldExpirySeconds)
	}
	return req
}
