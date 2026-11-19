package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/runcontext"
	"github.com/kiff/cloud/api/internal/tenant"
)

// RunLedger keeps the runs of the account's tafs and of Setup (Coach) that
// ended having used AI, one row each, and their credits (meter.Postgres).
type RunLedger interface {
	KeepRun(ctx context.Context, r meter.RunReport, from, to time.Time) (meter.RunEntry, bool, error)
	RunTotals(ctx context.Context, tenantID string, from, to time.Time) (meter.RunTotals, error)
	Runs(ctx context.Context, tenantID, runtimeAgent string, scope *meter.TaskScope, from, to time.Time, page *meter.RunPage, limit int) ([]meter.RunEntry, *meter.RunPage, error)
	Tafs(ctx context.Context, tenantID string) ([]string, error)
	SetupSpent(ctx context.Context, tenantID string, from, to time.Time) (int64, error)
	NoPlanSetupSpent(ctx context.Context, tenantID string) (int64, error)
}

// CreditsHandler is the credits a run uses, and the account's balance of
// them. A taf's runtime reports each of its runs that ended having used AI,
// once, with the run's agent's key (PUT /v1/me/runs/{run}/usage), and the
// ledger keeps it with its credits; before each run, it asks whether one
// may start (GET /v1/me/credits/allowance). People read the period's
// credits and the balance (GET /v1/me/usage/credits) and its runs
// (GET /v1/me/usage/credits/runs), and someone who manages billing raises
// Company's spend limit (PUT /v1/me/usage/limit, signed on the phone).
type CreditsHandler struct {
	tenants  tenant.Store
	ledger   RunLedger
	packs    CreditPacksRead
	bought   packs.BoughtRead
	catalog  *packs.Catalog
	requests packs.Requests
	alerts   alert.Notifier
	ownUse   *OwnUse
	names    UsageAgentNames
	logger   *slog.Logger
	now      func() time.Time
}

// NewCreditsHandler reads and keeps runs through ledger.
func NewCreditsHandler(tenants tenant.Store, ledger RunLedger, logger *slog.Logger) *CreditsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &CreditsHandler{tenants: tenants, ledger: ledger, logger: logger, now: time.Now}
}

// WithOwnUse keeps a person's reading of runs to their own and those of the
// agents they manage, as the tasks list does.
func (h *CreditsHandler) WithOwnUse(o *OwnUse) *CreditsHandler { h.ownUse = o; return h }

// WithAgentNames names the agents of the period's credits.
func (h *CreditsHandler) WithAgentNames(a UsageAgentNames) *CreditsHandler { h.names = a; return h }

// WithClock sets the clock, for tests.
func (h *CreditsHandler) WithClock(now func() time.Time) *CreditsHandler { h.now = now; return h }

// Register adds the credits routes to mux.
func (h *CreditsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("PUT /v1/me/runs/{run}/usage", h.report)
	mux.HandleFunc("GET /v1/me/credits/allowance", h.allowance)
	mux.HandleFunc("GET /v1/me/usage/credits", h.credits)
	mux.HandleFunc("GET /v1/me/usage/credits/runs", h.runs)
	mux.HandleFunc("PUT /v1/me/usage/limit", h.raise)
}

// runUsage is a run's usage report, as its runtime sends it.
type runUsage struct {
	Agent     string `json:"agent"`
	Trigger   string `json:"trigger"`
	Source    string `json:"source"`
	Requester *struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"requester"`
	Started      time.Time  `json:"started"`
	Ended        time.Time  `json:"ended"`
	CostMicroUSD int64      `json:"cost_micro_usd"`
	Turns        int        `json:"turns"`
	Status       string     `json:"status"`
	Stop         string     `json:"stop"`
	Failed       bool       `json:"failed"`
	Refused      int        `json:"refused"`
	Declined     int        `json:"declined"`
	KeptUntil    *time.Time `json:"kept_until"`
}

// maxRunUsage bounds a usage report's body.
const maxRunUsage = 16 << 10

// maxRunField bounds each of a report's words.
const maxRunField = 256

// runStatuses are how a reported run may have ended.
var runStatuses = []string{"COMPLETED", "ERROR", "CANCELLED"}

// report keeps one ended run's usage, from its runtime: only for the key's
// own agent, a taf's or Setup's, never one of the account's own agents,
// whose calls are KIFF actions. A run's first report is kept, so the
// runtime sends one again freely; both answer 204.
func (h *CreditsHandler) report(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" || !id.APIKey || id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "an agent's key reports its own runs"})
		return
	}
	run := r.PathValue("run")
	if run == "" || (runcontext.RunContext{RunID: run}).Validate() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the run's id: 1 to 128 letters, digits, '.', '_', ':' or '-'"})
		return
	}
	var u runUsage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunUsage)).Decode(&u); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is a run's usage report, as JSON"})
		return
	}
	if msg := u.invalid(); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "credits: read account failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return
	}
	// Setup's key is Setup's whatever its agent's kind (a phone-confirmed
	// Setup is made a taf); any other must be a taf's.
	setup := id.HasRole(auth.RoleSetupReader)
	if !setup {
		tafs, err := h.ledger.Tafs(r.Context(), t.ID)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "credits: tafs failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
			return
		}
		if !slices.Contains(tafs, id.AgentID) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a taf's or Coach's runs use credits"})
			return
		}
	}
	rep := meter.RunReport{Tenant: t.ID, Run: run, Agent: id.AgentID, RuntimeAgent: u.Agent, Setup: setup,
		Trigger: u.Trigger, Source: u.Source, Started: u.Started.UTC(), Ended: u.Ended.UTC(),
		CostMicroUSD: u.CostMicroUSD, Turns: u.Turns, Status: u.Status, Stop: u.Stop, Failed: u.Failed,
		Refused: u.Refused, Declined: u.Declined, KeptUntil: u.KeptUntil}
	if kind, _ := meter.KindOf(t); kind == meter.BalanceNone {
		rep.NoPlan = true
	}
	if u.Requester != nil {
		rep.RequesterKind = u.Requester.Kind
		// Whose run it was, for their own reading: a member's or a manager's
		// subject; a door's sender isn't a person of the account.
		if u.Requester.Kind == "member" || u.Requester.Kind == "manager" {
			rep.AskedBy = u.Requester.ID
		}
	}
	// Setup's fair use is the period the run ended in, which its row is
	// filed under, never the one its report arrived in: a run that ended
	// just before the period rolled over, reported after, is the old one's.
	from, to := tenant.PeriodAt(t, rep.Ended, h.now())
	if _, _, err := h.ledger.KeepRun(r.Context(), rep, from, to); err != nil {
		h.logger.ErrorContext(r.Context(), "credits: keep run failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// invalid says what's wrong with the report, or "".
func (u runUsage) invalid() string {
	long := func(ss ...string) bool {
		return slices.ContainsFunc(ss, func(s string) bool { return len(s) > maxRunField })
	}
	switch {
	case u.Agent == "" || long(u.Agent, u.Trigger, u.Source, u.Stop):
		return "agent is the run's agent in its runtime, and each word at most 256 bytes"
	case u.Requester != nil && long(u.Requester.Kind, u.Requester.ID):
		return "requester's kind and id are at most 256 bytes each"
	case u.Started.IsZero() || u.Ended.IsZero() || u.Ended.Before(u.Started):
		return "started and ended are when the run began and ended, the end not before the start"
	case u.CostMicroUSD < 0 || u.Turns < 0 || u.Refused < 0 || u.Declined < 0:
		return "cost_micro_usd, turns, refused and declined are never negative"
	case !slices.Contains(runStatuses, u.Status):
		return "status is how the run ended: COMPLETED, ERROR or CANCELLED"
	}
	return ""
}

// creditsPeriod is the period asked for: ?period=previous for the one
// before the current one, ?period=<start> (RFC 3339) for the one starting
// then, else the current one; and whether it's the current one.
func (h *CreditsHandler) creditsPeriod(r *http.Request, t tenant.Tenant) (time.Time, time.Time, bool, error) {
	from, to := tenant.Period(t, h.now())
	switch v := strings.TrimSpace(r.URL.Query().Get("period")); v {
	case "":
		return from, to, true, nil
	case "previous":
		pf, pt := tenant.PeriodAt(t, from.Add(-time.Nanosecond), h.now())
		return pf, pt, false, nil
	default:
		start, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, false, errors.New("period is previous, or the period's start, RFC 3339")
		}
		pf, pt := tenant.PeriodFrom(t, start)
		now := h.now().UTC()
		return pf, pt, !now.Before(pf) && now.Before(pt), nil
	}
}

type creditsAgent struct {
	Agent        string `json:"agent"`
	RuntimeAgent string `json:"runtime_agent"`
	Name         string `json:"name,omitempty"`
	Credits      int64  `json:"credits"`
	Runs         int    `json:"runs"`
}

type creditsSetup struct {
	Credits int64 `json:"credits"`
	Runs    int   `json:"runs"`
}

type creditsResponse struct {
	Period  usagePeriod    `json:"period"`
	Current bool           `json:"current"`
	Credits int64          `json:"credits"`
	Runs    int            `json:"runs"`
	ByAgent []creditsAgent `json:"by_agent"`
	// Setup are Setup's (Coach's) counted runs, past its fair use.
	Setup creditsSetup `json:"setup"`
	// Free counts the runs that used no credits, by why: setup (Coach
	// within its fair use), trial, failed.
	Free map[string]int `json:"free"`
	// Over is Solo's free overshoot in the period: credits runs used past
	// its balance, which nothing pays for.
	Over int64 `json:"over"`
	// Balance is the account's balance now, whichever period is read.
	Balance *balanceView `json:"balance"`
}

// credits is the period's credits, account-wide as the tasks' totals are.
func (h *CreditsHandler) credits(w http.ResponseWriter, r *http.Request) {
	t, ok := h.account(w, r)
	if !ok {
		return
	}
	from, to, current, err := h.creditsPeriod(r, t)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	tot, err := h.ledger.RunTotals(r.Context(), t.ID, from, to)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "credits: totals failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return
	}
	names := h.agentNames(r.Context(), t.ID)
	resp := creditsResponse{Period: usagePeriod{Start: from.UTC(), End: to.UTC()}, Current: current, Credits: tot.Credits, Runs: tot.Runs,
		ByAgent: []creditsAgent{}, Setup: creditsSetup{Credits: tot.Setup.Credits, Runs: tot.Setup.Runs}, Free: map[string]int{}, Over: tot.Over}
	for _, a := range tot.ByAgent {
		resp.ByAgent = append(resp.ByAgent, creditsAgent{Agent: a.Agent, RuntimeAgent: a.RuntimeAgent, Name: names[a.Agent], Credits: a.Credits, Runs: a.Runs})
	}
	for _, k := range meter.RunFreeKinds {
		resp.Free[k] = tot.Free[k]
	}
	// The balance is the current period's: the totals just read, when
	// that's the period read.
	var cur *meter.RunTotals
	if current {
		cur = &tot
	}
	b, bFrom, bTo, err := h.balanceOf(r.Context(), t, cur)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "credits: balance failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return
	}
	id, _ := auth.IdentityFrom(r.Context())
	once, err := h.onceOffered(r.Context(), t)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "credits: bought packs failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return
	}
	v := h.view(id, t, b, bFrom, bTo, once)
	resp.Balance = &v
	writeJSON(w, http.StatusOK, resp)
}

type creditsRun struct {
	Run          string     `json:"run"`
	Agent        string     `json:"agent"`
	RuntimeAgent string     `json:"runtime_agent"`
	Setup        bool       `json:"setup"`
	Trigger      string     `json:"trigger"`
	Source       string     `json:"source,omitempty"`
	Requester    string     `json:"requester_kind,omitempty"`
	Mine         bool       `json:"mine"`
	Started      time.Time  `json:"started"`
	Ended        time.Time  `json:"ended"`
	Counted      bool       `json:"counted"`
	Free         string     `json:"free,omitempty"`
	Credits      int64      `json:"credits"`
	Refused      int        `json:"refused"`
	Declined     int        `json:"declined"`
	KeptUntil    *time.Time `json:"kept_until,omitempty"`
}

// runsPerPage is a page of the period's runs.
const runsPerPage = 50

// runs lists the period's runs, newest first (?runtime_agent= for one
// agent's, ?cursor= for the next page): a person reads their own runs and
// those of the agents they manage, an admin all, as on the tasks list.
// Whose run it was shows only as mine, never another person's subject.
func (h *CreditsHandler) runs(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to an account"})
		return
	}
	t, ok := h.account(w, r)
	if !ok {
		return
	}
	scope, err := h.ownUse.Scope(r.Context(), id, t.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "credits: reader's scope unreadable", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return
	}
	from, to, _, err := h.creditsPeriod(r, t)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var page *meter.RunPage
	if c := strings.TrimSpace(r.URL.Query().Get("cursor")); c != "" {
		if page, err = decodeRunCursor(c); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cursor is the next of an earlier page"})
			return
		}
	}
	list, next, err := h.ledger.Runs(r.Context(), t.ID, strings.TrimSpace(r.URL.Query().Get("runtime_agent")), scope.Tasks(), from, to, page, runsPerPage)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "credits: runs failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return
	}
	items := make([]creditsRun, 0, len(list))
	for _, e := range list {
		items = append(items, creditsRun{Run: e.Run, Agent: e.Agent, RuntimeAgent: e.RuntimeAgent, Setup: e.Setup, Trigger: e.Trigger,
			Source: e.Source, Requester: e.RequesterKind, Mine: e.AskedBy != "" && e.AskedBy == id.Subject, Started: e.Started,
			Ended: e.Ended, Counted: e.Counted, Free: e.Free, Credits: e.Credits, Refused: e.Refused, Declined: e.Declined, KeptUntil: e.KeptUntil})
	}
	out := map[string]any{"period": usagePeriod{Start: from.UTC(), End: to.UTC()}, "items": items}
	if next != nil {
		out["next"] = encodeRunCursor(*next)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *CreditsHandler) account(w http.ResponseWriter, r *http.Request) (tenant.Tenant, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to an account"})
		return tenant.Tenant{}, false
	}
	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "credits: read account failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return tenant.Tenant{}, false
	}
	return t, true
}

func (h *CreditsHandler) agentNames(ctx context.Context, tenantID string) map[string]string {
	out := map[string]string{}
	if h.names == nil {
		return out
	}
	as, err := h.names.List(ctx, tenantID)
	if err != nil {
		h.logger.WarnContext(ctx, "credits: agent names unreadable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return out
	}
	for _, a := range as {
		if a.Name != "" {
			out[a.ID] = a.Name
		}
	}
	return out
}

func encodeRunCursor(p meter.RunPage) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p.At.UTC().Format(time.RFC3339Nano) + "|" + p.Run))
}

func decodeRunCursor(s string) (*meter.RunPage, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	at, run, ok := strings.Cut(string(b), "|")
	if !ok || run == "" {
		return nil, errors.New("not a cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, err
	}
	return &meter.RunPage{At: t, Run: run}, nil
}
