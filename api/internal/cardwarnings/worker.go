// Package cardwarnings delivers informational Card notices after accounting
// commits. It has no authorization, approval or Card mutation API.
package cardwarnings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/attention"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/owneremail"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

type Recipients interface {
	HoldRecipients(context.Context, string) ([]string, error)
}
type Mailer interface {
	// CardBudgetWarning sends to, the warning's id, its reason, the Card's
	// link, in the recipient's language.
	CardBudgetWarning(ctx context.Context, to, eventID, text, cardURL string, lang words.Lang) error
}

// RecipientSubjects says whose each address is, so a warning's email is in
// that person's language (personlang).
type RecipientSubjects interface {
	HoldRecipientsWithSubjects(context.Context, string) ([]owneremail.Recipient, error)
}

// Tenants reads the account, for its people's languages.
type Tenants interface {
	Get(context.Context, string) (tenant.Tenant, error)
}
type Store interface {
	ScheduleWarningObservations(context.Context) error
	ProcessWarningWork(context.Context) error
	PendingWarnings(context.Context) ([]mandate.CardWarning, error)
	DeliverWarning(context.Context, mandate.CardWarning, func(context.Context, mandate.CardWarning) error, func(context.Context, mandate.CardWarning) ([]string, error), func(context.Context, mandate.CardWarning, string) error) error
}

type Worker struct {
	Store      Store
	Attention  *attention.Store
	Recipients Recipients
	Mailer     Mailer
	// Tenants, when set, puts each email in its recipient's language;
	// English without it.
	Tenants Tenants
	Targets alert.Store
	Sink    alert.Sink
	BaseURL string
	Logger  *slog.Logger
	// StageTimeout bounds each stage of a pass on its own (30 s when zero),
	// so a slow stage can't use up the others' time.
	StageTimeout time.Duration
	// ShutdownGrace is how long a pass under way when Run's context ends may
	// still take (10 s when zero), so a stop doesn't cut a stage off midway.
	ShutdownGrace time.Duration
}

// Run passes once a second until ctx ends. A pass under way then finishes,
// within ShutdownGrace; no new one starts.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastScheduled := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			passCtx, done := w.graceful(ctx)
			lastScheduled = w.pass(passCtx, now, lastScheduled)
			done()
		}
	}
}

// graceful is a context for one pass that outlives ctx's end by
// ShutdownGrace: the API's stop cancels ctx, and a stage cut off there would
// only be redone on the next start.
func (w *Worker) graceful(ctx context.Context) (context.Context, context.CancelFunc) {
	grace := w.ShutdownGrace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	passCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var mu sync.Mutex
	var timer *time.Timer
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		timer = time.AfterFunc(grace, cancel)
	})
	return passCtx, func() {
		stop()
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		cancel()
	}
}

// pass runs one round of the three stages, each with its own timeout, and
// answers when observations were last scheduled.
func (w *Worker) pass(ctx context.Context, now, lastScheduled time.Time) time.Time {
	if now.Sub(lastScheduled) >= time.Minute {
		if err := w.stage(ctx, w.Store.ScheduleWarningObservations); err != nil {
			w.log("schedule", err)
		} else {
			lastScheduled = now
		}
	}
	if err := w.stage(ctx, w.Store.ProcessWarningWork); err != nil {
		w.log("accounting observation", err)
	}
	if err := w.stage(ctx, w.Deliver); err != nil {
		w.log("delivery", err)
	}
	return lastScheduled
}

func (w *Worker) stage(ctx context.Context, f func(context.Context) error) error {
	timeout := w.StageTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	stageCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return f(stageCtx)
}

func (w *Worker) log(stage string, err error) {
	if w.Logger == nil {
		return
	}
	if transient(err) {
		// Kept and tried again on a later pass (the work's backoff): a
		// deploy's schema step can take its tables in the other order.
		w.Logger.Info("Card warnings: "+stage+" tried again later", "error", err)
		return
	}
	w.Logger.Warn("Card warnings: "+stage, "error", err)
}

// transient is a deadlock or a serialization failure: Postgres rolled the
// transaction back, and the same work succeeds when tried again.
func transient(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001")
}

func (w *Worker) Deliver(ctx context.Context) error {
	events, err := w.Store.PendingWarnings(ctx)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = w.Store.DeliverWarning(ctx, event, w.attention, w.recipients, w.send); err != nil {
			w.log("delivery for "+event.TenantID+"/"+event.ID, err)
		}
	}
	return nil
}

func (w *Worker) attention(ctx context.Context, e mandate.CardWarning) error {
	if w.Attention == nil {
		return fmt.Errorf("warning attention store unavailable")
	}
	return w.Attention.CardWarning(ctx, e.TenantID, e.ID, e.CardID, e.Reason, e.At, e.ResolvedAt != nil)
}

func (w *Worker) recipients(ctx context.Context, e mandate.CardWarning) ([]string, error) {
	out := []string{}
	if w.Recipients != nil && w.Mailer != nil {
		people, err := w.Recipients.HoldRecipients(ctx, e.TenantID)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, email := range people {
			email = strings.TrimSpace(email)
			if email != "" && !seen[email] {
				seen[email] = true
				out = append(out, "email:"+email)
			}
		}
	}
	if w.Targets != nil && w.Sink != nil {
		targets, err := w.Targets.ListTargets(ctx, e.TenantID)
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			if warningTarget(target) {
				out = append(out, "webhook:"+target.ID)
			}
		}
	}
	return out, nil
}

// Existing all-decision-alert targets must not receive a new payload schema.
// A target opts in through an explicit warning action or outcome filter.
func warningTarget(t alert.Target) bool {
	return (t.Action == "card_budget" || slices.Contains(t.Outcomes, "card_warning")) && t.Matches("card_budget", "card_warning")
}

func (w *Worker) send(ctx context.Context, e mandate.CardWarning, destination string) error {
	base := strings.TrimRight(w.BaseURL, "/")
	if base == "" {
		base = "https://app.kiff.dev"
	}
	cardURL := base + "/cards/" + url.PathEscape(e.CardID) + "/warnings"
	if email, ok := strings.CutPrefix(destination, "email:"); ok {
		if w.Mailer == nil {
			return fmt.Errorf("warning email temporarily unavailable")
		}
		lang := w.languageOf(ctx, e.TenantID, email)
		return w.Mailer.CardBudgetWarning(ctx, email, e.ID, words.WarningReason(e, lang), cardURL, lang)
	}
	id, ok := strings.CutPrefix(destination, "webhook:")
	if !ok {
		return fmt.Errorf("unknown warning destination")
	}
	targets, err := w.Targets.ListTargets(ctx, e.TenantID)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.ID != id {
			continue
		}
		if !warningTarget(target) {
			return nil
		}
		body, err := json.Marshal(map[string]any{"schema_version": "kiff.card-warning.v1", "id": e.ID, "type": "card_warning", "card_id": e.CardID, "kind": e.Kind, "reason": e.Reason, "recorded_at": e.At, "card_url": cardURL})
		if err != nil {
			return err
		}
		// Slack incoming webhooks require text; registered generic webhooks use
		// the versioned body and deduplicate its stable id on external retries.
		if u, err := url.Parse(target.URL); err == nil && u.Hostname() == "hooks.slack.com" {
			body, _ = json.Marshal(map[string]string{"text": e.Reason + " <" + cardURL + "|Review the Card>"})
		}
		return w.Sink.Send(ctx, target, body)
	}
	return nil // removed target: never send to a stale endpoint
}

// languageOf is the language of the person at email on the account: theirs
// when the recipients say whose the address is, else the account's last,
// else English.
func (w *Worker) languageOf(ctx context.Context, tenantID, email string) words.Lang {
	if w.Tenants == nil {
		return words.EN
	}
	t, err := w.Tenants.Get(ctx, tenantID)
	if err != nil {
		return words.EN
	}
	if bySubject, ok := w.Recipients.(RecipientSubjects); ok {
		if people, err := bySubject.HoldRecipientsWithSubjects(ctx, tenantID); err == nil {
			for _, p := range people {
				if strings.EqualFold(strings.TrimSpace(p.Email), email) {
					return personlang.Of(t, p.Subject)
				}
			}
		}
	}
	return personlang.Account(t)
}
