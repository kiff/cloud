package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// MeterReader is what the usage read needs of the meter.
type MeterReader interface {
	Totals(ctx context.Context, tenantID string, from, to time.Time) (map[meter.Unit]meter.Totals, error)
	Tasks(ctx context.Context, tenantID, agent string, scope *meter.TaskScope, from, to time.Time, page *meter.TaskPage, limit int) ([]meter.Task, *meter.TaskPage, error)
	// Tafs lists the account's tafs that aren't archived, so each has its
	// line in the read even with nothing this period.
	Tafs(ctx context.Context, tenantID string) ([]string, error)
	// IsAgent reports whether id is one of the account's agents.
	IsAgent(ctx context.Context, tenantID, id string) (bool, error)
	// ByPerson counts what each person's runs counted.
	ByPerson(ctx context.Context, tenantID string, from, to time.Time) ([]meter.PersonCount, error)
}

// UsageGroups is what usage by group reads of the account's groups; the
// access store is one.
type UsageGroups interface {
	Groups(ctx context.Context, tenantID string) ([]access.Group, error)
	Members(ctx context.Context, tenantID, group string) ([]access.GroupMember, error)
}

// UsagePeople lists the account's people, so usage by person names each one,
// those whose tafs got nothing done too; the membership store is one.
type UsagePeople interface {
	List(ctx context.Context, tenantID string) ([]membership.Member, error)
}

// PeopleCountedSince is when KIFF began recording who asked for each run:
// tasks counted before it aren't tied to a person.
var PeopleCountedSince = time.Date(2026, 10, 27, 14, 37, 0, 0, time.UTC)

// MeterUsageHandler serves the account's two meters, as the tafs app and
// the dashboard show them (the shape agreed there):
//
//	GET /v1/me/usage                 the period's tasks (included, packs, used, left) and actions; who bills the plan
//	GET /v1/me/usage/tasks           each counted task, by its Record id; ?agent=<id> for one taf's
//	PUT /v1/me/usage/ceiling         retired (410): tasks past the included ones come from packs
//	PUT /v1/me/usage/ai              one more go-on past the AI fair-use bound, this period; signed
//	GET /v1/me/usage/people          what each person's and each group's runs counted
//
// Counts only, never money: prices are the stores' (Stripe, App Store).
type MeterUsageHandler struct {
	tenants tenant.Store
	meter   MeterReader
	titles  GatewayToolTitles // optional; each counted task's tool title, as the Record shows it
	names   UsageAgentNames   // optional; each agent's name, from its record
	// packs, the pack tasks; catalog, the pack table. Nil: no packs.
	packs   packs.Ledger
	catalog packs.Catalog
	// requests, the members' requests for packs; nil: none.
	requests packs.Requests
	// bought, which packs an account has bought; nil: a pack sold once per
	// account isn't listed at all.
	bought packs.BoughtRead
	// apple, the accounts' appAccountTokens; nil: no in-app purchases.
	apple appstore.Accounts
	// google: Google Play billing is set up, so the store lists its
	// product ids. False: they're absent, and Android offers nothing.
	google bool
	// groups, the account's groups for usage by group; nil: by person only.
	groups UsageGroups
	// members, the account's people, each listed; nil: only those with usage.
	members UsagePeople
	// ownUse keeps each person's counted tasks to their part of the account.
	// Nil: everyone reads all of them.
	ownUse *OwnUse
	logger *slog.Logger
	now    func() time.Time
}

// AppleManageURL is where an Apple-billed account manages its subscription.
const AppleManageURL = "https://apps.apple.com/account/subscriptions"

// GoogleManageURL is where a Google Play-billed account manages its
// subscription.
const GoogleManageURL = "https://play.google.com/store/account/subscriptions"

// WithPacks adds the account's pack tasks from ledger and the pack table.
func (h *MeterUsageHandler) WithPacks(ledger packs.Ledger, catalog packs.Catalog) *MeterUsageHandler {
	h.packs, h.catalog = ledger, catalog
	return h
}

// WithBought reads which packs an account has bought from b: a pack sold
// once per account leaves the store's list once it has.
func (h *MeterUsageHandler) WithBought(b packs.BoughtRead) *MeterUsageHandler {
	h.bought = b
	return h
}

// WithRequests adds the members' requests for packs.
func (h *MeterUsageHandler) WithRequests(r packs.Requests) *MeterUsageHandler {
	h.requests = r
	return h
}

// WithPeople lists every person on the account in usage by person.
func (h *MeterUsageHandler) WithPeople(p UsagePeople) *MeterUsageHandler {
	h.members = p
	return h
}

// WithGroups counts usage by group too.
func (h *MeterUsageHandler) WithGroups(g UsageGroups) *MeterUsageHandler {
	h.groups = g
	return h
}

// WithApple adds the account's appAccountToken and the app's store ids.
func (h *MeterUsageHandler) WithApple(a appstore.Accounts) *MeterUsageHandler {
	h.apple = a
	return h
}

// WithGoogle lists Google Play's product ids in the store: Google Play
// billing is set up.
func (h *MeterUsageHandler) WithGoogle() *MeterUsageHandler {
	h.google = true
	return h
}

// NewMeterUsageHandler serves the usage read from tenants and m.
func NewMeterUsageHandler(tenants tenant.Store, m MeterReader, logger *slog.Logger) *MeterUsageHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &MeterUsageHandler{tenants: tenants, meter: m, logger: logger, now: time.Now}
}

// WithToolTitles names each counted task's tool the way the Record does.
// Without it, a task has its tool's name only.
func (h *MeterUsageHandler) WithToolTitles(t GatewayToolTitles) *MeterUsageHandler {
	h.titles = t
	return h
}

// UsageAgentNames is the account's agents, for the name each is read by.
type UsageAgentNames interface {
	List(ctx context.Context, tenantID string) ([]agents.Agent, error)
}

// WithAgentNames names each agent in usage and each counted task's agent
// by its record, so no app keeps its own name for one.
func (h *MeterUsageHandler) WithAgentNames(a UsageAgentNames) *MeterUsageHandler {
	h.names = a
	return h
}

// agentNames is the account's agents' names, by id; best effort: none
// when they can't be read, and the app says the ids.
func (h *MeterUsageHandler) agentNames(ctx context.Context, tenantID string) map[string]string {
	out := map[string]string{}
	if h.names == nil {
		return out
	}
	as, err := h.names.List(ctx, tenantID)
	if err != nil {
		h.logger.WarnContext(ctx, "usage: agent names unreadable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return out
	}
	for _, a := range as {
		if a.Name != "" {
			out[a.ID] = a.Name
		}
	}
	return out
}

// WithClock sets the clock, for tests.
func (h *MeterUsageHandler) WithClock(now func() time.Time) *MeterUsageHandler {
	h.now = now
	return h
}

// Register mounts the routes.
func (h *MeterUsageHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/usage", h.usage)
	mux.HandleFunc("GET /v1/me/usage/tasks", h.tasks)
	mux.HandleFunc("PUT /v1/me/usage/ceiling", h.setCeiling)
	mux.HandleFunc("PUT /v1/me/usage/ai", h.setAIGoOn)
	mux.HandleFunc("GET /v1/me/usage/people", h.people)
}

// personUsage is what one person's runs counted in the period.
type personUsage struct {
	Subject string `json:"subject"`
	Tasks   int    `json:"tasks"`
	Actions int    `json:"actions"`
}

// groupUsage is what a group's people's runs counted: a person in two groups
// counts in both.
type groupUsage struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	People  int    `json:"people"`
	Tasks   int    `json:"tasks"`
	Actions int    `json:"actions"`
}

type peopleResponse struct {
	Period usagePeriod   `json:"period"`
	People []personUsage `json:"people"`
	// NoPerson is what no person's run made: schedules, doors, and the
	// account's own agents. Billing admins only.
	NoPerson *personUsage `json:"no_person,omitempty"`
	Groups   []groupUsage `json:"groups,omitempty"`
	// PeopleSince is when KIFF began recording who asked, while the period
	// started before it: its earlier tasks are in NoPerson.
	PeopleSince *time.Time `json:"people_since,omitempty"`
}

// people is usage by person and group. It's a cost and
// fair-use figure, counts of what got done, never a view of how people
// work (decision 10). The account's billing admin reads everyone's, and
// anyone else reads only their own.
func (h *MeterUsageHandler) people(w http.ResponseWriter, r *http.Request) {
	id, t, ok := h.account(w, r)
	if !ok {
		return
	}
	if id.AgentID != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "usage by person is read by people, not agents"})
		return
	}
	from, to, _, err := h.period(r, t)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	counts, err := h.meter.ByPerson(r.Context(), t.ID, from, to)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "usage: by person failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
		return
	}
	all := mayAddTasks(id)
	resp := peopleResponse{Period: usagePeriod{Start: from, End: to}, People: []personUsage{}}
	bySubject := map[string]personUsage{}
	for _, c := range counts {
		u := personUsage{Subject: c.Subject, Tasks: c.Tasks, Actions: c.Actions}
		switch {
		case c.Subject == "":
			if all {
				resp.NoPerson = &u
			}
		case all || c.Subject == id.Subject:
			resp.People = append(resp.People, u)
			bySubject[c.Subject] = u
		}
	}
	if !all && len(resp.People) == 0 && id.Subject != "" {
		resp.People = append(resp.People, personUsage{Subject: id.Subject})
	}
	if all && h.members != nil {
		// Everyone on the account, those whose tafs got nothing done this
		// period too, after those that did. A removed person isn't added at
		// 0, but one whose tafs got something done before leaving keeps the
		// row the counts gave them: it's what the account is billed for.
		members, err := h.members.List(r.Context(), t.ID)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "usage: people failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
			return
		}
		for _, m := range members {
			if _, ok := bySubject[m.Subject]; ok || m.Subject == "" || m.Status == membership.StatusRemoved {
				continue
			}
			bySubject[m.Subject] = personUsage{Subject: m.Subject}
			resp.People = append(resp.People, personUsage{Subject: m.Subject})
		}
	}
	if all && from.Before(PeopleCountedSince) {
		since := PeopleCountedSince
		resp.PeopleSince = &since
	}
	if all && h.groups != nil {
		groups, err := h.groups.Groups(r.Context(), t.ID)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "usage: groups failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
			return
		}
		for _, g := range groups {
			members, err := h.groups.Members(r.Context(), t.ID, g.ID)
			if err != nil {
				h.logger.ErrorContext(r.Context(), "usage: group members failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
				return
			}
			u := groupUsage{ID: g.ID, Name: g.Name, People: len(members)}
			for _, m := range members {
				u.Tasks += bySubject[m.Subject].Tasks
				u.Actions += bySubject[m.Subject].Actions
			}
			resp.Groups = append(resp.Groups, u)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type usagePeriod struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// agentCount is one taf's part of the tasks meter: what it counted, and its
// free cases by the same keys as tasks.free. The entries
// add up to tasks.counted and tasks.free.
type agentCount struct {
	Agent string `json:"agent"`
	// Name is what people read the agent by ("Refunds 4", "Coach"), from
	// its record; "" when it has none, and the app says the id.
	Name    string         `json:"name,omitempty"`
	Counted int            `json:"counted"`
	Free    map[string]int `json:"free"`
}

// tasksUsage is the period's tasks: what the plan includes, the pack tasks
// for the period (drawn in it, and left on valid packs), used and left.
// Left is null where there's no limit (company, internal).
type tasksUsage struct {
	Counted  int            `json:"counted"`
	Included int            `json:"included"`
	Packs    int            `json:"packs"`
	Used     int            `json:"used"`
	Left     *int           `json:"left"`
	ByAgent  []agentCount   `json:"by_agent"`
	Free     map[string]int `json:"free"`
	// Ceiling and CeilingReached are the tasks the period has and whether
	// they're used up, as earlier apps read them.
	Ceiling        int  `json:"ceiling"`
	CeilingReached bool `json:"ceiling_reached"`
}

// billingUsage is who bills the account's plan, and where it's managed:
// Apple's subscriptions page for one Apple bills. None for one billed on
// kiff.dev, whose Manage billing opens the Stripe portal when it's
// pressed (a session minted on every read would call Stripe on every page
// load), and none on a contract.
type billingUsage struct {
	Source    string  `json:"source"`
	ManageURL *string `json:"manage_url"`
	// AppleSoloUntil: the account moved from App Store Solo to Team, which
	// Stripe bills now, while Solo still renews with Apple until then
	// unless the person cancels it there. The app says so,
	// with Apple's page for it. Absent otherwise.
	AppleSoloUntil *time.Time `json:"apple_solo_until,omitempty"`
	// AppleManageURL is that Apple page, with AppleSoloUntil.
	AppleManageURL *string `json:"apple_manage_url,omitempty"`
}

// packView is one of the account's packs with tasks left.
type packView struct {
	ID        string    `json:"id"`
	Pack      string    `json:"pack"`
	Name      string    `json:"name"`
	Tasks     int       `json:"tasks"`
	TasksLeft int       `json:"tasks_left"`
	Kind      string    `json:"kind"`
	BoughtAt  time.Time `json:"bought_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// storeView is what the app needs to sell: the account's appAccountToken,
// Solo's product, and the packs (name, tasks, how long they last, and the
// store ids). No prices: the stores show them.
type storeView struct {
	AppAccountToken string `json:"app_account_token,omitempty"`
	SoloProduct     string `json:"solo_product"`
	// GoogleSoloProduct is Solo's Play subscription; absent while Google
	// Play billing isn't set up.
	GoogleSoloProduct string          `json:"google_solo_product,omitempty"`
	Packs             []storeViewPack `json:"packs"`
}

type storeViewPack struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Line   string `json:"line"`
	Tasks  int    `json:"tasks"`
	Months int    `json:"months"`
	// Credits are a pack of credits'; it never expires. Tasks and Months
	// stay 0, for apps from before that read them.
	Credits int64 `json:"credits"`
	// Once marks the pack sold once per account, listed until the
	// account has bought it.
	Once bool `json:"once,omitempty"`
	// AppleProduct is absent for a pack the App Store doesn't sell yet:
	// the iPhone app drops a pack without it, where an empty id would
	// show a row nothing can buy.
	AppleProduct string `json:"apple_product,omitempty"`
	// GoogleProduct is the pack's Play product; absent while Google Play
	// billing isn't set up.
	GoogleProduct string `json:"google_product,omitempty"`
	StripePrice   string `json:"stripe_price"`
}

type actionsUsage struct {
	Counted  int            `json:"counted"`
	Included int            `json:"included"`
	Free     map[string]int `json:"free"`
}

type trialUsage struct {
	// EndsAt is null for a trial not started yet: it starts at checkout,
	// with a card, and KIFF refuses the account's calls until then.
	EndsAt      *time.Time `json:"ends_at"`
	DaysLeft    int        `json:"days_left"`
	TasksLeft   int        `json:"tasks_left"`
	ActionsLeft int        `json:"actions_left"`
}

// aiUsage is the owner's AI fair-use answers for the period: how many
// times they said go on past the bound, each a signed tap. The bound and
// the use are kiff/agents' and tafs cloud's, which asks again at each
// further full bound (kiff/tafs#193); KIFF keeps only the count.
type aiUsage struct {
	GoOns int `json:"go_ons"`
}

// aiGoOns is t's go-ons in the period starting at from.
func aiGoOns(t tenant.Tenant, from time.Time) int {
	s, _ := t.Metadata[tenant.MetadataKeyAIGoOn].(string)
	at, err := time.Parse(time.RFC3339, s)
	if err != nil || !at.Equal(from.UTC()) {
		return 0
	}
	switch n := t.Metadata[tenant.MetadataKeyAIGoOns].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

type usageResponse struct {
	Plan string `json:"plan"`
	// Seats are the people the plan pays for, its admins and editors: 1
	// on the trial and Solo, Team's seats, and 0 where there's no limit
	// (company, internal). Team's AI bound grows with them (kiff/tafs#193).
	Seats   int          `json:"seats"`
	Period  usagePeriod  `json:"period"`
	Tasks   tasksUsage   `json:"tasks"`
	Actions actionsUsage `json:"actions"`
	Trial   *trialUsage  `json:"trial"`
	AI      aiUsage      `json:"ai"`
	Billing billingUsage `json:"billing"`
	// PackList is the account's packs with tasks left, oldest first: the
	// order they're used in.
	PackList []packView `json:"pack_list"`
	// CanAddTasks reports that the caller may buy tasks (Solo, or packs
	// where can_add_packs); a member who can't asks an admin (Requests).
	CanAddTasks bool `json:"can_add_tasks"`
	// CanAddPacks reports that packs are offered: can_add_tasks, on an
	// account whose tafs run. On a trial not started or ended only Solo is
	// offered, and packs bought before count again once it starts.
	CanAddPacks bool       `json:"can_add_packs"`
	Store       *storeView `json:"store,omitempty"`
	// Requests are the members' requests for packs: every open one for
	// someone who may add tasks, the caller's own this period otherwise.
	Requests []packs.Request `json:"requests"`
}

// account reads the caller's account, writing the refusal when it can't.
func (h *MeterUsageHandler) account(w http.ResponseWriter, r *http.Request) (auth.Identity, tenant.Tenant, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to an account"})
		return id, tenant.Tenant{}, false
	}
	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "usage: read account failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
		return id, tenant.Tenant{}, false
	}
	return id, t, true
}

// period is the period asked for (?period=<start>), else the current one.
func (h *MeterUsageHandler) period(r *http.Request, t tenant.Tenant) (time.Time, time.Time, bool, error) {
	if v := strings.TrimSpace(r.URL.Query().Get("period")); v != "" {
		start, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, false, errors.New("period must be the period's start, RFC 3339")
		}
		from, to := tenant.PeriodFrom(t, start)
		now := h.now().UTC()
		return from, to, !now.Before(from) && now.Before(to), nil
	}
	from, to := tenant.Period(t, h.now())
	return from, to, true, nil
}

func (h *MeterUsageHandler) usage(w http.ResponseWriter, r *http.Request) {
	id, t, ok := h.account(w, r)
	if !ok {
		return
	}
	from, to, current, err := h.period(r, t)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	tot, err := h.meter.Totals(r.Context(), t.ID, from, to)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "usage: totals failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
		return
	}
	tafs, err := h.meter.Tafs(r.Context(), t.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "usage: tafs failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
		return
	}
	resp := h.view(t, from, to, current, tot, tafs)
	names := h.agentNames(r.Context(), t.ID)
	for i := range resp.Tasks.ByAgent {
		resp.Tasks.ByAgent[i].Name = names[resp.Tasks.ByAgent[i].Agent]
	}
	if err := h.addTasks(r.Context(), id, t, from, to, current, &resp); err != nil {
		h.logger.ErrorContext(r.Context(), "usage: packs failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
		return
	}
	inertTasks(&resp)
	writeJSON(w, http.StatusOK, resp)
}

// inertTasks empties what the read said of tasks and the trial: credits are
// the balance now (GET /v1/me/usage/credits), and there's no trial. The
// fields stay, in the shape apps from before decode, until both stores have
// the apps that no longer read them: tasks count nothing and have no limit,
// a trial reads as not started, and no pack of tasks is held or offered.
func inertTasks(resp *usageResponse) {
	resp.Tasks = tasksUsage{ByAgent: []agentCount{}, Free: map[string]int{}}
	if resp.Trial != nil {
		resp.Trial = &trialUsage{}
	}
	resp.PackList = []packView{}
	resp.CanAddTasks, resp.CanAddPacks = false, false
}

// mayAddTasks reports that id may buy packs: the account's billing admin.
func mayAddTasks(id auth.Identity) bool {
	if id.AgentID != "" {
		return false
	}
	if id.MemberRole != "" {
		return membership.Can(id.MemberRole, membership.CapBilling)
	}
	return id.ManagesAccount()
}

// addTasks completes the read with the pack tasks, who bills the plan, the
// store's ids and the members' requests.
func (h *MeterUsageHandler) addTasks(ctx context.Context, id auth.Identity, t tenant.Tenant, from, to time.Time, current bool, resp *usageResponse) error {
	now := h.now().UTC()
	at := now
	if !current && to.Before(now) {
		at = to
	}
	resp.PackList = []packView{}
	resp.Requests = []packs.Request{}
	if h.packs != nil {
		drawn, left, err := h.packs.Allowance(ctx, t.ID, from, at)
		if err != nil {
			return err
		}
		resp.Tasks.Packs = drawn + left
		valid, err := h.packs.Valid(ctx, t.ID, at)
		if err != nil {
			return err
		}
		for _, e := range valid {
			name := e.Pack
			if p, ok := h.catalog.Pack(e.Pack); ok {
				name = p.Name
			} else if e.Kind == packs.KindBonus {
				name = "Bonus"
			}
			resp.PackList = append(resp.PackList, packView{ID: e.ID, Pack: e.Pack, Name: name, Tasks: e.Tasks, TasksLeft: e.TasksLeft,
				Kind: string(e.Kind), BoughtAt: e.BoughtAt, ExpiresAt: e.ExpiresAt})
		}
	}
	// A trial not started or ended runs nothing (KIFF refuses its calls), so
	// the packs it holds wait for a plan: listed, not counted. An ended trial
	// has no tasks of its own either.
	runs := tenant.Runs(t, now)
	ended := !runs && tenant.TrialStatusAt(t, now) == tenant.TrialEnded
	if current && !runs {
		resp.Tasks.Packs = 0
	}
	if current && ended {
		resp.Tasks.Included = 0
	}
	resp.Tasks.Used = resp.Tasks.Counted
	if current && ended {
		none := 0
		resp.Tasks.Left, resp.Tasks.Ceiling, resp.Tasks.CeilingReached = &none, 0, false
	} else if limit := tenant.PeriodTasks(t, resp.Tasks.Packs); limit > 0 {
		left := max(0, limit-resp.Tasks.Used)
		resp.Tasks.Left = &left
		resp.Tasks.Ceiling = limit
		resp.Tasks.CeilingReached = current && left == 0
	}
	if resp.Trial != nil && resp.Tasks.Left != nil {
		resp.Trial.TasksLeft = *resp.Tasks.Left
	}
	resp.CanAddTasks = mayAddTasks(id) && resp.Tasks.Left != nil
	// Packs only add to a plan that runs: on a trial not started or ended,
	// Solo is what's offered.
	resp.CanAddPacks = resp.CanAddTasks && runs

	source := tenant.BillingSourceOf(t)
	resp.Billing.Source = string(source)
	if source == tenant.BillingApple {
		u := AppleManageURL
		resp.Billing.ManageURL = &u
	} else if source == tenant.BillingGoogle {
		u := GoogleManageURL
		resp.Billing.ManageURL = &u
	} else if until, ok := appstore.SoloRenewsUntil(t, h.now()); ok && source == tenant.BillingStripe && mayAddTasks(id) {
		// Said to the billing admin only, and only while Solo still renews:
		// once its renewal is off with Apple, there's nothing to cancel.
		u := AppleManageURL
		resp.Billing.AppleSoloUntil, resp.Billing.AppleManageURL = &until, &u
	}

	if len(h.catalog.Packs) > 0 {
		resp.Store = &storeView{SoloProduct: h.catalog.Solo.AppleProduct, Packs: []storeViewPack{}}
		if h.google {
			resp.Store.GoogleSoloProduct = h.catalog.Solo.GoogleProduct
		}
		// Only packs of credits are sold: packs of tasks no longer count. A
		// pack of credits never expires, so it has no months.
		// A pack sold once per account is listed until the account has
		// bought it; with no read of what it bought, it isn't listed.
		got := map[string]bool{}
		if len(h.catalog.OncePacks()) > 0 && h.bought != nil {
			var err error
			if got, err = h.bought.BoughtPacks(ctx, t.ID); err != nil {
				return err
			}
		}
		for _, p := range h.catalog.Offered(func(id string) bool { return h.bought == nil || got[id] }) {
			sp := storeViewPack{ID: p.ID, Name: p.Name, Line: p.Line(), Credits: p.Credits, Once: p.Once,
				AppleProduct: p.AppleProduct, StripePrice: p.StripePrice}
			if h.google {
				sp.GoogleProduct = p.GoogleProduct
			}
			resp.Store.Packs = append(resp.Store.Packs, sp)
		}
		if h.apple != nil && mayAddTasks(id) {
			token, err := h.apple.Token(ctx, t.ID)
			if err != nil {
				return err
			}
			resp.Store.AppAccountToken = token
		}
	}

	if h.requests != nil && id.AgentID == "" {
		var (
			list []packs.Request
			err  error
		)
		if mayAddTasks(id) {
			list, err = h.requests.List(ctx, t.ID, "", true, now.AddDate(-1, 0, 0), now)
		} else if id.Subject != "" {
			list, err = h.requests.List(ctx, t.ID, id.Subject, false, from, now)
		}
		if err != nil {
			return err
		}
		if list != nil {
			resp.Requests = list
		}
	}
	return nil
}

// view is the read: tafs are the account's tafs, each listed in by_agent
// even with nothing this period.
func (h *MeterUsageHandler) view(t tenant.Tenant, from, to time.Time, current bool, tot map[meter.Unit]meter.Totals, tafs []string) usageResponse {
	l := tenant.PlanFor(t)
	tasks, actions := tot[meter.UnitTask], tot[meter.UnitAction]
	resp := usageResponse{
		Plan:   string(l.Canonical),
		Seats:  l.Seats,
		Period: usagePeriod{Start: from.UTC(), End: to.UTC()},
		Tasks: tasksUsage{Counted: tasks.Counted, Included: l.IncludedTasks, Used: tasks.Counted,
			ByAgent: []agentCount{}, Free: tasks.Free},
		Actions:  actionsUsage{Counted: actions.Counted, Included: l.IncludedActions, Free: actions.Free},
		PackList: []packView{},
		Requests: []packs.Request{},
	}
	if limit := tenant.PeriodTasks(t, 0); limit > 0 {
		left := max(0, limit-tasks.Counted)
		resp.Tasks.Left, resp.Tasks.Ceiling, resp.Tasks.CeilingReached = &left, limit, current && left == 0
	}
	resp.AI.GoOns = aiGoOns(t, from)
	// Every taf, and any other agent with a task this period (one archived
	// since): so the entries add up to the meter's totals.
	agents := map[string]bool{}
	for _, a := range tafs {
		agents[a] = true
	}
	for a := range tasks.ByAgent {
		agents[a] = true
	}
	for a := range tasks.FreeByAgent {
		agents[a] = true
	}
	for agent := range agents {
		free := map[string]int{}
		for _, k := range meter.FreeKinds {
			free[k] = tasks.FreeByAgent[agent][k]
		}
		resp.Tasks.ByAgent = append(resp.Tasks.ByAgent, agentCount{Agent: agent, Counted: tasks.ByAgent[agent], Free: free})
	}
	sort.Slice(resp.Tasks.ByAgent, func(i, j int) bool {
		a, b := resp.Tasks.ByAgent[i], resp.Tasks.ByAgent[j]
		return a.Counted > b.Counted || a.Counted == b.Counted && a.Agent < b.Agent
	})
	if l.Canonical == tenant.PlanTrial {
		resp.Trial = &trialUsage{TasksLeft: max(0, l.IncludedTasks-tasks.Counted), ActionsLeft: max(0, l.IncludedActions-actions.Counted)}
		if end, ok := tenant.TrialEndsAt(t); ok {
			resp.Trial.EndsAt = &end
			resp.Trial.DaysLeft = max(0, int(math.Ceil(end.Sub(h.now().UTC()).Hours()/24)))
		}
	}
	return resp
}

// tasksPerPage is how many counted tasks a page lists.
const tasksPerPage = 50

func (h *MeterUsageHandler) tasks(w http.ResponseWriter, r *http.Request) {
	id, t, ok := h.account(w, r)
	if !ok {
		return
	}
	// Each counted task opens its Record line: a person reads their own runs'
	// and those of the agents they manage; an admin all.
	scope, err := h.ownUse.Scope(r.Context(), id, t.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "usage: reader's scope unreadable", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
		return
	}
	from, to, _, err := h.period(r, t)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var page *meter.TaskPage
	if c := strings.TrimSpace(r.URL.Query().Get("cursor")); c != "" {
		if page, err = decodeTaskCursor(c); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cursor is the next of an earlier page"})
			return
		}
	}
	// One taf's tasks: an agent that isn't the account's
	// is not found, as on the other agent routes.
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	if agent != "" {
		ok, err := h.meter.IsAgent(r.Context(), t.ID, agent)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "usage: agent lookup failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
			return
		}
	}
	tasks, next, err := h.meter.Tasks(r.Context(), t.ID, agent, scope.Tasks(), from, to, page, tasksPerPage)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "usage: tasks failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "usage unavailable"})
		return
	}
	// The meter keeps each call's tool title from when it was recorded. A
	// call recorded before it did gets the tool's title now, if the tool is
	// still connected; best effort, as on the Record.
	var titles map[string]string
	for i := range tasks {
		if tasks[i].ToolTitle != "" {
			continue
		}
		if titles == nil && h.titles != nil {
			if titles, _ = h.titles.ToolTitles(r.Context(), t.ID); titles == nil {
				titles = map[string]string{}
			}
		}
		tasks[i].ToolTitle = titles[tasks[i].Tool]
	}
	names := h.agentNames(r.Context(), t.ID)
	for i := range tasks {
		tasks[i].AgentName = names[tasks[i].Agent]
	}
	out := map[string]any{"items": tasks}
	if next != nil {
		out["next"] = encodeTaskCursor(*next)
	}
	writeJSON(w, http.StatusOK, out)
}

func encodeTaskCursor(p meter.TaskPage) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p.At.UTC().Format(time.RFC3339Nano) + "|" + p.Key))
}

func decodeTaskCursor(s string) (*meter.TaskPage, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	at, key, ok := strings.Cut(string(b), "|")
	if !ok || key == "" {
		return nil, errors.New("cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, err
	}
	return &meter.TaskPage{At: t, Key: key}, nil
}

// setCeiling is retired: the tasks a period has are what the plan
// includes and the account's packs, and someone adds a pack when they run
// out. Gone, with a message that says so.
func (h *MeterUsageHandler) setCeiling(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusGone, map[string]string{"error": "ceiling_retired",
		"message": "There is no ceiling to set. When the tasks run out, add a pack."})
}

// setAIGoOn adds one go-on past the AI fair-use bound for the current
// period (kiff/tafs#193): letting the tafs spend past it is the owner's
// act, so from a phone it's signed (PhoneSignedRules), and a replayed
// signature is refused, so one tap adds exactly one. It takes
// billing.manage. Only {"go_on": true}; the next period starts at 0.
func (h *MeterUsageHandler) setAIGoOn(w http.ResponseWriter, r *http.Request) {
	id, t, ok := h.account(w, r)
	if !ok {
		return
	}
	if id.AgentID != "" || (id.MemberRole != "" && !membership.Can(id.MemberRole, membership.CapBilling)) || (id.MemberRole == "" && !id.ManagesAccount()) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "going on is the account's billing admin's"})
		return
	}
	var body struct {
		GoOn bool `json:"go_on"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || !body.GoOn {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"go_on": true}`})
		return
	}
	from, _ := tenant.Period(t, h.now())
	// One step for the period and its count: a period never takes the
	// previous one's count, and two go-ons at once both count.
	n, err := h.tenants.AddAIGoOn(r.Context(), t.ID, from)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "usage: set ai go-on failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record it"})
		return
	}
	writeJSON(w, http.StatusOK, aiUsage{GoOns: n})
}
