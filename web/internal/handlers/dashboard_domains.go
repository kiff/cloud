package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// domains renders /dashboard/domains: the domain-apex surface. It leads
// with the tenant's domains; a one-domain tenant gets the single
// domain's full view inline (actions, lifecycle, observed agents,
// evidence entry point), not a bare control.
func (h *DashboardHandler) domains(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	view := pages.DashboardDomainsView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Flash:       convertFlash(flash),
	}

	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to enable domains."
		_ = pages.DashboardDomains(view).Render(r.Context(), w)
		return
	}

	bearer, me, ok := h.resolveMe(w, r, s, func(v pages.DashboardDomainsView) {
		_ = pages.DashboardDomains(v).Render(r.Context(), w)
	}, view)
	if !ok {
		return
	}
	view.Me = me

	list, err := h.api.ListDomainsBearer(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		}
		view.ErrMsg = "could not load your domains: " + err.Error()
		_ = pages.DashboardDomains(view).Render(r.Context(), w)
		return
	}
	view.Domains = list

	// A one-domain tenant gets a domain-shaped surface: load the detail
	// inline so the apex isn't a single bare card.
	if len(list) == 1 {
		if detail, err := h.api.GetDomainDetailBearer(r.Context(), bearer, list[0].Name); err != nil {
			if errors.Is(err, apiclient.ErrUnauthorized) {
				h.sessions.Clear(w)
				http.Redirect(w, r, "/sign-in", http.StatusFound)
				return
			}
			// Non-fatal: fall back to the list card.
			h.logger.WarnContext(r.Context(), "dashboard domains: load detail failed",
				slog.String("domain", list[0].Name), slog.String("err", err.Error()))
		} else {
			view.Selected = detail
		}
	}

	// When no single domain's detail is in focus (multi-domain, or a
	// single domain whose inline detail failed to load), render the
	// operational portfolio: each domain's posture, recent activity, and
	// next action. Built concurrently so the page stays fast as the
	// domain count grows.
	if view.Selected == nil && len(list) > 0 {
		h.decoratePortfolio(r, bearer, &view)
	}

	// Fold the Overview's next-action card + activity glance onto the
	// domain home when a domain is in focus.
	h.decorateDomainHome(r, bearer, &view)

	h.decorateDomainTab(r, bearer, &view)

	_ = pages.DashboardDomains(view).Render(r.Context(), w)
}

// decorateDomainTab resolves which section of the domain page is in view
// and, for the Overview, loads the cards in force on the domain with
// their balances. A failed read is surfaced rather than rendered as "no
// card grants this action", which would be a claim about the agents made
// out of a fact about us.
func (h *DashboardHandler) decorateDomainTab(r *http.Request, bearer string, view *pages.DashboardDomainsView) {
	view.Tab = pages.NormalizeDomainTab(r.URL.Query().Get("tab"))
	if view.Selected == nil || view.Tab != "overview" {
		return
	}
	cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "domain overview: list cards failed", slog.String("err", err.Error()))
		view.ErrMsg = "The cards on this domain could not be read, so which agents may act here is not shown."
		return
	}
	now := time.Now()
	for _, m := range cards {
		if m.Domain == view.Selected.Name && cardInForce(m, now) {
			view.DomainCards = append(view.DomainCards, m)
		}
	}
	view.CardUsage = h.cardBalances(r.Context(), bearer, view.DomainCards)
}

// newDomain renders /dashboard/domains/new: the authoring form for an
// additional domain. A tenant builds its first/default domain through
// Build (draft → activate); this is how it adds more. The form takes
// the domain YAML — the same shape Build emits — and posts it to
// createDomain. ("new" is a reserved slug; a domain literally named
// "new" would be shadowed by this route, which is an acceptable
// edge.)
func (h *DashboardHandler) newDomain(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	view := pages.DashboardNewDomainView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Flash:       convertFlash(flash),
	}
	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to create domains."
		_ = pages.DashboardNewDomain(view).Render(r.Context(), w)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if me, merr := h.api.GetMeBearer(r.Context(), bearer); merr == nil {
		view.Me = me
	} else if errors.Is(merr, apiclient.ErrUnauthorized) {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	_ = pages.DashboardNewDomain(view).Render(r.Context(), w)
}

// createDomain handles POST /dashboard/domains: create an additional
// domain from the posted YAML via the cloud's POST /v1/me/domains. On
// success it flashes and redirects to the new domain's home. A name
// clash (409) or a validation failure (422) re-renders the form with
// the posted YAML preserved so the user does not lose their work.
func (h *DashboardHandler) createDomain(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, "/dashboard/domains/new", http.StatusSeeOther)
		return
	}

	yaml := strings.TrimSpace(r.PostFormValue("yaml"))
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	// Re-render the form with the posted YAML and an error, sharing
	// the Me load so the page chrome stays consistent.
	rerender := func(errMsg string, issues []apiclient.DomainValidationIssue) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		view := pages.DashboardNewDomainView{
			Email:       s.Email,
			Subject:     s.Subject,
			DisplayName: s.DisplayName,
			YAML:        yaml,
			ErrMsg:      errMsg,
			Issues:      issues,
		}
		if me, merr := h.api.GetMeBearer(r.Context(), bearer); merr == nil {
			view.Me = me
		}
		_ = pages.DashboardNewDomain(view).Render(r.Context(), w)
	}

	if yaml == "" {
		rerender("Paste the domain YAML to create a domain.", nil)
		return
	}

	created, err := h.api.CreateDomainBearer(r.Context(), bearer, []byte(yaml))
	if err != nil {
		switch {
		case errors.Is(err, apiclient.ErrUnauthorized):
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		case errors.Is(err, apiclient.ErrDomainExists):
			rerender("A domain with that name already exists. Pick a different domain name, or edit the existing one from its page.", nil)
			return
		case errors.Is(err, apiclient.ErrDomainLimitReached):
			rerender("You've reached your plan's domain limit. Delete a domain or upgrade your plan to add another.", nil)
			return
		default:
			var verr *apiclient.DomainValidationError
			if errors.As(err, &verr) {
				rerender(verr.Message, verr.Issues)
				return
			}
			h.logger.WarnContext(r.Context(), "dashboard: create domain failed", slog.String("err", err.Error()))
			rerender("Could not create the domain: "+err.Error(), nil)
			return
		}
	}

	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashDomainCreated,
		Message: "Created domain “" + created.Domain + "”.",
	})
	http.Redirect(w, r, "/dashboard/domains/"+url.PathEscape(created.Domain), http.StatusSeeOther)
}

// editDomain renders /dashboard/domains/{domain}/edit: the per-domain
// authoring surface. It pre-fills a YAML editor with the domain's
// current contract (GET.../yaml) so a non-default domain can be
// edited in place — the gap left when Slice 2 deferred per-domain
// Build authoring. The form posts back to updateDomain, which PUTs
// the change.
func (h *DashboardHandler) editDomain(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	flash, _ := h.sessions.ReadFlash(w, r)
	view := pages.DashboardEditDomainView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Slug:        slug,
		Flash:       convertFlash(flash),
	}
	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to edit domains."
		_ = pages.DashboardEditDomain(view).Render(r.Context(), w)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if me, merr := h.api.GetMeBearer(r.Context(), bearer); merr == nil {
		view.Me = me
	} else if errors.Is(merr, apiclient.ErrUnauthorized) {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	yaml, err := h.api.GetDomainYAMLBearer(r.Context(), bearer, slug)
	switch {
	case err == nil:
		view.YAML = yaml
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	case errors.Is(err, apiclient.ErrNoDomain):
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "That domain no longer exists."})
		http.Redirect(w, r, "/dashboard/domains", http.StatusSeeOther)
		return
	default:
		view.ErrMsg = "could not load this domain's definition: " + err.Error()
	}
	_ = pages.DashboardEditDomain(view).Render(r.Context(), w)
}

// updateDomain handles POST /dashboard/domains/{domain}/edit: PUTs the
// edited YAML via the cloud's per-domain update. On success it flashes
// and redirects to the domain's home. A validation failure (422) or a
// name change (409 — rename is separate) re-renders the editor with
// the posted YAML preserved.
func (h *DashboardHandler) updateDomain(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	detailPath := "/dashboard/domains/" + url.PathEscape(slug)

	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	yaml := strings.TrimSpace(r.PostFormValue("yaml"))

	rerender := func(errMsg string, issues []apiclient.DomainValidationIssue) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		view := pages.DashboardEditDomainView{
			Email:       s.Email,
			Subject:     s.Subject,
			DisplayName: s.DisplayName,
			Slug:        slug,
			YAML:        yaml,
			ErrMsg:      errMsg,
			Issues:      issues,
		}
		if me, merr := h.api.GetMeBearer(r.Context(), bearer); merr == nil {
			view.Me = me
		}
		_ = pages.DashboardEditDomain(view).Render(r.Context(), w)
	}

	if yaml == "" {
		rerender("Paste the domain YAML to save changes.", nil)
		return
	}

	switch _, err := h.api.UpdateDomainBearer(r.Context(), bearer, slug, []byte(yaml)); {
	case err == nil:
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind:    session.FlashDomainUpdated,
			Message: "Updated domain “" + slug + "”.",
		})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
	case errors.Is(err, apiclient.ErrDomainRenameUnsupported):
		rerender("A domain's name can't change here. Keep the same domain name to edit it in place; renaming is a separate operation.", nil)
	case errors.Is(err, apiclient.ErrNoDomain):
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "That domain no longer exists."})
		http.Redirect(w, r, "/dashboard/domains", http.StatusSeeOther)
	default:
		var verr *apiclient.DomainValidationError
		if errors.As(err, &verr) {
			rerender(verr.Message, verr.Issues)
			return
		}
		h.logger.WarnContext(r.Context(), "dashboard: update domain failed", slog.String("err", err.Error()))
		rerender("Could not save the domain: "+err.Error(), nil)
	}
}

// domainDetail renders /dashboard/domains/{domain}: one domain's full
// view. A slug that doesn't match the tenant's domain renders the
// empty/list surface rather than a hard 404 page.
func (h *DashboardHandler) domainDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	flash, _ := h.sessions.ReadFlash(w, r)
	view := pages.DashboardDomainsView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Flash:       convertFlash(flash),
	}

	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to enable domains."
		_ = pages.DashboardDomains(view).Render(r.Context(), w)
		return
	}

	bearer, me, ok := h.resolveMe(w, r, s, func(v pages.DashboardDomainsView) {
		_ = pages.DashboardDomains(v).Render(r.Context(), w)
	}, view)
	if !ok {
		return
	}
	view.Me = me

	detail, err := h.api.GetDomainDetailBearer(r.Context(), bearer, slug)
	switch {
	case err == nil:
		view.Selected = detail
		h.rememberDomain(w, s, slug) // remember the in-focus domain across tabs
	case errors.Is(err, apiclient.ErrNoDomain):
		// Unknown/stale slug: render the empty/list surface.
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	default:
		view.ErrMsg = "could not load this domain: " + err.Error()
	}

	// Load the tenant's domain list so the page can offer a switcher
	// between domains — only when a domain is in focus, so the
	// unknown-slug case keeps falling back to the empty/list surface
	// rather than this fetch deciding it. Non-fatal.
	if view.Selected != nil {
		if list, lerr := h.api.ListDomainsBearer(r.Context(), bearer); lerr == nil {
			view.Domains = list
		}
	}

	// Fold the Overview's next-action card + activity glance onto the
	// domain home when a domain is in focus.
	h.decorateDomainHome(r, bearer, &view)
	h.decorateDomainTab(r, bearer, &view)

	_ = pages.DashboardDomains(view).Render(r.Context(), w)
}

// splitApprovalsByActor separates the queue into the ones this actor
// requested and a count of the rest.
//
// The remainder is counted rather than discarded. Work waiting on a
// reviewer must not vanish because of a filter, especially one applied
// by the link they followed rather than by them; the panel says how
// much is being kept off the screen and offers to drop the filter.
func splitApprovalsByActor(all []apiclient.PendingApproval, actor string) ([]apiclient.PendingApproval, int) {
	if actor == "" {
		return all, 0
	}
	mine := make([]apiclient.PendingApproval, 0, len(all))
	elsewhere := 0
	for _, a := range all {
		if a.RequestedBy == actor {
			mine = append(mine, a)
			continue
		}
		elsewhere++
	}
	return mine, elsewhere
}

// decorateDomainHome folds the retired Overview's live jobs onto the
// domain home: the contextual next-action card, the activity glance,
// and the verify-the-loop reconciliation. It only does work when a
// domain is in focus (view.Selected set).
//
// The secondary reads are independent given the bearer + domain name, so
// they fan out concurrently rather than running serially — this is the
// most-hit authenticated page. Every load is non-fatal: a failed fetch
// degrades its own section, never the page; results are assembled after
// the group joins, so view is only written from this goroutine.
func (h *DashboardHandler) decorateDomainHome(r *http.Request, bearer string, view *pages.DashboardDomainsView) {
	if view.Selected == nil {
		return
	}
	ctx := r.Context()
	domainName := view.Selected.Name
	needVerify := len(view.Selected.Actions) > 0

	var (
		keyCount         int
		usage            *apiclient.Usage
		proposals        []apiclient.ProposalDecision
		lastReceived     *time.Time
		pendingApprovals int
		evidence         *apiclient.ControlDecisionsResponse
		evidenceErr      error
	)

	tasks := []func(){
		func() {
			if keys, err := h.api.ListKeysBearer(ctx, bearer); err != nil {
				h.logger.WarnContext(ctx, "dashboard domains: list keys failed", slog.String("err", err.Error()))
			} else {
				keyCount = len(keys)
			}
		},
		func() {
			if u, err := h.api.GetDomainUsageBearer(ctx, bearer, domainName); err != nil {
				h.logger.WarnContext(ctx, "dashboard domains: get domain usage failed", slog.String("err", err.Error()))
			} else {
				usage = u
			}
		},
		func() {
			if resp, err := h.api.ListProposalsBearer(ctx, bearer, 20); err != nil {
				h.logger.WarnContext(ctx, "dashboard domains: list proposals failed", slog.String("err", err.Error()))
			} else {
				proposals = resp.Proposals
				lastReceived = resp.LastReceivedAt
			}
		},
		func() {
			if approvals, err := h.api.ListApprovalsBearer(ctx, bearer,
				apiclient.ApprovalsListOptions{Status: "pending", Limit: 50}); err != nil {
				h.logger.WarnContext(ctx, "dashboard domains: list approvals failed", slog.String("err", err.Error()))
			} else {
				pendingApprovals = len(approvals.Approvals)
			}
		},
	}
	if needVerify {
		tasks = append(tasks, func() {
			evidence, evidenceErr = h.api.GetDomainEvidenceBearer(ctx, bearer, domainName,
				apiclient.ControlDecisionsOptions{Limit: 100})
		})
	}

	var wg sync.WaitGroup
	wg.Add(len(tasks))
	for _, task := range tasks {
		go func(fn func()) {
			defer wg.Done()
			fn()
		}(task)
	}
	wg.Wait()

	// Assemble — single goroutine, no races on view.
	view.Usage = usage
	view.LastReceivedAt = lastReceived
	view.NextAction = computeNextAction(pages.DashboardView{
		// Selected != nil ⇒ the tenant has an active domain, so the
		// next-action branches start at key/approvals/proposal readiness.
		Domain:                &apiclient.Domain{Domain: domainName},
		KeyCount:              keyCount,
		Usage:                 usage,
		Proposals:             proposals,
		LastReceivedAt:        lastReceived,
		PendingApprovalsCount: pendingApprovals,
	})

	if needVerify {
		switch {
		case evidenceErr == nil:
			view.Verify = pages.BuildDomainVerify(view.Selected.Actions, evidence.Decisions.Rows)
		case errors.Is(evidenceErr, apiclient.ErrNoDomain):
			// No active control / evidence yet: reconcile against zero
			// observed decisions so every action reads as backed-but-
			// not-yet-exercised rather than as an error.
			view.Verify = pages.BuildDomainVerify(view.Selected.Actions, nil)
		default:
			h.logger.WarnContext(ctx, "dashboard domains: load verify evidence failed",
				slog.String("domain", domainName), slog.String("err", evidenceErr.Error()))
			view.Verify = pages.DomainVerify{Available: false}
		}
	}
}

// decoratePortfolio builds the multi-domain operational rollup: one row
// per domain with its posture, recent-activity count, and derived next
// action, plus the workspace-wide pending-approvals count. Per-domain
// usage reads fan out concurrently (this is the multi-domain landing);
// every read is non-fatal and degrades that row's activity to zero
// rather than failing the page. Rows preserve the domain list order.
func (h *DashboardHandler) decoratePortfolio(r *http.Request, bearer string, view *pages.DashboardDomainsView) {
	ctx := r.Context()
	list := view.Domains
	rows := make([]pages.DomainPortfolioRow, len(list))

	// Shared signals fetched once (not per domain): the guard runtime
	// connections (freshness per domain) and the tenant's pending
	// approvals (attributed per domain by entity below).
	var (
		conns    []apiclient.GuardConnection
		pending  []apiclient.PendingApproval
		sharedWG sync.WaitGroup
	)
	sharedWG.Add(2)
	go func() {
		defer sharedWG.Done()
		if c, err := h.api.ListGuardConnectionsBearer(ctx, bearer); err != nil {
			h.logger.WarnContext(ctx, "portfolio: list guard connections failed", slog.String("err", err.Error()))
		} else {
			conns = c
		}
	}()
	go func() {
		defer sharedWG.Done()
		if appr, err := h.api.ListApprovalsBearer(ctx, bearer,
			apiclient.ApprovalsListOptions{Status: "pending", Limit: 50}); err != nil {
			h.logger.WarnContext(ctx, "portfolio: list approvals failed", slog.String("err", err.Error()))
		} else if appr != nil {
			pending = appr.Approvals
		}
	}()
	sharedWG.Wait()

	now := time.Now()
	approvalsByDomain := approvalsPerDomain(list, pending)
	view.ApprovalsHref = approvalsCalloutHref(list, approvalsByDomain)

	var wg sync.WaitGroup
	wg.Add(len(list))
	for i := range list {
		go func(i int) {
			defer wg.Done()
			d := list[i]
			row := pages.DomainPortfolioRow{Name: d.Name}
			row.State, row.StateClass = portfolioState(d)
			row.Runtime, row.RuntimeClass = portfolioRuntime(conns, d.Name, now)
			row.Href = "/dashboard/domains/" + url.PathEscape(d.Name)
			row.Approvals = approvalsByDomain[strings.ToLower(d.Name)]
			if u, err := h.api.GetDomainUsageBearer(ctx, bearer, d.Name); err != nil {
				h.logger.WarnContext(ctx, "portfolio: domain usage failed",
					slog.String("domain", d.Name), slog.String("err", err.Error()))
			} else if u != nil {
				row.Decisions = int(u.Counters["kiff.cloud.proposals.submitted"])
			}
			row.HasActivity = row.Decisions > 0
			row.NextActionLabel, row.NextActionHref = portfolioNextAction(d, row.HasActivity, row.Approvals)
			rows[i] = row
		}(i)
	}
	wg.Wait()
	pendingTotal := len(pending)

	// Prepend an in-progress draft as a first-class "Draft" row when it
	// isn't already an active domain — the domain being built, alongside
	// the ones already operating. It links to Build (its setup context),
	// not a domain home that doesn't exist yet.
	isActive := func(n string) bool {
		for _, d := range list {
			if d.Name == n {
				return true
			}
		}
		return false
	}
	if name := h.inProgressDraftName(ctx, bearer, isActive); name != "" {
		draftRow := pages.DomainPortfolioRow{
			Name:            name,
			State:           "Draft",
			StateClass:      "dash-portfolio-state-draft",
			Href:            "/dashboard/build",
			Draft:           true,
			NextActionLabel: "Finish domain",
			NextActionHref:  "/dashboard/build",
		}
		rows = append([]pages.DomainPortfolioRow{draftRow}, rows...)
	}

	view.Portfolio = rows
	view.PendingApprovals = pendingTotal
}

// portfolioState maps a domain's posture/status to its portfolio state
// label + styling class. Posture (the mode the decide path honors) leads;
// an unset posture reads as the plain "Active" status.
func portfolioState(d apiclient.DomainListItem) (label, class string) {
	switch d.Posture {
	case "enforce":
		return "Enforcing", "dash-portfolio-state-enforce"
	case "observe":
		return "Observe", "dash-portfolio-state-observe"
	default:
		return "Active", "dash-portfolio-state-active"
	}
}

// portfolioNextAction derives the one next thing for a domain row.
// Pending approvals win (a human is blocking real traffic); otherwise an
// unexercised domain points at connecting a runtime, and an active one at
// its Activity.
func portfolioNextAction(d apiclient.DomainListItem, hasActivity bool, approvals int) (label, href string) {
	base := "/dashboard/domains/" + url.PathEscape(d.Name)
	if approvals > 0 {
		return fmt.Sprintf("Review %d %s", approvals, pluralizeWord(approvals, "approval", "approvals")),
			base + "/evidence?tab=approvals"
	}
	if !hasActivity {
		return "Connect a runtime", "/dashboard/runtimes"
	}
	return "Open activity", base + "/evidence"
}

// pluralizeWord picks singular/plural by count (handler-side; the page
// has its own templ pluralize).
func pluralizeWord(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// portfolioRuntime derives a domain's runtime freshness from the guard
// connections whose workflow maps to the domain: the freshest matching
// connection's last-seen decides Live (< 1h) / Connected (< 24h) / Idle
// (older). No matching connection ⇒ empty (rendered as "—"). The
// mapping mirrors the observed-runtime match: a connection's workflow
// (or its derived domain slug) equal to the domain name.
func portfolioRuntime(conns []apiclient.GuardConnection, domainName string, now time.Time) (label, class string) {
	var latest time.Time
	for _, c := range conns {
		if !connectionMatchesDomain(c, domainName) {
			continue
		}
		if c.LastSeenAt.After(latest) {
			latest = c.LastSeenAt
		}
	}
	if latest.IsZero() {
		return "", ""
	}
	age := now.Sub(latest)
	switch {
	case age < time.Hour:
		return "Live", "dash-portfolio-runtime-live"
	case age < 24*time.Hour:
		return "Connected", "dash-portfolio-runtime-connected"
	default:
		return "Idle", "dash-portfolio-runtime-idle"
	}
}

// connectionMatchesDomain reports whether a guard connection backs the
// named domain, by its workflow or its derived domain slug.
func connectionMatchesDomain(c apiclient.GuardConnection, domainName string) bool {
	return strings.EqualFold(strings.TrimSpace(c.Workflow), domainName) ||
		strings.EqualFold(guardDraftDomain(c), domainName)
}

// approvalsCalloutHref resolves where the workspace approvals callout
// should point: the approvals tab of the domain holding the most pending
// approvals, so "Review" lands on an actionable queue rather than
// bouncing back to the portfolio. Falls back to the first domain when no
// approval attributes to a domain by entity (a rare mismatch), so the
// link still reaches an approvals surface. Empty when there are no
// domains at all.
func approvalsCalloutHref(list []apiclient.DomainListItem, byDomain map[string]int) string {
	if len(list) == 0 {
		return ""
	}
	best, bestCount := "", 0
	for _, d := range list {
		if c := byDomain[strings.ToLower(d.Name)]; c > bestCount {
			best, bestCount = d.Name, c
		}
	}
	if best == "" {
		best = list[0].Name // nothing attributed; land on an approvals tab anyway
	}
	return "/dashboard/domains/" + url.PathEscape(best) + "/evidence?tab=approvals"
}

// approvalsPerDomain buckets pending approvals to domains by entity type
// (the approval read carries the entity, and each domain declares its
// entity in the list). Keyed by lower-cased domain name. Approvals whose
// entity matches no domain are left unattributed (still counted in the
// workspace total).
func approvalsPerDomain(list []apiclient.DomainListItem, pending []apiclient.PendingApproval) map[string]int {
	byEntity := make(map[string]string, len(list)) // lower(entity) -> lower(domain name)
	for _, d := range list {
		if e := strings.ToLower(strings.TrimSpace(d.Entity)); e != "" {
			byEntity[e] = strings.ToLower(d.Name)
		}
	}
	out := make(map[string]int)
	for _, a := range pending {
		if dom, ok := byEntity[strings.ToLower(strings.TrimSpace(a.EntityType))]; ok {
			out[dom]++
		}
	}
	return out
}

// resolveMe refreshes the bearer and loads Me, sharing the auth-failure
// and api-error handling every domains route needs. On a recoverable
// api error it renders the page (via render) with an ErrMsg set and
// returns ok=false; on auth failure it redirects and returns ok=false.
func (h *DashboardHandler) resolveMe(
	w http.ResponseWriter, r *http.Request, s session.Session,
	render func(pages.DashboardDomainsView), base pages.DashboardDomainsView,
) (string, *apiclient.Me, bool) {
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard domains: refresh bearer failed", slog.String("err", err.Error()))
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return "", nil, false
	}
	me, err := h.api.GetMeBearer(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return "", nil, false
		}
		base.ErrMsg = "could not reach the api: " + err.Error()
		render(base)
		return "", nil, false
	}
	return bearer, me, true
}

// deleteDomain handles POST /dashboard/domains/{domain}/delete: a
// guarded delete of an additional domain. The form requires the user
// to type the domain name to confirm (mirrors the account-delete
// pattern), validated server-side. On success it flashes and
// redirects to the apex; the default domain (409) and a stale slug
// (404) flash a precise message. Mismatched confirmation never calls
// the API.
func (h *DashboardHandler) deleteDomain(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	detailPath := "/dashboard/domains/" + url.PathEscape(slug)

	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	// Server-side typed confirmation — no JS required.
	if confirm := strings.TrimSpace(r.PostFormValue("confirm_name")); !strings.EqualFold(confirm, slug) {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind:    session.FlashError,
			Message: "Domain name didn't match. Type the domain's name exactly to confirm deletion.",
		})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
		return
	}

	switch err := h.api.DeleteDomainBearer(r.Context(), bearer, slug); {
	case err == nil:
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind:    session.FlashDomainDeleted,
			Message: "Deleted domain “" + slug + "”.",
		})
		http.Redirect(w, r, "/dashboard/domains", http.StatusSeeOther)
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
	case errors.Is(err, apiclient.ErrDomainIsDefault):
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind:    session.FlashError,
			Message: "This is your default domain; it can't be deleted. Delete your other domains first.",
		})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
	case errors.Is(err, apiclient.ErrNoDomain):
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "That domain no longer exists.",
		})
		http.Redirect(w, r, "/dashboard/domains", http.StatusSeeOther)
	default:
		h.logger.WarnContext(r.Context(), "dashboard: delete domain failed", slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "Could not delete the domain: " + err.Error(),
		})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
	}
}

// setDomainMode handles POST /dashboard/domains/{domain}/mode: the
// per-domain governance posture flip (observe ↔ enforce). It PUTs the
// requested mode via the cloud's management-gated
// /v1/me/domains/{domain}/mode and redirects back to the domain home
// with a flash. A runtime-role caller (403) gets a precise message;
// the change is honored on the decide path immediately after.
func (h *DashboardHandler) setDomainMode(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	detailPath := "/dashboard/domains/" + url.PathEscape(slug)

	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	mode := strings.ToLower(strings.TrimSpace(r.PostFormValue("mode")))
	if mode != "observe" && mode != "enforce" {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "Posture must be observe or enforce.",
		})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
		return
	}

	switch err := h.api.SetDomainModeBearer(r.Context(), bearer, slug, mode); {
	case err == nil:
		msg := "Governance posture set to enforce — decisions now bind."
		if mode == "observe" {
			msg = "Governance posture set to observe — decisions are advisory (recorded, not enforced)."
		}
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashDomainUpdated, Message: msg})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
	case errors.Is(err, apiclient.ErrForbidden):
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind:    session.FlashError,
			Message: "Changing enforcement needs a management role; a runtime key can't flip its own posture.",
		})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
	case errors.Is(err, apiclient.ErrNoDomain):
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "That domain no longer exists.",
		})
		http.Redirect(w, r, "/dashboard/domains", http.StatusSeeOther)
	default:
		h.logger.WarnContext(r.Context(), "dashboard: set domain mode failed", slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "Could not change the posture: " + err.Error(),
		})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
	}
}

// domainEvidence renders /dashboard/domains/{domain}/evidence: the
// Control Room reframed as the domain's evidence view, scoped to the
// domain and reached from it rather than as a top-level "your one
// control" page. Consumes the domain-keyed evidence route and reuses
// the existing Control Room partials verbatim.
func (h *DashboardHandler) domainEvidence(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	flash, _ := h.sessions.ReadFlash(w, r)
	// Which evidence sub-view to show. The Activity page is organized into
	// tabs (server-side ?tab=, no JS) so it reads as a focused dashboard
	// instead of one long scroll; unknown/empty falls back to decisions.
	tab := strings.TrimSpace(r.URL.Query().Get("tab"))
	switch tab {
	case "receipts", "approvals", "alerts", "about":
	default:
		tab = "decisions"
	}
	// Decisions-tab paging + outcome filter (slice 2). Both only shape the
	// Decisions panel, so they're applied to the evidence query only when
	// that tab is active — otherwise the shared payload (whose receipts are
	// tied to the returned decision rows) would be distorted for the other
	// tabs.
	const evidencePageSize = 20
	page := 1
	if n, perr := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page"))); perr == nil && n > 1 {
		page = n
	}
	outcome := strings.TrimSpace(r.URL.Query().Get("outcome"))
	switch outcome {
	case "allowed", "blocked", "approval_required", "limit_exceeded", "invalid":
	default:
		outcome = ""
	}
	// ?actor= narrows the whole view to one proposing identity, and
	// ?card= says which card sent the reader here so the page can lead
	// back to it. Both are echoes of what the caller asked for: the
	// filter is applied by the api, and an actor that proposed nothing
	// renders as an empty timeline that says which agent it is empty
	// for.
	actor := strings.TrimSpace(r.URL.Query().Get("actor"))
	view := pages.DashboardDomainEvidenceView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		DomainName:  slug,
		Tab:         tab,
		Page:        page,
		Outcome:     outcome,
		Actor:       actor,
		Card:        strings.TrimSpace(r.URL.Query().Get("card")),
		PageSize:    evidencePageSize,
		Flash:       convertFlash(flash),
	}

	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to enable domain evidence."
		_ = pages.DashboardDomainEvidence(view).Render(r.Context(), w)
		return
	}

	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard domain evidence: refresh bearer failed", slog.String("err", err.Error()))
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	me, err := h.api.GetMeBearer(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		}
		view.ErrMsg = "could not reach the api: " + err.Error()
		_ = pages.DashboardDomainEvidence(view).Render(r.Context(), w)
		return
	}
	view.Me = me

	// Evidence window selector (7 / 30 / 90 days), wired to the API's
	// existing ?from=&?to=. An explicit ?window= is remembered; otherwise
	// the last selection is restored so the window survives a hop to
	// another tab and back.
	winFrom, winTo, winPreset, winPersist := effectiveWindow(r, s, time.Now().UTC())
	view.WindowOptions = evidenceWindowOptions(r, winPreset)
	view.Window = winPreset

	evOpts := apiclient.ControlDecisionsOptions{Limit: evidencePageSize, From: winFrom, To: winTo, Actor: actor}
	if tab == "decisions" {
		evOpts.Offset = (page - 1) * evidencePageSize
		if outcome != "" {
			evOpts.Outcomes = []string{outcome}
		}
	}
	timeline, err := h.api.GetDomainEvidenceBearer(r.Context(), bearer, slug, evOpts)
	switch {
	case err == nil:
		view.Evidence = pages.ControlEvidenceFromAPI(timeline)
		// Remember the domain + any explicit window in one write, so the
		// scope survives a hop through a non-domain tab.
		h.rememberScope(w, s, slug, winPersist)
		// The review ratio over the same window AND the same control as
		// the counts it sits above. It has to be fetched here rather
		// than earlier, because the control id only exists once the
		// evidence resolves — and a tenant-wide ratio above
		// domain-scoped tiles is a number that does not reconcile with
		// the one underneath it, which is the fastest way to lose a
		// reader's trust in both.
		//
		// Read from the api rather than derived from the counts below,
		// so the rules that make the figure honest live in one place. A
		// failure leaves it nil and the panel absent: a confident
		// percentage from a failed call is the worst thing this page
		// could show.
		// Scoped to the same actor as the rows and the tiles. A
		// domain-wide ratio above one agent's rows is a number that
		// does not reconcile with what is under it, which is the same
		// failure the control scope exists to avoid one level up.
		if ratio, rerr := h.api.ReviewRatioBearer(
			r.Context(), bearer, view.Evidence.Control.ID, actor, winFrom, winTo,
		); rerr == nil {
			view.Ratio = ratio
		} else {
			h.logger.WarnContext(r.Context(), "dashboard domain evidence: review ratio unavailable",
				slog.String("domain", slug), slog.String("err", rerr.Error()))
		}
	case errors.Is(err, apiclient.ErrNoDomain):
		// Unknown slug or no active control yet → honest empty state.
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	default:
		h.logger.WarnContext(r.Context(), "dashboard domain evidence: load failed",
			slog.String("domain", slug), slog.String("err", err.Error()))
		view.ErrMsg = "could not load this domain's evidence: " + err.Error()
	}

	// Requests-over-time series for the Decisions-tab chart. Only fetched
	// when the Decisions tab is showing (that's where the chart mounts)
	// and evidence resolved (a real domain/control). Non-fatal: a failed
	// or empty series just hides the chart behind its empty state.
	if tab == "decisions" && view.Evidence != nil {
		if series, serr := h.api.GetActivitySeriesBearer(r.Context(), bearer, slug,
			apiclient.ActivitySeriesOptions{From: winFrom, To: winTo, Actor: actor}); serr != nil {
			if !errors.Is(serr, apiclient.ErrNoDomain) {
				h.logger.WarnContext(r.Context(), "dashboard domain evidence: load series failed",
					slog.String("domain", slug), slog.String("err", serr.Error()))
			}
		} else {
			view.Series = series
		}
	}

	// Fold the actionable approval queue onto the Activity view.
	// Tenant-scoped today (0-or-1 domain), so this is exactly this
	// domain's pending approvals. Non-fatal: a failed fetch leaves the
	// queue empty and the read-only evidence section still renders.
	if list, aerr := h.api.ListApprovalsBearer(r.Context(), bearer,
		apiclient.ApprovalsListOptions{Status: "pending"}); aerr != nil {
		h.logger.WarnContext(r.Context(), "dashboard domain evidence: list approvals failed",
			slog.String("domain", slug), slog.String("err", aerr.Error()))
	} else {
		// Filtered with the rest of the page. An approval records the
		// actor that requested it (RequestedBy is stamped from the
		// proposal's actor), so the queue can answer for one agent —
		// and a page headed "showing only what A proposed" that lists
		// B's pending work contradicts itself, in the tab badge as
		// well as in the panel.
		view.PendingApprovals, view.PendingElsewhere = splitApprovalsByActor(list.Approvals, actor)
	}

	_ = pages.DashboardDomainEvidence(view).Render(r.Context(), w)
}
