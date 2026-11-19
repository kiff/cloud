package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/notify"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// WithChrome enriches dashboard requests with the shared shell's
// context data — the notification summary (topbar bell) and the domain
// selector model (sidebar) — stashed on the context so every surface
// renders them, including Studio and the device page, which don't go
// through renderTab.
//
// Placed INSIDE RequireSession (which puts the session on the context)
// and OUTSIDE the route mux. Best-effort: any failure — no session, no
// bearer, an API error — leaves the data absent, and the shell renders
// its quiet/empty states. Every method is enriched: most POST handlers
// redirect, but some render the shell (step 2 of /tools/new, a form shown
// again with its error), and those must show the account's own sidebar.
// Only a GET is sent to /welcome.
//
// Cost note: this adds a few read calls (approvals + connections + prefs
// + domains + draft) per request, run in parallel, partially redundant with
// renderTab's own fetches. Coalescing behind one endpoint is a
// follow-up. The bearer minted here is stashed on the context so the
// page handler reuses it instead of minting a second one.
func (h *DashboardHandler) WithChrome(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every method, not only GET: a POST that renders a page (step 2
		// of /tools/new, a form shown again with its error) draws the same
		// sidebar, so it needs the account kind and the bell too.
		if h.api == nil {
			next.ServeHTTP(w, r)
			return
		}
		s, ok := SessionFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		bearer, err := h.bearers.BearerFor(r.Context(), s)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := withRequestBearer(r.Context(), bearer)
		// The bell and the sidebar are independent reads; fetched one
		// after the other they added their latencies to every page.
		var summary notify.Summary
		var nav components.DomainNav
		var kind string
		var caps []string
		var zone string
		kindRead := false
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); summary = h.composeNotifications(ctx, bearer) }()
		go func() { defer wg.Done(); nav = h.composeDomainNav(ctx, bearer, s, r) }()
		go func() {
			defer wg.Done()
			if me, err := h.api.GetMeBearer(ctx, bearer); err == nil {
				kind, kindRead = me.AccountKind, true
				caps = me.Capabilities
				zone = me.TimeZone
			}
		}()
		wg.Wait()
		// An account whose owner has not said what it is for is asked once.
		// Only when the read worked: an API hiccup must not trap anyone on
		// the question. Only someone who can answer is asked: an invited
		// editor, approver or viewer cannot set it.
		canAnswer := components.Can(components.WithCapabilities(ctx, caps), "account.settings")
		if r.Method == http.MethodGet && kindRead && kind == "" && canAnswer && asksAccountKind(r.URL.Path) {
			http.Redirect(w, r, "/welcome?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		ctx = components.WithAccountKind(ctx, kind)
		// What this person's role allows, so pages hide the controls
		// it doesn't; the API refuses them regardless.
		ctx = components.WithCapabilities(ctx, caps)
		// Times on the page read in the account's time zone.
		ctx = components.WithTimeZone(ctx, zone)
		summary.ClearedAt = s.NotificationsClearedAt
		ctx = notify.WithSummary(ctx, summary)
		ctx = components.WithDomainNav(ctx, nav)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// composeDomainNav builds the sidebar domain-selector model from the
// tenant's domain-apex list. Best-effort: a failed list yields an empty
// selector (the "no domain yet" state).
//
// Current is derived from the domain BEING VIEWED this request (the slug
// in the URL) first, falling back to the remembered LastDomain. This
// matters: LastDomain is written by the handler (rememberDomain) AFTER
// this middleware has already read the session, so relying on it alone
// leaves the selector one navigation behind — it would show the previous
// domain until the cookie caught up. Reading the path keeps the selector
// in sync with the page on the same request.
func (h *DashboardHandler) composeDomainNav(ctx context.Context, bearer string, s session.Session, r *http.Request) components.DomainNav {
	// The draft read doesn't depend on the domain list, only the
	// decision about it does, so it runs alongside the list.
	draftName := make(chan string, 1)
	go func() { draftName <- h.loadDraftName(ctx, bearer) }()
	list, err := h.api.ListDomainsBearer(ctx, bearer)
	if err != nil {
		h.logger.WarnContext(ctx, "chrome: list domains failed", slog.String("err", err.Error()))
		return components.DomainNav{}
	}
	items := make([]components.DomainNavItem, 0, len(list))
	inList := func(name string) bool {
		for _, d := range list {
			if d.Name == name {
				return true
			}
		}
		return false
	}
	for _, d := range list {
		items = append(items, components.DomainNavItem{Slug: d.Name, Name: d.Name, Status: d.Status, Posture: d.Posture})
	}
	// Surface an in-progress draft as a first-class "Draft" entry when it
	// isn't already an active domain (a draft that shadows an active one
	// is an edit-in-progress, not a separate domain). Best-effort: a
	// missing draft or a parse failure just omits the entry.
	if name := <-draftName; name != "" && !inList(name) {
		items = append(items, components.DomainNavItem{Slug: name, Name: name, Draft: true})
	}
	return components.DomainNav{Current: currentDomainSlug(r, s.LastDomain, inList), Items: items}
}

// inProgressDraftName returns the domain name of a draft that is not yet
// an active domain — the in-progress domain being built. Empty when there
// is no draft, the draft can't be resolved to a name, or the draft's
// domain is already active (an edit-in-progress, not a new domain).
func (h *DashboardHandler) inProgressDraftName(ctx context.Context, bearer string, isActive func(string) bool) string {
	name := h.loadDraftName(ctx, bearer)
	if name == "" || isActive(name) {
		return ""
	}
	return name
}

// loadDraftName returns the domain name the tenant's draft resolves to,
// or empty when there is no draft or it can't be read.
func (h *DashboardHandler) loadDraftName(ctx context.Context, bearer string) string {
	draft, err := h.api.GetDraftBearer(ctx, bearer)
	if err != nil {
		if !errors.Is(err, apiclient.ErrDraftNotFound) {
			h.logger.WarnContext(ctx, "chrome: load draft failed", slog.String("err", err.Error()))
		}
		return ""
	}
	return draftDomainName(draft)
}

// currentDomainSlug resolves which domain the sidebar selector should
// mark as current for this request: the slug in the URL of a
// domain-scoped page wins (so the selector matches the page immediately,
// not one navigation behind), otherwise the remembered LastDomain. Only
// a slug that matches a live domain is returned.
func currentDomainSlug(r *http.Request, lastDomain string, inList func(string) bool) string {
	path := r.URL.Path
	// /dashboard/domains/{slug}[/edit|/evidence]
	if rest, ok := strings.CutPrefix(path, "/dashboard/domains/"); ok {
		slug := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			slug = rest[:i]
		}
		if slug != "" && slug != "new" && inList(slug) {
			return slug
		}
	}
	// /dashboard/govern?domain={slug}
	if path == "/dashboard/govern" {
		if d := strings.TrimSpace(r.URL.Query().Get("domain")); d != "" && inList(d) {
			return d
		}
	}
	if lastDomain != "" && inList(lastDomain) {
		return lastDomain
	}
	return ""
}

// composeNotifications fetches the v1 notification sources (pending
// approvals + guard connections) and composes the feed. Each source is
// best-effort: a failing source is logged and skipped, never fatal.
func (h *DashboardHandler) composeNotifications(ctx context.Context, bearer string) notify.Summary {
	// Three independent reads, fetched in parallel; each writes only its
	// own variable.
	var (
		approvals []apiclient.PendingApproval
		conns     []apiclient.GuardConnection
		prefs     notify.Prefs
		held      []apiclient.CardException
		wg        sync.WaitGroup
	)
	wg.Add(4)
	go func() {
		defer wg.Done()
		// Actions outside a card, waiting for the owner (the mandates
		// design amendment). Best-effort like the others: the Needs
		// you page is the authoritative list.
		if es, err := h.api.ListCardExceptionsBearer(ctx, bearer, "held"); err != nil {
			h.logger.WarnContext(ctx, "notifications: list card exceptions failed", slog.String("err", err.Error()))
		} else {
			held = es
		}
	}()
	go func() {
		defer wg.Done()
		if list, err := h.api.ListApprovalsBearer(ctx, bearer,
			apiclient.ApprovalsListOptions{Status: "pending", Limit: 50}); err != nil {
			h.logger.WarnContext(ctx, "notifications: list approvals failed", slog.String("err", err.Error()))
		} else if list != nil {
			approvals = list.Approvals
		}
	}()
	go func() {
		defer wg.Done()
		if c, err := h.api.ListGuardConnectionsBearer(ctx, bearer); err != nil {
			h.logger.WarnContext(ctx, "notifications: list connections failed", slog.String("err", err.Error()))
		} else {
			conns = c
		}
	}()
	go func() {
		defer wg.Done()
		// Per-rule prefs gate which kinds surface. Best-effort: a failed
		// fetch falls back to nil (default-on), so a prefs outage never
		// silences notifications.
		if p, err := h.api.GetNotificationPrefsBearer(ctx, bearer); err != nil {
			h.logger.WarnContext(ctx, "notifications: get prefs failed", slog.String("err", err.Error()))
		} else {
			prefs = notify.Prefs(p)
		}
	}()
	wg.Wait()

	sum := notify.Compose(approvals, conns, prefs, time.Now().UTC())
	// A separate request keeps the legacy feed usable while the API rolls out.
	// A missing queue is never represented as "no open work" on its own page.
	if feed, err := h.api.AttentionBearer(ctx, bearer); err == nil {
		sum = notify.Compose(nil, conns, prefs, time.Now().UTC())
		sum.Durable = true
		for _, item := range feed.Items {
			if item.Kind == "approval" && !prefs.Enabled(notify.RuleApproval) {
				continue
			}
			if item.Kind == "card_warning" {
				sum.Items = append(sum.Items, notify.Item{ID: item.ID, Revision: item.Revision, Persistent: true, Seen: !item.Unread, Kind: notify.KindBudget, Severity: notify.SeverityWarn, Title: "Card budget warning", Subtitle: item.Reason, Href: "/needs-you?tab=warnings", ActionLabel: "See warnings", At: item.CreatedAt, Actionable: false})
				continue
			}
			title := item.Action + " was blocked"
			href := "/needs-you?tab=blocked#" + url.PathEscape(item.ID)
			if item.Kind == "approval" {
				title = item.Action + " is waiting for approval"
				href = "/needs-you#" + url.PathEscape(item.ID)
			}
			sum.Items = append(sum.Items, notify.Item{ID: item.ID, Revision: item.Revision, Persistent: true, Seen: !item.Unread,
				Kind: notify.KindApproval, Severity: notify.SeverityWarn, Title: title, Subtitle: item.ActorID, Scope: item.DomainID,
				Href: href, ActionLabel: "Review", At: item.CreatedAt, Actionable: true})
			sum.Count++
		}
	}
	for _, e := range held {
		sum.Items = append(sum.Items, notify.Item{ID: e.ID, Kind: notify.KindApproval, Severity: notify.SeverityWarn,
			Title: "Outside a card: " + pages.ExceptionSummary(e), Subtitle: "waiting for your answer",
			Href: pages.NeedsYouHref(e.ID), ActionLabel: "Answer", At: e.CreatedAt, Since: e.CreatedAt, Actionable: true})
		sum.Count++
	}
	return sum
}

// notifications renders GET /dashboard/notifications: the full,
// filterable feed. Reuses renderTab for the shared chrome + view, and
// injects the active ?filter= into the view via the render closure. The
// feed itself is read from the context summary the middleware composed.
func (h *DashboardHandler) notifications(w http.ResponseWriter, r *http.Request) {
	filter := normalizeNotificationsFilter(r.URL.Query().Get("filter"))
	h.renderTab(w, r, func(v pages.DashboardView) templ.Component {
		v.NotificationsFilter = filter
		return pages.DashboardNotifications(v)
	})
}

// normalizeNotificationsFilter validates the ?filter= value against the
// known kinds; anything else falls back to "" (all).
func normalizeNotificationsFilter(raw string) string {
	switch raw {
	case string(notify.KindApproval), string(notify.KindRuntime):
		return raw
	default:
		return ""
	}
}

// notificationSettings renders GET /dashboard/notifications/settings —
// the per-rule preferences. Fetches the current prefs and renders the
// toggle rows.
func (h *DashboardHandler) notificationSettings(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, notificationSettingsHref, http.StatusSeeOther)
}

// notificationSettingsHref is Settings › Notifications.
const notificationSettingsHref = "/dashboard/settings?tab=notifications"

// updateNotificationSettings handles POST /dashboard/notifications/settings:
// flip one rule to the submitted target state, persist, and redirect back
// with a flash. Merges onto the current prefs so the other rules are
// preserved.
func (h *DashboardHandler) updateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	const back = notificationSettingsHref
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	rule := strings.TrimSpace(r.PostFormValue("rule"))
	if rule != notify.RuleApproval && rule != notify.RuleRuntimeIdle && rule != notify.RuleHeldEmail {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "unknown notification rule"})
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	enabled := r.PostFormValue("enabled") == "true"

	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	// Merge onto current prefs so flipping one rule preserves the others.
	prefs, err := h.api.GetNotificationPrefsBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "notification settings: get prefs failed", slog.String("err", err.Error()))
		prefs = map[string]bool{}
	}
	if prefs == nil {
		prefs = map[string]bool{}
	}
	prefs[rule] = enabled
	if _, err := h.api.PutNotificationPrefsBearer(r.Context(), bearer, prefs); err != nil {
		h.logger.WarnContext(r.Context(), "notification settings: put prefs failed", slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "could not save notification settings: " + err.Error()})
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashNotificationSaved, Message: "Notification settings updated."})
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// clearNotifications handles POST /dashboard/notifications/clear: stamp
// the session so the bell hides everything up to now, then return to the
// page the user was on. Nothing upstream changes; the notifications page
// still lists every item.
func (h *DashboardHandler) clearNotifications(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	seen := map[string]int64{}
	for key, values := range r.PostForm {
		if !strings.HasPrefix(key, "seen:") {
			continue
		}
		if len(values) != 1 {
			http.Error(w, "Invalid revision", 400)
			return
		}
		revision, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || revision < 1 {
			http.Error(w, "Invalid revision", 400)
			return
		}
		seen[strings.TrimPrefix(key, "seen:")] = revision
	}
	if len(seen) > 0 {
		bearer, err := h.bearer(r.Context(), s)
		if err != nil {
			http.Error(w, "Could not authenticate", 401)
			return
		}
		if err := h.api.SeeAttentionBearer(r.Context(), bearer, seen); err != nil {
			http.Error(w, "Could not mark notifications seen. Please retry.", 503)
			return
		}
	}
	s.NotificationsClearedAt = time.Now().UTC()
	_ = h.sessions.Issue(w, s)
	http.Redirect(w, r, clearNotificationsReturn(r.Referer()), http.StatusSeeOther)
}

// clearNotificationsReturn sends the user back to the dashboard page
// they cleared from. Only the path and query of the Referer are kept (the
// host is dropped, and a "//" path is refused), so the redirect never
// leaves the app.
func clearNotificationsReturn(referer string) string {
	u, err := url.Parse(referer)
	if err != nil || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/dashboard"
	}
	if u.RawQuery != "" {
		return u.Path + "?" + u.RawQuery
	}
	return u.Path
}
