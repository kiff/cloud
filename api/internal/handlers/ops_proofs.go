package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/operatoralerts"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/proofs"
)

// The integration proofs (proofs): the runner's ingest, and the operator
// console's monitoring page, replay and overrides.

// ProofIngestHandler serves POST /v1/ops/proofs: the proof runner in
// kiff/agents sends one run's summary. It is mounted on the public mux,
// outside the session middleware, because its only credential is a GitHub
// Actions OIDC token for the configured repository, branch, workflow and
// audience. Any refusal of the token answers 404, like the rest of
// /v1/ops, so the route isn't discoverable.
type ProofIngestHandler struct {
	verifier *proofs.OIDCVerifier
	store    proofs.Store
	tenants  map[string]bool
	versions proofs.VersionChecker
	alerts   operatoralerts.Store // optional: the error-run alerts go to the operator queue
	logger   *slog.Logger
	now      func() time.Time
}

// NewProofIngestHandler returns nil, so nothing is mounted, unless the
// verifier, the store and at least one test tenant are configured.
// versions nil leaves every run uncounted.
func NewProofIngestHandler(verifier *proofs.OIDCVerifier, store proofs.Store, testTenants []string,
	versions proofs.VersionChecker, alerts operatoralerts.Store, logger *slog.Logger) *ProofIngestHandler {
	set := map[string]bool{}
	for _, t := range testTenants {
		if t = strings.TrimSpace(t); t != "" {
			set[t] = true
		}
	}
	if verifier == nil || store == nil || len(set) == 0 {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ProofIngestHandler{verifier: verifier, store: store, tenants: set, versions: versions,
		alerts: alerts, logger: logger, now: time.Now}
}

// WithClock sets the clock, for tests.
func (h *ProofIngestHandler) WithClock(now func() time.Time) *ProofIngestHandler {
	h.now = now
	return h
}

// Register mounts the ingest on the public mux. No-op on nil.
func (h *ProofIngestHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("POST /v1/ops/proofs", h.ingest)
}

const maxProofBody = 64 << 10

func (h *ProofIngestHandler) ingest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	claims, err := h.verifier.VerifyRequest(ctx, r)
	if err != nil {
		h.logger.WarnContext(ctx, "proofs: ingest refused", slog.String("err", err.Error()))
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProofBody+1))
	if err != nil || len(body) > maxProofBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too_large"})
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields() // an extra field could carry what redaction missed
	var run proofs.Run
	if err := dec.Decode(&run); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	// The runner's commit is the one GitHub signed into the token (sha):
	// the body may only repeat it.
	if claims.Sha == "" || run.RunnerVersion != claims.Sha {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_run", "reason": "runner_version_not_token"})
		return
	}
	now := h.now()
	if err := proofs.Validate(run, h.tenants, now); err != nil {
		code := strings.TrimPrefix(err.Error(), proofs.ErrInvalid.Error()+": ")
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_run", "reason": code})
		return
	}
	// KIFF's fields, never the runner's.
	run.ReceivedAt = now.UTC()
	run.Counted = false
	run.GitHubRunID, run.GitHubRunAttempt = digitsOnly(claims.RunID), digitsOnly(claims.RunAttempt)
	if h.versions != nil {
		on, err := h.versions.OnMain(ctx, run.RunnerVersion)
		if err != nil {
			h.logger.WarnContext(ctx, "proofs: runner version unconfirmed; run kept uncounted",
				slog.String("run_id", run.RunID), slog.String("err", err.Error()))
		}
		run.Counted = err == nil && on
	}
	if err := h.store.Append(ctx, run); err != nil {
		if errors.Is(err, proofs.ErrDuplicate) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "run_exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store"})
		return
	}
	h.logger.InfoContext(ctx, "proofs: run kept", slog.String("run_id", run.RunID), slog.String("app", run.App),
		slog.String("outcome", run.Outcome), slog.Bool("counted", run.Counted), slog.String("gh_run", claims.RunID))
	h.alertErrors(ctx, run, now)
	writeJSON(w, http.StatusCreated, map[string]any{"run_id": run.RunID, "counted": run.Counted})
}

// alertErrors raises the error-run alerts (proofs.ErrorAlerts) on the
// operator queue, or in the log when no queue is wired. Best effort: the
// run is kept either way.
func (h *ProofIngestHandler) alertErrors(ctx context.Context, run proofs.Run, now time.Time) {
	if run.Outcome != proofs.OutcomeError {
		return
	}
	runs, err := h.store.RunsSince(ctx, now.Add(-proofs.Retention))
	if err != nil {
		h.logger.ErrorContext(ctx, "proofs: error-run check unreadable", slog.String("err", err.Error()))
		return
	}
	for _, a := range proofs.ErrorAlerts(run, runs) {
		kind := a.Kind
		if a.App != "" {
			kind += ":" + a.App
		}
		if h.alerts == nil {
			h.logger.ErrorContext(ctx, "proofs: operator alert", slog.String("kind", kind), slog.String("detail", a.Detail))
			continue
		}
		if _, err := h.alerts.Upsert(ctx, operatoralerts.Upsert{TenantID: a.Tenant, Kind: kind,
			Severity: operatoralerts.SeverityHigh, Detail: a.Detail}, now); err != nil {
			h.logger.ErrorContext(ctx, "proofs: operator alert not queued", slog.String("kind", kind), slog.String("err", err.Error()))
		}
	}
}

// ProofStateHandler serves GET /v1/ops/proofs/state?entry=name@rev to
// planque: whether one catalog entry revision is freshly proven. Planque
// has no cross-account identity in KIFF (its connector keys are each one
// account's), so this read takes a service token listed in config
// (KIFF_PROOF_STATE_TOKENS), checked in constant time; operators' sessions
// don't reach it, and any refusal answers 404 like the rest of /v1/ops.
type ProofStateHandler struct {
	reader *proofs.Reader
	tokens [][sha256.Size]byte
}

// NewProofStateHandler returns nil, so nothing is mounted, without a
// reader or a token.
func NewProofStateHandler(reader *proofs.Reader, tokens []string) *ProofStateHandler {
	h := &ProofStateHandler{reader: reader}
	for _, t := range tokens {
		if t = strings.TrimSpace(t); t != "" {
			h.tokens = append(h.tokens, sha256.Sum256([]byte(t)))
		}
	}
	if reader == nil || len(h.tokens) == 0 {
		return nil
	}
	return h
}

// Register mounts the read on the public mux. No-op on nil.
func (h *ProofStateHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/ops/proofs/state", h.state)
}

// allowed compares the bearer's digest with every token's, all of them
// every time, so neither a match's position nor a length shows in timing.
func (h *ProofStateHandler) allowed(r *http.Request) bool {
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || bearer == "" {
		return false
	}
	got := sha256.Sum256([]byte(bearer))
	match := 0
	for _, t := range h.tokens {
		match |= subtle.ConstantTimeCompare(got[:], t[:])
	}
	return match == 1
}

func (h *ProofStateHandler) state(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(r) {
		http.NotFound(w, r)
		return
	}
	entry := r.URL.Query().Get("entry")
	if !proofs.ValidEntry(entry) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "entry"})
		return
	}
	writeJSON(w, http.StatusOK, h.reader.EntryFor(r.Context(), entry))
}

// opsProofs is the console side, on OpsHandler.
type opsProofs struct {
	store  proofs.Store
	events proofs.EventSource // nil: the replay shows the summary only
	// releaseOK lets "release anyway" be used at all
	// (KIFF_PROOF_RELEASE_ANYWAY=on); each release also needs the owner's
	// yes, recorded with it.
	releaseOK bool
	// testKeys are the proof runner's phone keys: a session holding one
	// never makes an override.
	testKeys proofs.TestKeys
}

// WithOpsProofTestKeys refuses every override from a session that holds
// one of the proof runner's test phone keys (KIFF_PROOF_TEST_KEYS). Apply
// after WithOpsProofs.
func WithOpsProofTestKeys(keys proofs.TestKeys) OpsOption {
	return func(h *OpsHandler) {
		if h.proofs != nil {
			h.proofs.testKeys = keys
		}
	}
}

// holdsProofTestKey reports whether ident's device or approval key is one
// of the proof runner's test phone keys.
func holdsProofTestKey(keys proofs.TestKeys, ident auth.Identity) bool {
	return keys.Has(ident.DeviceKey) || keys.Has(ident.ApprovalKey)
}

// WithOpsProofs mounts the integration proofs' monitoring page and replay
// (GET /v1/ops/proofs, GET /v1/ops/proofs/{run_id}) and, with the control
// plane wired, the overrides of an app's write hold.
func WithOpsProofs(store proofs.Store, events proofs.EventSource, releaseAnyway bool) OpsOption {
	return func(h *OpsHandler) {
		if store != nil {
			h.proofs = &opsProofs{store: store, events: events, releaseOK: releaseAnyway}
		}
	}
}

func (h *OpsHandler) registerProofs(mux *http.ServeMux) {
	if h.proofs == nil {
		return
	}
	mux.HandleFunc("GET /v1/ops/proofs", h.gate(h.proofRows))
	mux.HandleFunc("GET /v1/ops/proofs/{run_id}", h.gate(h.proofRun))
	if h.audit != nil {
		mux.HandleFunc("POST /v1/ops/proofs/apps/{app}/hold", h.gate(h.proofHold))
		mux.HandleFunc("POST /v1/ops/proofs/apps/{app}/release", h.gate(h.proofRelease))
		mux.HandleFunc("POST /v1/ops/proofs/apps/{app}/clear", h.gate(h.proofClear))
	}
}

// proofRows serves GET /v1/ops/proofs: one row per app.
func (h *OpsHandler) proofRows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := h.now().UTC()
	runs, err := h.proofs.store.RunsSince(ctx, now.AddDate(0, 0, -proofs.StripDays))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store"})
		return
	}
	overrides, err := h.proofs.store.Overrides(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": proofs.Rows(runs, overrides, now), "release_anyway": h.proofs.releaseOK})
}

// proofRun serves GET /v1/ops/proofs/{run_id}: the summary and, when the
// event source has them, the run's events, each checked again for
// secrets before it's shown.
func (h *OpsHandler) proofRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	run, err := h.proofs.store.Run(ctx, r.PathValue("run_id"))
	if errors.Is(err, proofs.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store"})
		return
	}
	out := map[string]any{"run": run}
	if h.proofs.events == nil {
		out["events_unavailable"] = true
		writeJSON(w, http.StatusOK, out)
		return
	}
	lines, err := h.proofs.events.Events(ctx, run.EventsKey)
	if err != nil {
		h.logger.WarnContext(ctx, "proofs: events unreadable", slog.String("run_id", run.RunID), slog.String("err", err.Error()))
		out["events_unavailable"] = true
		writeJSON(w, http.StatusOK, out)
		return
	}
	events := make([]json.RawMessage, 0, len(lines))
	redacted := 0
	for _, line := range lines {
		clean, changed := proofs.RedactEvent(line)
		if changed {
			redacted++
		}
		events = append(events, clean)
	}
	if redacted > 0 {
		h.logger.ErrorContext(ctx, "proofs: secret-looking values redacted from a run's events",
			slog.String("run_id", run.RunID), slog.Int("lines", redacted))
	}
	out["events"] = events
	out["redacted"] = redacted
	writeJSON(w, http.StatusOK, out)
}

// The longest an override can stand: a hold, 30 days; a release, 7. Both
// are under the 60-day sweep, so a sweep never ends one early.
const (
	maxProofHold    = 30 * 24 * time.Hour
	maxProofRelease = 7 * 24 * time.Hour
)

type proofOverrideRequest struct {
	Reason string `json:"reason"`
	// Until is when the override ends (RFC 3339). Required; capped.
	Until string `json:"until"`
	// OwnerApproval is where the owner said yes to this release (a link or
	// a note naming it). Required for a release.
	OwnerApproval string `json:"owner_approval"`
}

func readProofOverride(r *http.Request) (proofOverrideRequest, error) {
	var req proofOverrideRequest
	if r.Body == nil {
		return req, errors.New("body")
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		return req, err
	}
	req.Reason = strings.TrimSpace(req.Reason)
	req.OwnerApproval = strings.TrimSpace(req.OwnerApproval)
	return req, nil
}

func (h *OpsHandler) proofHold(w http.ResponseWriter, r *http.Request) {
	h.proofOverride(w, r, proofs.OverrideHold, operatoraudit.ActionProofHold)
}

func (h *OpsHandler) proofRelease(w http.ResponseWriter, r *http.Request) {
	h.proofOverride(w, r, proofs.OverrideRelease, operatoraudit.ActionProofRelease)
}

func (h *OpsHandler) proofClear(w http.ResponseWriter, r *http.Request) {
	h.proofOverride(w, r, proofs.OverrideClear, operatoraudit.ActionProofClear)
}

// proofOverride records one override. The audit entry is written first: if
// it fails, nothing changes; if the override then fails to save, the audit
// shows an attempt that didn't apply, never a change with no audit.
func (h *OpsHandler) proofOverride(w http.ResponseWriter, r *http.Request, action, auditAction string) {
	ctx := r.Context()
	if holdsProofTestKey(h.proofs.testKeys, auth.MustIdentity(ctx)) {
		// The test phone proves the hold; it never lifts or sets one.
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "proof_test_key"})
		return
	}
	app := r.PathValue("app")
	if !validProofApp(app) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "app"})
		return
	}
	req, err := readProofOverride(r)
	if err != nil || req.Reason == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reason_required"})
		return
	}
	now := h.now().UTC()
	o := proofs.Override{App: app, Action: action, Operator: auth.MustIdentity(ctx).Subject, Reason: req.Reason}
	if action != proofs.OverrideClear {
		until, err := time.Parse(time.RFC3339, req.Until)
		if err != nil || !until.After(now) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "until_required"})
			return
		}
		limit := maxProofHold
		if action == proofs.OverrideRelease {
			limit = maxProofRelease
		}
		if until.Sub(now) > limit {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "until_too_far"})
			return
		}
		u := until.UTC()
		o.Until = &u
	}
	if action == proofs.OverrideRelease {
		// Releasing a red app lets a broken integration's writes go
		// without asking: the owner's yes, never an operator's alone.
		if !h.proofs.releaseOK {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "release_needs_owner"})
			return
		}
		if req.OwnerApproval == "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner_approval_required"})
			return
		}
		o.OwnerApproval = req.OwnerApproval
	}
	meta := map[string]any{"app": app}
	if o.Until != nil {
		meta["until"] = o.Until.Format(time.RFC3339)
	}
	if o.OwnerApproval != "" {
		meta["owner_approval"] = o.OwnerApproval
	}
	if err := h.audit.Record(ctx, operatoraudit.Entry{OperatorSubject: o.Operator, Action: auditAction,
		Reason: o.Reason, Metadata: meta}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "audit"})
		return
	}
	saved, err := h.proofs.store.AppendOverride(ctx, o)
	if err != nil {
		h.logger.ErrorContext(ctx, "proofs: override audited but not saved", slog.String("app", app), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store"})
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

// digitsOnly is s when it's a GitHub number (at most 20 digits), else "".
func digitsOnly(s string) string {
	if s == "" || len(s) > 20 {
		return ""
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return s
}

func validProofApp(app string) bool {
	if app == "" || len(app) > 64 {
		return false
	}
	for i, c := range app {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}
