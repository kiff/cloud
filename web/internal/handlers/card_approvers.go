package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// approverScopes are the Card's amounts an approver can be given: each
// action's per-action maximum on a sum. The unit is the gateway tool's
// when it declares one, else the one apps/api resolved for the Card's
// limit on the same sum, so a Card off the gateway reads in € too.
func (h *DashboardHandler) approverScopes(ctx context.Context, bearer string, m apiclient.Mandate, limits []apiclient.MandateLimit) []pages.ApproverScope {
	out := []pages.ApproverScope{}
	for _, g := range m.Grants {
		for _, p := range g.PerActionMax {
			if strings.HasPrefix(p.Quantity, "sum(") && strings.HasSuffix(p.Quantity, ")") {
				out = append(out, pages.ApproverScope{Action: g.Action, Parameter: p.Quantity[4 : len(p.Quantity)-1], PerAction: p.Max})
			}
		}
	}
	if h.gateway != nil && m.Domain == "gateway" {
		if tools, err := h.gateway.List(ctx, bearer); err == nil {
			for i := range out {
				for _, t := range tools {
					if strings.EqualFold(t.Name, out[i].Action) && strings.EqualFold(t.AmountArg, out[i].Parameter) {
						out[i].Unit, out[i].Scale = t.AmountUnit, t.AmountScale
					}
				}
			}
		}
	}
	for i := range out {
		if out[i].Unit != "" {
			continue
		}
		for _, l := range limits {
			if l.Unit != "" && strings.EqualFold(l.Quantity, "sum("+out[i].Parameter+")") {
				out[i].Unit, out[i].Scale = l.Unit, l.Scale
			}
		}
	}
	return out
}

// loadApprovers fills the Approvers tab: who may see it, the Card's
// approvers, and the amounts one can be given.
func (h *DashboardHandler) loadApprovers(ctx context.Context, bearer string, v *pages.CardDetailView) {
	if me, err := h.api.GetMeBearer(ctx, bearer); err != nil || me.MemberRole != "admin" {
		return
	}
	v.ApproverAdmin = true
	var err error
	if v.Approvers, err = h.api.ListApproversBearer(ctx, bearer, v.Card.ID); err != nil {
		v.ApproverError = "External approver connections could not be read. Human review is still available in Needs you."
	}
	v.ApproverScopes = h.approverScopes(ctx, bearer, *v.Card, v.Balance.Limits)
}

// cardLimits is the Card's statement limits, for their units; nil when
// the statement cannot be read.
func (h *DashboardHandler) cardLimits(ctx context.Context, bearer, id string) []apiclient.MandateLimit {
	if st, err := h.api.MandateStatementBearer(ctx, bearer, id); err == nil {
		return st.Limits
	}
	return nil
}
func parseApproverAmount(raw string, scale int) (int64, error) {
	if scale < 0 || scale > 6 {
		return 0, errors.New("invalid amount scale")
	}
	whole, frac, _ := strings.Cut(strings.TrimSpace(raw), ".")
	if whole == "" || len(frac) > scale {
		return 0, errors.New("enter a positive amount with the displayed precision")
	}
	for _, r := range whole + frac {
		if r < '0' || r > '9' {
			return 0, errors.New("enter a positive amount")
		}
	}
	n, err := strconv.ParseInt(whole+frac+strings.Repeat("0", scale-len(frac)), 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("enter a positive amount within the supported range")
	}
	return n, nil
}
func (h *DashboardHandler) connectApprover(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	target := "/cards/" + url.PathEscape(r.PathValue("id")) + "/approvers"
	fail := func(msg string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
	if h.api == nil {
		fail("The API is unavailable; no authority was granted.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		fail("The form could not be read.")
		return
	}
	cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
	if err != nil {
		fail("The Card could not be read; no authority was granted.")
		return
	}
	var m apiclient.Mandate
	for _, c := range cards {
		if c.ID == r.PathValue("id") {
			m = c
		}
	}
	if m.ID == "" {
		http.NotFound(w, r)
		return
	}
	// The connect steps name the scope in one field (pages.ApproverScope.Key),
	// and the end as a suggested datetime or a date.
	if parts := strings.Split(r.PostFormValue("scope"), "|"); len(parts) == 4 {
		r.PostForm.Set("action", parts[0])
		r.PostForm.Set("parameter", parts[1])
		r.PostForm.Set("unit", parts[2])
		r.PostForm.Set("scale", parts[3])
	}
	if r.PostFormValue("expires_at") == "" && r.PostFormValue("expires_on") != "" {
		r.PostForm.Set("expires_at", r.PostFormValue("expires_on")+"T23:59")
	}
	var scope *pages.ApproverScope
	for _, candidate := range h.approverScopes(r.Context(), bearer, m, h.cardLimits(r.Context(), bearer, m.ID)) {
		if candidate.Action == r.PostFormValue("action") && candidate.Parameter == r.PostFormValue("parameter") {
			copy := candidate
			scope = &copy
		}
	}
	if scope == nil {
		fail("Choose an action and amount from the current Card.")
		return
	}
	if r.PostFormValue("unit") != scope.Unit || r.PostFormValue("scale") != strconv.Itoa(scope.Scale) {
		fail("The amount unit changed or could not be read. Reload the Card before granting authority.")
		return
	}
	max, err := parseApproverAmount(r.PostFormValue("max"), scope.Scale)
	if err != nil {
		fail(err.Error())
		return
	}
	expiry, err := time.ParseInLocation("2006-01-02T15:04", r.PostFormValue("expires_at"), time.UTC)
	if err != nil {
		fail("Choose an expiry in UTC, within the next 90 days.")
		return
	}
	created, err := h.api.CreateApproverBearer(r.Context(), bearer, apiclient.CreateApproverRequest{Name: strings.TrimSpace(r.PostFormValue("name")), CardID: m.ID, Worker: r.PostFormValue("worker"), Action: scope.Action, Parameter: scope.Parameter, Max: max, ExpiresAt: expiry})
	if err != nil {
		fail("The connection could not be confirmed. Reload this Card before trying again. Setup requires an active admin and an active Ask Card without human-only separation of duties.")
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashKind("approver_key_revealed"), Key: &session.FlashKeyPayload{ID: created.Grant.ID, Plaintext: created.Credential, Label: "External approver: " + created.Grant.Name, Owner: s.Subject}})
	http.Redirect(w, r, target, http.StatusSeeOther)
}
func (h *DashboardHandler) revokeCardApprover(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	target := "/cards/" + url.PathEscape(r.PathValue("id")) + "/approvers"
	msg, kind := "External approver revoked. Unused approvals lose authority; requests remain available for human review until their original deadline.", session.FlashOwnerAnswered
	if h.api == nil || h.api.RevokeApproverBearer(r.Context(), bearer, r.PathValue("grant")) != nil {
		msg, kind = "The connection could not be revoked. Reload and try again.", session.FlashError
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: kind, Message: msg})
	http.Redirect(w, r, target, http.StatusSeeOther)
}
