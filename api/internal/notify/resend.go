package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

// resendEndpoint is the Resend transactional email API. Overridable in
// tests via ResendConfig.Endpoint.
const resendEndpoint = "https://api.resend.com/emails"

// ResendConfig configures a ResendNotifier.
type ResendConfig struct {
	// APIKey is the Resend API key (re_...). Required.
	APIKey string
	// From is the sender, e.g. "KIFF <hello@kiff.dev>".
	// Required; must be on a Resend-verified domain.
	From string
	// ReplyTo is the Reply-To address, e.g. "support@kiff.dev".
	// Optional but recommended.
	ReplyTo string
	// Endpoint overrides the Resend API URL (tests). Empty → default.
	Endpoint string
	// HTTPClient overrides the HTTP client (tests). Empty → a client
	// with a 10s timeout.
	HTTPClient *http.Client
	// MaxAttempts bounds send retries on transient failures (5xx /
	// transport). <= 0 → 3.
	MaxAttempts int
}

// ResendNotifier sends real customer emails via Resend's HTTP API.
type ResendNotifier struct {
	apiKey      string
	from        string
	replyTo     string
	endpoint    string
	http        *http.Client
	maxAttempts int
}

// ErrResendNotConfigured is returned by NewResendNotifier when a
// required field is missing.
var ErrResendNotConfigured = errors.New("notify: resend not configured")

// NewResendNotifier validates config and returns a notifier. Returns
// ErrResendNotConfigured when APIKey or From is empty so the caller
// fails fast / falls back to the no-op notifier rather than silently
// not sending.
func NewResendNotifier(cfg ResendConfig) (*ResendNotifier, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("%w: API key required", ErrResendNotConfigured)
	}
	if strings.TrimSpace(cfg.From) == "" {
		return nil, fmt.Errorf("%w: From address required", ErrResendNotConfigured)
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = resendEndpoint
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	attempts := cfg.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	return &ResendNotifier{
		apiKey:      cfg.APIKey,
		from:        cfg.From,
		replyTo:     cfg.ReplyTo,
		endpoint:    endpoint,
		http:        client,
		maxAttempts: attempts,
	}, nil
}

// TenantSuspended implements Notifier.
func (r *ResendNotifier) TenantSuspended(ctx context.Context, to string, lang words.Lang) error {
	lang = words.AccountEmailLang(lang)
	e := words.AccountEmailFor(lang)
	return r.sendMail(ctx, to, e.SuspendedSubject, e.SuspendedBody, "", mail{Lang: lang, Lead: e.SuspendedBody})
}

// TenantUnsuspended implements Notifier.
func (r *ResendNotifier) TenantUnsuspended(ctx context.Context, to string, lang words.Lang) error {
	lang = words.AccountEmailLang(lang)
	e := words.AccountEmailFor(lang)
	return r.sendMail(ctx, to, e.UnsuspendedSubject, e.UnsuspendedBody, "", mail{Lang: lang, Lead: e.UnsuspendedBody})
}

// SupportTicketUpdated implements Notifier. Close-the-loop only: the
// email links back to the in-app thread and carries NO ticket content.
// ticketURL is our own link (app base + ticket id); the layout escapes
// it in the anchor regardless, so there is no injection surface even if
// the id shape ever changes.
func (r *ResendNotifier) SupportTicketUpdated(ctx context.Context, to, ticketURL string, lang words.Lang) error {
	lang = words.AccountEmailLang(lang)
	e := words.AccountEmailFor(lang)
	text := e.SupportBody + " " + ticketURL
	return r.sendMail(ctx, to, e.SupportSubject, text, "", mail{Lang: lang, Lead: e.SupportBody,
		Buttons: []mailButton{{Label: e.SupportView, URL: ticketURL}}})
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	ReplyTo string   `json:"reply_to,omitempty"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

// sendMail draws m with the shared layout and POSTs the email, retrying
// transient failures with a short backoff. It never logs or returns the
// API key; errors carry only the status and Resend's error text. key is
// the provider's idempotency key, none when empty.
func (r *ResendNotifier) sendMail(ctx context.Context, to, subject, text, key string, m mail) error {
	m.Title = subject
	page, err := renderLayout(m)
	if err != nil {
		return err
	}
	return r.sendHTMLWithKey(ctx, to, subject, text, page, key)
}

// sendHTML POSTs one email with an explicit HTML body (for messages
// that need more than a single wrapped paragraph, e.g. a link). Same
// retry/backoff and secret-handling guarantees as send.
func (r *ResendNotifier) sendHTML(ctx context.Context, to, subject, text, htmlContent string) error {
	return r.sendHTMLWithKey(ctx, to, subject, text, htmlContent, "")
}

func (r *ResendNotifier) sendHTMLWithKey(ctx context.Context, to, subject, text, htmlContent, idempotencyKey string) error {
	if strings.TrimSpace(to) == "" {
		return errors.New("notify: empty recipient")
	}
	payload, err := json.Marshal(resendRequest{
		From: r.from, To: []string{to}, ReplyTo: r.replyTo,
		Subject: subject, HTML: htmlContent, Text: text,
	})
	if err != nil {
		return fmt.Errorf("notify: marshal request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt-1) * 500 * time.Millisecond):
			}
		}
		retryable, err := r.attemptWithKey(ctx, payload, idempotencyKey)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable {
			return err
		}
	}
	return fmt.Errorf("notify: send failed after %d attempts: %w", r.maxAttempts, lastErr)
}

// attempt performs one HTTP send. Returns (retryable, err): retryable
// is true for transport errors and 5xx, false for 4xx (won't fix on
// retry) and success.
func (r *ResendNotifier) attempt(ctx context.Context, payload []byte) (bool, error) {
	return r.attemptWithKey(ctx, payload, "")
}

func (r *ResendNotifier) attemptWithKey(ctx context.Context, payload []byte, idempotencyKey string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("notify: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := r.http.Do(req)
	if err != nil {
		return true, fmt.Errorf("notify: post: %w", err) // transport → retry
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode >= 500:
		return true, fmt.Errorf("notify: resend %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	default:
		return false, fmt.Errorf("notify: resend %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}
