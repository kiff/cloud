package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/receipt"
)

// HaltsHandler is the owner's halt (gate B): one
// agent, or the whole account, stopped until it is resumed.
//
//	POST /v1/agents/{id}/halt    halt the agent
//	POST /v1/halt                halt the account: every agent, and any made meanwhile
//	POST /v1/agents/{id}/resume  resume it; from a phone, signed (PhoneSignedRules)
//	POST /v1/resume              resume the account (an agent halted on its own stays so)
//	GET  /v1/agents/{id}/halt    where the agent stands
//	GET  /v1/halt                where the account stands
//
// Halting takes authority away, so it is one tap: the phone's session, no
// signature. Resuming gives it back, so a phone signs it with Face ID. Both
// are the owner's or an admin's (cards.manage), never an agent's key, and
// each is on the Record with its own receipt.
//
// A halt holds in KIFF once it is stored: decide refuses the agent's calls
// (halted). planque, which sends relay calls on permits issued before it,
// must confirm it sees the halt; until it has, the halt reads halting, and
// a person's read asks planque again. planque's own read (its connector
// key) never does: planque confirms by reading the halt back, so asking it
// from that read would loop.
//
// A trial (agent~trial) is its agent: reading or halting it reads or halts
// the agent, as decide refuses a trial's calls under the agent's halt.
type HaltsHandler struct {
	store    halts.Store
	planque  halts.Confirmer
	receipts receipt.Store
	windows  auth.WindowStore
	logger   *slog.Logger
	now      func() time.Time
}

// WithWindows ends every approval window of the account on a halt
// (kiff/tafs#494): the owner's emergency stop closes the taps too.
func (h *HaltsHandler) WithWindows(windows auth.WindowStore) *HaltsHandler {
	h.windows = windows
	return h
}

// NewHaltsHandler serves halts from store. planque and receipts may be nil:
// no planque to confirm with confirms at once, and no receipt store keeps
// the events without receipts.
func NewHaltsHandler(store halts.Store, planque halts.Confirmer, receipts receipt.Store, logger *slog.Logger) *HaltsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &HaltsHandler{store: store, planque: planque, receipts: receipts, logger: logger, now: time.Now}
}

func (h *HaltsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/agents/{id}/halt", h.halt)
	mux.HandleFunc("POST /v1/halt", h.halt)
	mux.HandleFunc("POST /v1/agents/{id}/resume", h.resume)
	mux.HandleFunc("POST /v1/resume", h.resume)
	mux.HandleFunc("GET /v1/agents/{id}/halt", h.get)
	mux.HandleFunc("GET /v1/halt", h.get)
}

// haltView is where an agent, or the account, stands. State is running,
// halting (KIFF holds it; planque hasn't confirmed yet) or halted (both
// have). HaltedAt is the latest halt's start, kept after a resume.
type haltView struct {
	Agent      string     `json:"agent,omitempty"`
	State      string     `json:"state"`
	Halted     bool       `json:"halted"`
	Own        bool       `json:"own"`
	Account    bool       `json:"account"`
	HaltedAt   *time.Time `json:"halted_at,omitempty"`
	KIFFAck    bool       `json:"kiff_ack"`
	PlanqueAck bool       `json:"planque_ack"`
	// Receipt is this request's event's receipt, when it halted or
	// resumed something.
	Receipt string `json:"receipt,omitempty"`
}

func viewHalt(agent string, e halts.Effective) haltView {
	v := haltView{Agent: agent, Halted: e.On(), Own: e.Own.On, Account: e.Account.On, KIFFAck: e.On(), PlanqueAck: e.On() && e.Confirmed()}
	switch {
	case !e.On():
		v.State = "running"
	case e.Confirmed():
		v.State = "halted"
	default:
		v.State = "halting"
	}
	if at := e.At(); !at.IsZero() {
		v.HaltedAt = &at
	}
	return v
}

// target is the request's identity and the agent it names (halts.Account
// for the account's routes), or false once it has answered.
func (h *HaltsHandler) target(w http.ResponseWriter, r *http.Request, manage bool) (auth.Identity, string, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session or key is required"})
		return id, "", false
	}
	// An agent's key never halts or resumes, nor reads another agent's
	// halt; a manager halts and resumes; anyone who reads the account
	// (planque's connector key too, before each send) reads them.
	if id.AgentID != "" || (manage && !can(id, membership.CapCardsManage)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the account's owner or an admin can halt or resume its agents"})
		return id, "", false
	}
	agent := halts.Account
	if raw := r.PathValue("id"); raw != "" {
		// A trial's holder (agent~trial) reads and halts as its agent; the
		// agent's own id is checked, which can't contain "~".
		if a, ok := mandate.TrialAgent(raw); ok {
			raw = a
		}
		n, err := agents.NormalizeID(raw)
		if err != nil || n != raw {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent id"})
			return id, "", false
		}
		agent = n
	}
	return id, agent, true
}

func (h *HaltsHandler) halt(w http.ResponseWriter, r *http.Request) {
	id, agent, ok := h.target(w, r, true)
	if !ok {
		return
	}
	at := h.now().UTC().Truncate(time.Microsecond) // as Postgres keeps it, so planque's ack matches
	by := issuerOf(id)
	got, began, err := h.store.Halt(r.Context(), id.Tenant, agent, at, by)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "halts: halt failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the halt could not be stored"})
		return
	}
	var trace string
	if began {
		trace = h.log(r.Context(), id.Tenant, agent, halts.KindHalt, got.HaltedAt, by)
	}
	// A halt ends the account's approval windows, whether it began now or
	// holds already: a Close or a routine call racing it never reopens one.
	if h.windows != nil {
		if _, err := h.windows.CloseAll(r.Context(), auth.WindowMatch{Tenant: id.Tenant}, at, auth.WindowClosedByHalt); err != nil {
			h.logger.WarnContext(r.Context(), "halts: closing the account's approval windows failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		}
	}
	h.confirm(r.Context(), id.Tenant, agent, got)
	h.answer(w, r, id.Tenant, agent, trace)
}

func (h *HaltsHandler) resume(w http.ResponseWriter, r *http.Request) {
	id, agent, ok := h.target(w, r, true)
	if !ok {
		return
	}
	at := h.now().UTC().Truncate(time.Microsecond)
	by := issuerOf(id)
	_, ended, err := h.store.Resume(r.Context(), id.Tenant, agent, at, by)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "halts: resume failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the resume could not be stored"})
		return
	}
	var trace string
	if ended {
		trace = h.log(r.Context(), id.Tenant, agent, halts.KindResume, at, by)
	}
	h.answer(w, r, id.Tenant, agent, trace)
}

func (h *HaltsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, agent, ok := h.target(w, r, false)
	if !ok {
		return
	}
	e, err := halts.For(r.Context(), h.store, id.Tenant, agent)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the halt could not be read"})
		return
	}
	// Still halting: ask planque again, so a person's read moves it on.
	// Not on planque's own read, which is how planque confirms.
	for _, hl := range []halts.Halt{e.Own, e.Account} {
		if hl.On && hl.PlanqueAck == nil && !id.IsConnector() {
			h.confirm(r.Context(), id.Tenant, hl.AgentID, hl)
		}
	}
	h.answer(w, r, id.Tenant, agent, "")
}

// answer writes where agent stands now, with trace, the receipt of what
// this request did.
func (h *HaltsHandler) answer(w http.ResponseWriter, r *http.Request, tenant, agent, trace string) {
	e, err := halts.For(r.Context(), h.store, tenant, agent)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the halt could not be read"})
		return
	}
	v := viewHalt(agent, e)
	v.Receipt = trace
	writeJSON(w, http.StatusOK, v)
}

// confirm asks planque to confirm hl and records its ack. A planque that
// can't be reached leaves it halting; its own dispatch fails closed.
func (h *HaltsHandler) confirm(ctx context.Context, tenant, agent string, hl halts.Halt) {
	if !hl.On || hl.PlanqueAck != nil {
		return
	}
	if h.planque != nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := h.planque.Confirm(cctx, tenant, agent, hl.HaltedAt)
		cancel()
		if err != nil {
			h.logger.WarnContext(ctx, "halts: planque hasn't confirmed the halt", slog.String("tenant", tenant), slog.String("agent", agent), slog.String("err", err.Error()))
			return
		}
	}
	if err := h.store.AckPlanque(ctx, tenant, agent, hl.HaltedAt, h.now()); err != nil {
		h.logger.WarnContext(ctx, "halts: recording planque's ack failed", slog.String("tenant", tenant), slog.String("err", err.Error()))
	}
}

// log records a halt or a resume for the Record, with its receipt, and
// returns the receipt's id ("" when none could be kept). Best effort: the
// halt itself is already stored and holds.
func (h *HaltsHandler) log(ctx context.Context, tenant, agent, kind string, at time.Time, by string) string {
	ev := halts.Event{TenantID: tenant, ID: ulid.MustNew(ulid.Timestamp(at), ulid.DefaultEntropy()).String(), AgentID: agent, Kind: kind, At: at, By: by}
	if h.receipts != nil {
		rc, err := receipt.BuildHalt(tenant, receipt.HaltStep{ID: ev.ID, AgentID: agent, Kind: kind, At: at, By: by})
		if err == nil {
			err = h.receipts.Save(ctx, rc)
		}
		if err != nil {
			h.logger.ErrorContext(ctx, "halts: the receipt could not be kept", slog.String("tenant", tenant), slog.String("err", err.Error()))
		} else {
			ev.Receipt = rc.TraceID
		}
	}
	if err := h.store.Log(ctx, ev); err != nil {
		h.logger.ErrorContext(ctx, "halts: the event could not be logged", slog.String("tenant", tenant), slog.String("err", err.Error()))
	}
	return ev.Receipt
}
