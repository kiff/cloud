package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// payloadSchemaVersion identifies the webhook body shape.
const payloadSchemaVersion = "kiff.alert.v1"

// defaultDeliveryTimeout bounds a single fan-out (store read + all
// webhook POSTs). Generous enough for a slow webhook, short enough
// that a stuck target can't leak goroutines indefinitely.
const defaultDeliveryTimeout = 10 * time.Second

// deliveryRecordTimeout bounds persisting one delivery record. It is
// independent of the per-fan-out send timeout on purpose: a slow or hung webhook can exhaust the dispatch deadline,
// and the delivery record is exactly the evidence that slow/failed
// case most needs — so the write must not be cancelled along with it.
const deliveryRecordTimeout = 5 * time.Second

// Sink delivers one alert payload to one target. The HTTP webhook is
// the v0 sink; email/Slack sinks implement the same interface later.
type Sink interface {
	Send(ctx context.Context, target Target, payload []byte) error
}

// webhookPayload is the JSON body POSTed to a webhook target. It
// mirrors Event but with stable wire tags and a schema version.
// Crucially it carries no request parameters.
type webhookPayload struct {
	SchemaVersion string       `json:"schema_version"`
	Type          string       `json:"type"`
	TenantID      string       `json:"tenant_id"`
	Domain        string       `json:"domain,omitempty"`
	Action        string       `json:"action"`
	EntityID      string       `json:"entity_id"`
	EntityType    string       `json:"entity_type,omitempty"`
	Outcome       string       `json:"outcome"`
	Reasons       []string     `json:"reasons,omitempty"`
	Approval      *Approval    `json:"approval,omitempty"`
	Hold          *Hold        `json:"hold,omitempty"`
	Notify        *[]string    `json:"notify,omitempty"`
	Request       *PackRequest `json:"request,omitempty"`
	Connection    *Connection  `json:"connection,omitempty"`
	Handoff       *Handoff     `json:"handoff,omitempty"`
	DecidedAt     time.Time    `json:"decided_at"`
	EventID       string       `json:"event_id"`
}

// Dispatcher is the production Notifier. It looks up the tenant's
// targets and fans out to the matching ones, best-effort, on a
// detached context so the decide path is never blocked or delayed.
type Dispatcher struct {
	store      Store
	sink       Sink
	deliveries DeliveryStore // optional; persists delivery attempts
	retries    RetryStore    // optional; queues a failed delivery to be sent again
	logger     *slog.Logger
	baseURL    string // absolutizes a relative review URL; e.g. https://app.kiff.dev
	timeout    time.Duration
	now        func() time.Time
	newID      func() string
}

// Option configures a Dispatcher.
type Option func(*Dispatcher)

// WithDeliveryStore installs the store that persists each delivery
// attempt (delivered / failed / blocked) against the triggering
// decision. When nil, deliveries are dispatched but not
// recorded (unchanged behavior).
func WithDeliveryStore(s DeliveryStore) Option {
	return func(d *Dispatcher) { d.deliveries = s }
}

// WithRetries queues each failed delivery to be sent again on a backoff.
// When nil, a failed delivery is only recorded.
func WithRetries(s RetryStore) Option {
	return func(d *Dispatcher) { d.retries = s }
}

// WithBaseURL sets the app base URL used to absolutize a relative
// approval review URL in the payload (so the webhook receiver gets a
// clickable link). When empty, the review URL is sent as-is.
func WithBaseURL(u string) Option {
	return func(d *Dispatcher) { d.baseURL = strings.TrimRight(u, "/") }
}

// WithTimeout overrides the per-fan-out delivery timeout.
func WithTimeout(d time.Duration) Option {
	return func(disp *Dispatcher) {
		if d > 0 {
			disp.timeout = d
		}
	}
}

// WithLogger substitutes the logger.
func WithLogger(l *slog.Logger) Option {
	return func(d *Dispatcher) {
		if l != nil {
			d.logger = l
		}
	}
}

// NewDispatcher wires the store and sink. Returns nil when either is
// nil so callers can treat "no dispatcher" as "alerts disabled".
func NewDispatcher(store Store, sink Sink, opts ...Option) *Dispatcher {
	if store == nil || sink == nil {
		return nil
	}
	d := &Dispatcher{
		store:   store,
		sink:    sink,
		logger:  slog.Default(),
		timeout: defaultDeliveryTimeout,
		now:     func() time.Time { return time.Now().UTC() },
		newID:   newDeliveryID,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Notify implements Notifier. Schedules delivery and returns
// immediately — it never blocks the caller (the decide path).
func (d *Dispatcher) Notify(ev Event) {
	go d.deliver(ev)
}

// deliver runs the fan-out on a detached, timeout-bounded context.
// Best-effort: every failure is logged, none propagates.
func (d *Dispatcher) deliver(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()

	targets, err := d.store.ListTargets(ctx, ev.TenantID)
	if err != nil {
		d.logger.WarnContext(ctx, "alert: list targets failed",
			slog.String("tenant", ev.TenantID),
			slog.String("err", err.Error()),
		)
		return
	}

	if ev.EventID == "" {
		ev.EventID = newEventID()
	}
	payload := d.buildPayload(ev)
	body, err := json.Marshal(payload)
	if err != nil {
		d.logger.WarnContext(ctx, "alert: marshal payload failed",
			slog.String("tenant", ev.TenantID),
			slog.String("err", err.Error()),
		)
		return
	}

	for _, t := range targets {
		if !t.Matches(ev.Action, ev.Outcome) {
			continue
		}
		err := d.sink.Send(ctx, t, body)
		switch {
		case err == nil:
			d.logger.InfoContext(ctx, "alert delivered",
				slog.String("tenant", ev.TenantID),
				slog.String("target", t.ID),
				slog.String("action", ev.Action),
				slog.String("outcome", ev.Outcome),
			)
			d.recordDelivery(ctx, ev, t, DeliveryDelivered, "")
		case errors.Is(err, ErrBlockedAddress):
			// SSRF guard refused the address — a first-class status,
			// not a silent drop.
			d.logger.WarnContext(ctx, "alert blocked by SSRF guard",
				slog.String("tenant", ev.TenantID),
				slog.String("target", t.ID),
				slog.String("outcome", ev.Outcome),
			)
			d.recordDelivery(ctx, ev, t, DeliveryBlocked, err.Error())
		default:
			d.logger.WarnContext(ctx, "alert: deliver failed",
				slog.String("tenant", ev.TenantID),
				slog.String("target", t.ID),
				slog.String("outcome", ev.Outcome),
				slog.String("err", err.Error()),
			)
			d.recordDelivery(ctx, ev, t, DeliveryFailed, err.Error())
			d.scheduleRetry(ev, t, body, err)
		}
	}
}

// scheduleRetry queues a failed delivery to be sent again, on its
// own short context like the delivery record.
func (d *Dispatcher) scheduleRetry(ev Event, t Target, body []byte, sendErr error) {
	if d.retries == nil {
		return
	}
	now := d.now()
	ctx, cancel := context.WithTimeout(context.Background(), deliveryRecordTimeout)
	defer cancel()
	r := Retry{ID: d.newID(), TenantID: ev.TenantID, TargetID: t.ID, EventID: ev.EventID, ProposalID: ev.ProposalID,
		Action: ev.Action, Outcome: ev.Outcome, Body: body, Attempts: 1, NextAt: now.Add(RetryBackoff[0]),
		LastError: sendErr.Error(), CreatedAt: now}
	if err := d.retries.ScheduleRetry(ctx, r); err != nil {
		d.logger.WarnContext(ctx, "alert: schedule retry failed",
			slog.String("tenant", ev.TenantID), slog.String("target", t.ID), slog.String("err", err.Error()))
	}
}

// recordDelivery persists one delivery attempt. Best-effort: a failed
// record is logged, never propagated — the decide path's dispatch is
// already detached. No-op when no delivery store is wired.
//
// The write runs on its own short context (deliveryRecordTimeout), not
// the per-fan-out send context: a slow/hung sink can exhaust the
// dispatch deadline, and the record is the evidence that case most
// needs, so it must not be cancelled with the send.
func (d *Dispatcher) recordDelivery(ctx context.Context, ev Event, t Target, status, detail string) {
	if d.deliveries == nil {
		return
	}
	rec := Delivery{
		ID:          d.newID(),
		TenantID:    ev.TenantID,
		ProposalID:  ev.ProposalID,
		TargetID:    t.ID,
		TargetType:  "webhook",
		Action:      ev.Action,
		Outcome:     ev.Outcome,
		Status:      status,
		Detail:      detail,
		AttemptedAt: d.now(),
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), deliveryRecordTimeout)
	defer cancel()
	if err := d.deliveries.RecordDelivery(writeCtx, rec); err != nil {
		d.logger.WarnContext(ctx, "alert: record delivery failed",
			slog.String("tenant", ev.TenantID),
			slog.String("target", t.ID),
			slog.String("err", err.Error()),
		)
	}
}

func (d *Dispatcher) buildPayload(ev Event) webhookPayload {
	p := webhookPayload{
		SchemaVersion: payloadSchemaVersion,
		Type:          "kiff.decision.alert",
		EventID:       ev.EventID,
		TenantID:      ev.TenantID,
		Domain:        ev.Domain,
		Action:        ev.Action,
		EntityID:      ev.EntityID,
		EntityType:    ev.EntityType,
		Outcome:       ev.Outcome,
		Reasons:       ev.Reasons,
		DecidedAt:     ev.DecidedAt,
	}
	if ev.Approval != nil {
		a := *ev.Approval
		if d.baseURL != "" && strings.HasPrefix(a.ReviewURL, "/") {
			a.ReviewURL = d.baseURL + a.ReviewURL
		}
		p.Approval = &a
	}
	if ev.Hold != nil {
		h := *ev.Hold
		if d.baseURL != "" && strings.HasPrefix(h.ReviewURL, "/") {
			h.ReviewURL = d.baseURL + h.ReviewURL
		}
		p.Hold = &h
	}
	if ev.Request != nil {
		r := *ev.Request
		p.Request = &r
	}
	if ev.Notify != nil {
		n := append([]string{}, (*ev.Notify)...)
		p.Notify = &n
	}
	if ev.Connection != nil {
		c := *ev.Connection
		p.Connection = &c
	}
	if ev.Handoff != nil {
		h := *ev.Handoff
		p.Handoff = &h
	}
	switch ev.Outcome {
	case HoldAnswered:
		p.Type = "kiff.hold.answered"
	case PackRequested, PackRequestAnswered:
		p.Type = "kiff.pack_request"
	case ConnectionSignedOut:
		p.Type = "kiff.connection.signed_out"
	case HandoffAnswered:
		p.Type = "kiff.handoff.answered"
	}
	return p
}

// HTTPSink POSTs the payload as JSON to the target URL. A non-2xx
// response is an error (logged by the dispatcher). Its client dials
// through an SSRF guard: the delivery connection is refused if
// the resolved IP is in a blocked range, checked at connect time so
// DNS rebinding can't slip an internal address past registration.
type HTTPSink struct {
	client *http.Client
}

// NewHTTPSink returns an HTTP webhook sink whose dialer refuses
// blocked (private/loopback/link-local/metadata) addresses. Set
// allowPrivate true only for local/dev (the env-gated escape hatch);
// production passes false.
func NewHTTPSink(allowPrivate bool) *HTTPSink {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   guardedDialControl(allowPrivate),
	}
	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		// No idle connection reuse across hosts is needed for a
		// fire-and-forget webhook; keep defaults otherwise.
	}
	return &HTTPSink{client: &http.Client{Timeout: 8 * time.Second, Transport: transport}}
}

// Send implements Sink.
func (s *HTTPSink) Send(ctx context.Context, target Target, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kiff-alert/1")
	if target.Secret != "" {
		req.Header.Set("KIFF-Signature", SignatureHeader(target.Secret, time.Now(), payload))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
