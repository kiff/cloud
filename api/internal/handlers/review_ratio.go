package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/reviewload"
	"github.com/kiff/cloud/api/internal/tenant"
)

// ReviewRatioHandler serves GET /v1/me/review-ratio: what share of the
// consequential actions an agent proposed needed nobody.
//
// It is the number the engagement is sold on — give us a week and we
// will tell you what share of the approvals your team does today can
// stop happening — so it is built to be able to say no. It reports an
// insufficient window as insufficient rather than as a flattering
// percentage, and it distinguishes what happened from what would have
// happened, because in observe mode nothing was withheld and every
// figure is a counterfactual.
//
// Read-only. It adds no query the system did not already run: the
// per-outcome counts come from the same window-complete count the
// Control Room header and the proof pack read.
type ReviewRatioHandler struct {
	decisions proposals.Reader
	// tenants resolves observe vs enforce. Optional: without it the
	// response reports enforcing=false, which makes the surface say
	// "would have" — the conservative reading, and the true one for
	// any tenant that has not turned the gate on.
	tenants tenant.Store
	logger  *slog.Logger
	now     func() time.Time
}

type ReviewRatioOption func(*ReviewRatioHandler)

func WithReviewRatioLogger(l *slog.Logger) ReviewRatioOption {
	return func(h *ReviewRatioHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

func WithReviewRatioTenants(s tenant.Store) ReviewRatioOption {
	return func(h *ReviewRatioHandler) {
		if s != nil {
			h.tenants = s
		}
	}
}

func WithReviewRatioClock(now func() time.Time) ReviewRatioOption {
	return func(h *ReviewRatioHandler) {
		if now != nil {
			h.now = now
		}
	}
}

// NewReviewRatioHandler returns nil without a decisions reader, so the
// route is absent rather than answering from nothing. A ratio computed
// over no source is the one output this endpoint must never produce.
func NewReviewRatioHandler(decisions proposals.Reader, opts ...ReviewRatioOption) *ReviewRatioHandler {
	if decisions == nil {
		return nil
	}
	h := &ReviewRatioHandler{
		decisions: decisions,
		logger:    slog.Default(),
		now:       func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

func (h *ReviewRatioHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/me/review-ratio", h.get)
}

// reviewRatioResponse is the wire shape.
//
// Status is "ok" or "insufficient_sample", and a client must branch on
// it rather than on percent. Zero percent means "every action needed a
// person", which is a real and useful answer; an insufficient window
// means we decline to say, and rendering one as the other is the whole
// failure this endpoint is shaped to avoid.
type reviewRatioResponse struct {
	Status string `json:"status"`
	// Percent is the share that needed nobody. Present only when
	// status is ok.
	Percent int `json:"percent,omitempty"`

	WouldRun          int `json:"would_run"`
	WouldReachAPerson int `json:"would_reach_a_person"`
	WouldBeRefused    int `json:"would_be_refused"`
	Total             int `json:"total"`
	// Metered is our own billing limit turning a call away. Excluded
	// from every figure above and reported only so a reader can
	// reconcile against the raw decision count.
	Metered int `json:"metered"`

	// Enforcing says whether these decisions were acted on. False
	// means observe: nothing was withheld, so every figure is what
	// would have happened.
	Enforcing bool `json:"enforcing"`

	MinimumSample int       `json:"minimum_sample"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	Domain        string    `json:"domain,omitempty"`

	// Note travels with the numbers rather than living in a template,
	// so the caveat cannot drift away from the figure it qualifies.
	Note string `json:"note"`
}

func (h *ReviewRatioHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	from, to, err := parseReviewWindow(r, h.now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// ?actor= scopes the figure to one proposing identity, for the
	// timeline filtered to a card's holder. A ratio for the whole
	// domain sitting above rows for one agent is two answers on one
	// screen, and the reader has no way to tell which is which — the
	// same reason this endpoint is control-scoped rather than
	// tenant-wide.
	q := proposals.DecisionQuery{
		From:        from,
		To:          to,
		ControlID:   strings.TrimSpace(r.URL.Query().Get("control")),
		Action:      strings.TrimSpace(r.URL.Query().Get("action")),
		Environment: strings.TrimSpace(r.URL.Query().Get("environment")),
		Actor:       strings.TrimSpace(r.URL.Query().Get("actor")),
	}

	counts, _, err := h.decisions.CountDecisionsByOutcome(r.Context(), id.Tenant, q)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "review ratio: count failed",
			slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		// No fallback figure. An unavailable count is reported as
		// unavailable, because the one thing worse than no number here
		// is a wrong one a customer repeats to their board.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "the decisions could not be counted, so no ratio is reported",
		})
		return
	}

	load := reviewload.FromCounts(counts, h.enforcing(r, id.Tenant))

	resp := reviewRatioResponse{
		Status:            "ok",
		WouldRun:          load.WouldRun,
		WouldReachAPerson: load.WouldReachAPerson,
		WouldBeRefused:    load.WouldBeRefused,
		Total:             load.Total(),
		Metered:           load.Metered,
		Enforcing:         load.Enforcing,
		MinimumSample:     reviewload.MinimumSample,
		From:              from,
		To:                to,
		Domain:            q.ControlID,
	}
	if !load.Sufficient() {
		resp.Status = "insufficient_sample"
		resp.Note = "Fewer decisions than this figure needs to mean anything. " +
			"That is itself worth knowing: an agent proposing this few consequential " +
			"actions does not have an approval queue worth dismantling yet."
	} else {
		resp.Percent = load.Percent()
		resp.Note = reviewRatioNote(load)
	}
	writeJSON(w, http.StatusOK, resp)
}

// reviewRatioNote states what the number does and does not claim, in
// the right tense.
func reviewRatioNote(l reviewload.Load) string {
	tense := "would have run without reaching a person"
	if l.Enforcing {
		tense = "ran without reaching a person"
	}
	return "Share of proposed actions that " + tense + ". " +
		"Refused actions are counted separately and are not in this figure: " +
		"a refusal also saves someone's attention, but it means the work did not happen, " +
		"which is a different thing."
}

// enforcing reports whether decisions in this window were binding.
//
// Fails to false — observe — on any doubt. The conservative direction
// is to describe the numbers as what would have happened, because
// claiming an action was withheld when it was not is the error a
// customer catches.
func (h *ReviewRatioHandler) enforcing(r *http.Request, tenantID string) bool {
	if h.tenants == nil {
		return false
	}
	t, err := h.tenants.Get(r.Context(), tenantID)
	if err != nil {
		return false
	}
	return tenant.ModeFor(t) == tenant.ModeEnforce
}

// parseReviewWindow reads ?from= & ?to= (RFC3339), defaulting to the
// last seven days — the engagement's week-one window, which is what
// this number is for.
func parseReviewWindow(r *http.Request, now time.Time) (time.Time, time.Time, error) {
	to := now
	from := now.AddDate(0, 0, -7)
	if v := strings.TrimSpace(r.URL.Query().Get("to")); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to must be an RFC3339 timestamp")
		}
		to = parsed.UTC()
	}
	if v := strings.TrimSpace(r.URL.Query().Get("from")); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from must be an RFC3339 timestamp")
		}
		from = parsed.UTC()
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, errors.New("to must not be before from")
	}
	return from, to, nil
}
