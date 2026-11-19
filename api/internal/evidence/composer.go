package evidence

import (
	"context"
	"log/slog"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
)

// Composer assembles ControlEvidence from the tenant-scoped read
// stores. Every dependency is optional: a nil store means its section
// reports StatusUnavailable rather than failing the whole composition,
// which is how the two renderers ask for different subsets (the proof
// pack wires a domain source; the Control Room wires a posture reader;
// neither has to wire what it doesn't render).
//
// Primary vs secondary failure: the decision spine and control
// resolution are primary — a hard error there fails the composition,
// because the renderer has nothing honest to show. Secondary sections
// (posture, alerts) are decoration on the decisions payload: a read
// error degrades that section to StatusUnavailable (logged) rather than
// taking down the whole object. Proof-pack receipts stay primary to
// preserve the export's prior fail-on-error behavior.
type Composer struct {
	controls   domain.ControlStore
	reader     proposals.Reader
	receipts   receipt.Store
	posture    posture.Reader
	deliveries alert.DeliveryStore
	source     domain.Source
	registry   *executors.Registry
	now        func() time.Time
	logger     *slog.Logger
}

// Option configures a Composer.
type Option func(*Composer)

// WithControls installs the control store used to resolve identity.
func WithControls(s domain.ControlStore) Option { return func(c *Composer) { c.controls = s } }

// WithDecisions installs the decisions reader (G1).
func WithDecisions(r proposals.Reader) Option { return func(c *Composer) { c.reader = r } }

// WithReceipts installs the receipt store (G2).
func WithReceipts(s receipt.Store) Option { return func(c *Composer) { c.receipts = s } }

// WithPosture installs the posture-history reader (G5).
func WithPosture(p posture.Reader) Option { return func(c *Composer) { c.posture = p } }

// WithDeliveries installs the alert delivery store (G4).
func WithDeliveries(d alert.DeliveryStore) Option { return func(c *Composer) { c.deliveries = d } }

// WithDomainSource installs the domain source + executor registry used
// to assemble the contract / state machine / policy section.
func WithDomainSource(s domain.Source, reg *executors.Registry) Option {
	return func(c *Composer) { c.source = s; c.registry = reg }
}

// WithClock overrides the GeneratedAt clock (deterministic tests /
// byte-for-byte proof-pack golden).
func WithClock(now func() time.Time) Option {
	return func(c *Composer) {
		if now != nil {
			c.now = now
		}
	}
}

// WithLogger sets the logger used when a secondary section degrades on
// a read error. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *Composer) {
		if l != nil {
			c.logger = l
		}
	}
}

// NewComposer builds a Composer from the given options.
func NewComposer(opts ...Option) *Composer {
	c := &Composer{
		now:    func() time.Time { return time.Now().UTC() },
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Compose assembles ControlEvidence for one (tenant, scope, window).
// Section collectors run independently; a missing store degrades that
// section to Unavailable rather than failing the whole object. A
// hard store error on a wired dependency is returned so the caller can
// surface it (the data the renderer would show is genuinely missing).
func (c *Composer) Compose(ctx context.Context, in Input) (ControlEvidence, error) {
	e := ControlEvidence{
		TenantID:    in.TenantID,
		Window:      in.Window,
		GeneratedAt: c.now().UTC(),
	}

	// Control identity. Control Room resolves by id; the proof pack by
	// (action, environment), best-effort (a missing control is not an
	// error there — the pack still documents decisions/receipts).
	ctrl, err := c.resolveControl(ctx, in)
	if err != nil {
		return ControlEvidence{}, err
	}
	e.Control = ctrl

	// Decisions — the spine. The caller supplies the scope-appropriate
	// query (action-scoped for the pack, control-scoped for the room).
	dec, err := c.collectDecisions(ctx, in.TenantID, in.Decisions)
	if err != nil {
		return ControlEvidence{}, err
	}
	// Per-action breakdown for a control that governs several actions
	// (control scope only). Each entry is window-complete for one action,
	// so Govern can show every governed action's own tally, not just the
	// control-wide rollup. Non-fatal: a failed sub-count is skipped.
	if in.ControlID != "" && ctrl != nil && len(ctrl.Actions) > 0 {
		dec.ByAction = c.collectByAction(ctx, in.TenantID, in.Decisions, ctrl.Actions)
	}
	e.Decisions = dec

	// Receipts (+ verifiability via the section). Action scope (proof
	// pack) filters receipts by its single action over the window.
	// Control scope resolves receipts per decision via the explicit
	// proposal->receipt link, so they're mechanically tied to THIS
	// control — no cross-control overclaim.
	if in.Action != "" {
		rec, rerr := c.collectReceipts(ctx, in.TenantID, []string{in.Action}, in.Window)
		if rerr != nil {
			return ControlEvidence{}, rerr
		}
		e.Receipts = rec
	} else {
		e.Receipts = c.collectReceiptsForDecisions(ctx, in.TenantID, dec.Rows, dec.Truncated)
	}

	// Posture history (G5).
	post, err := c.collectPosture(ctx, in.TenantID)
	if err != nil {
		return ControlEvidence{}, err
	}
	e.Posture = post

	// Alert deliveries (G4), enriched onto the decision rows by
	// proposal_id.
	al, err := c.collectAlerts(ctx, in.TenantID, dec.Rows)
	if err != nil {
		return ControlEvidence{}, err
	}
	e.Alerts = al

	// Approvals — Unavailable in Phase 1 (no mechanical scope yet).
	e.Approvals = ApprovalsSection{Status: StatusUnavailable}

	// Domain contract / state machine / policy (proof pack).
	e.Domain = c.collectDomain(ctx, in.TenantID, in.Action)

	return e, nil
}

// resolveControl resolves the control identity for the input's scope.
// ControlID takes precedence (Control Room, where a missing control is
// a 404 upstream); otherwise Action+Environment via FindActiveControl
// (proof pack, where a missing control is tolerated and returns nil).
func (c *Composer) resolveControl(ctx context.Context, in Input) (*domain.Control, error) {
	if c.controls == nil {
		return nil, nil
	}
	switch {
	case in.ControlID != "":
		ctrl, err := domain.FindControlByID(ctx, c.controls, in.TenantID, in.ControlID)
		if err != nil {
			if err == domain.ErrControlNotFound {
				return nil, ErrControlNotFound
			}
			return nil, err
		}
		return &ctrl, nil
	case in.Action != "":
		ctrl, err := domain.FindActiveControl(ctx, c.controls, in.TenantID, in.Action, in.Environment)
		if err != nil {
			// Best-effort for the proof-pack route: no control just
			// means the pack omits the control header.
			return nil, nil
		}
		return &ctrl, nil
	default:
		return nil, nil
	}
}
