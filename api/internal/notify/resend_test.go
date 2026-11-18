package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testKey = "re_test_SECRET_do_not_leak"

func TestNewResendNotifier_Validation(t *testing.T) {
	t.Parallel()
	if _, err := NewResendNotifier(ResendConfig{From: "x"}); err == nil {
		t.Error("want error on empty API key")
	}
	if _, err := NewResendNotifier(ResendConfig{APIKey: "k"}); err == nil {
		t.Error("want error on empty From")
	}
	if _, err := NewResendNotifier(ResendConfig{APIKey: "k", From: "x"}); err != nil {
		t.Errorf("valid config: %v", err)
	}
}

func TestResend_TenantSuspended_SendsCorrectEmail(t *testing.T) {
	t.Parallel()
	var got resendRequest
	var authHdr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHdr = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"email_123"}`))
	}))
	defer srv.Close()

	n, err := NewResendNotifier(ResendConfig{
		APIKey: testKey, From: "KIFF Operations <ops@kiff.dev>", ReplyTo: "support@kiff.dev",
		Endpoint: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.TenantSuspended(context.Background(), "customer@example.com"); err != nil {
		t.Fatalf("TenantSuspended: %v", err)
	}
	if got.From != "KIFF Operations <ops@kiff.dev>" {
		t.Errorf("from: %q", got.From)
	}
	if got.ReplyTo != "support@kiff.dev" {
		t.Errorf("reply_to: %q", got.ReplyTo)
	}
	if len(got.To) != 1 || got.To[0] != "customer@example.com" {
		t.Errorf("to: %v", got.To)
	}
	if got.Subject != subjectSuspended {
		t.Errorf("subject: %q", got.Subject)
	}
	if !strings.Contains(got.Text, "temporarily suspended") {
		t.Errorf("text missing expected copy: %q", got.Text)
	}
	if authHdr != "Bearer "+testKey {
		t.Errorf("auth header not set correctly")
	}
}

// The internal operator reason must never reach the customer email.
func TestResend_NoInternalReasonInEmail(t *testing.T) {
	t.Parallel()
	const secretReason = "40% blocked — investigating fraud"
	var got resendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n, _ := NewResendNotifier(ResendConfig{APIKey: testKey, From: "ops@kiff.dev", Endpoint: srv.URL})
	// The notifier API has no parameter to pass a reason — by design.
	_ = n.TenantSuspended(context.Background(), "c@example.com")
	if strings.Contains(got.Subject+got.Text+got.HTML, secretReason) {
		t.Fatal("internal reason leaked into customer email")
	}
}

func TestResend_4xxNoRetry_NoKeyLeak(t *testing.T) {
	t.Parallel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"invalid from"}`))
	}))
	defer srv.Close()
	n, _ := NewResendNotifier(ResendConfig{APIKey: testKey, From: "ops@kiff.dev", Endpoint: srv.URL, MaxAttempts: 3})
	err := n.TenantSuspended(context.Background(), "c@example.com")
	if err == nil {
		t.Fatal("want error on 4xx")
	}
	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Errorf("4xx must not retry; calls=%d", c)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatal("API key leaked in error")
	}
}

func TestResend_5xxRetriesThenFails(t *testing.T) {
	t.Parallel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	n, _ := NewResendNotifier(ResendConfig{APIKey: testKey, From: "ops@kiff.dev", Endpoint: srv.URL, MaxAttempts: 2})
	if err := n.TenantUnsuspended(context.Background(), "c@example.com"); err == nil {
		t.Fatal("want error after retries")
	}
	if c := atomic.LoadInt32(&calls); c != 2 {
		t.Errorf("want 2 attempts, got %d", c)
	}
}

func TestNoopNotifier(t *testing.T) {
	t.Parallel()
	n := NewNoopNotifier()
	if err := n.TenantSuspended(context.Background(), "c@example.com"); err != nil {
		t.Errorf("noop suspend: %v", err)
	}
	if err := n.TenantUnsuspended(context.Background(), ""); err != nil {
		t.Errorf("noop unsuspend: %v", err)
	}
}

func TestResend_SupportTicketUpdated_LinkOnlyNoBody(t *testing.T) {
	t.Parallel()
	var got resendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"email_1"}`))
	}))
	defer srv.Close()

	n, err := NewResendNotifier(ResendConfig{
		APIKey: testKey, From: "KIFF Operations <ops@kiff.dev>", Endpoint: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	const link = "https://app.kiff.dev/dashboard/support/tkt_123"
	if err := n.SupportTicketUpdated(context.Background(), "user@example.com", link); err != nil {
		t.Fatalf("SupportTicketUpdated: %v", err)
	}
	if got.Subject != subjectSupportUpdated {
		t.Errorf("subject: %q", got.Subject)
	}
	if len(got.To) != 1 || got.To[0] != "user@example.com" {
		t.Errorf("to: %v", got.To)
	}
	// The link is present in both text and HTML parts.
	if !strings.Contains(got.Text, link) {
		t.Errorf("text missing link: %q", got.Text)
	}
	if !strings.Contains(got.HTML, link) || !strings.Contains(got.HTML, "href=") {
		t.Errorf("html missing link anchor: %q", got.HTML)
	}
}

// The support close-the-loop email must carry no ticket content — only
// a link back to the in-app thread.
func TestResend_SupportTicketUpdated_CarriesNoTicketContent(t *testing.T) {
	t.Parallel()
	const secretBody = "here is the sensitive internal answer to your problem"
	var got resendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"email_1"}`))
	}))
	defer srv.Close()

	n, _ := NewResendNotifier(ResendConfig{APIKey: testKey, From: "ops@kiff.dev", Endpoint: srv.URL})
	// The API has no parameter to pass the reply body — by design.
	_ = n.SupportTicketUpdated(context.Background(), "u@example.com", "https://app.kiff.dev/dashboard/support/tkt_1")
	if strings.Contains(got.Subject+got.Text+got.HTML, secretBody) {
		t.Fatal("ticket content leaked into close-the-loop email")
	}
}
