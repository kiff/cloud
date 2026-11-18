package billing_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/billing"
)

func TestNewMeterEventClient_NilWhenUnconfigured(t *testing.T) {
	t.Parallel()
	if billing.NewMeterEventClient("", "governed_operations") != nil {
		t.Error("no secret should yield nil client")
	}
	if billing.NewMeterEventClient("sk_test_x", "") != nil {
		t.Error("no event name should yield nil client")
	}
	if billing.NewMeterEventClient("sk_test_x", "governed_operations") == nil {
		t.Error("configured client should be non-nil")
	}
}

func TestMeterEventClient_Send_Success(t *testing.T) {
	t.Parallel()
	var gotAuth, gotForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/billing/meter_events" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotForm = string(b)
		_, _ = io.WriteString(w, `{"identifier":"op-1"}`)
	}))
	defer srv.Close()

	c := billing.NewMeterEventClient("sk_test_secret", "governed_operations",
		billing.WithMeterAPIBase(srv.URL), billing.WithMeterHTTPClient(srv.Client()))
	at := time.Unix(1_700_000_000, 0)
	if err := c.Send(context.Background(), "cus_123", "op-1", 1, at); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotAuth != "Bearer sk_test_secret" {
		t.Errorf("auth = %q", gotAuth)
	}
	form, _ := url.ParseQuery(gotForm)
	if form.Get("event_name") != "governed_operations" {
		t.Errorf("event_name = %q", form.Get("event_name"))
	}
	if form.Get("identifier") != "op-1" {
		t.Errorf("identifier = %q (must be the op id for Stripe dedup)", form.Get("identifier"))
	}
	if form.Get("payload[stripe_customer_id]") != "cus_123" {
		t.Errorf("customer = %q", form.Get("payload[stripe_customer_id]"))
	}
	if form.Get("payload[value]") != "1" {
		t.Errorf("value = %q", form.Get("payload[value]"))
	}
}

func TestMeterEventClient_Send_StripeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"No such meter"}}`)
	}))
	defer srv.Close()
	c := billing.NewMeterEventClient("sk_test_secret", "governed_operations",
		billing.WithMeterAPIBase(srv.URL), billing.WithMeterHTTPClient(srv.Client()))
	err := c.Send(context.Background(), "cus_1", "op-1", 1, time.Now())
	if err == nil || !strings.Contains(err.Error(), "No such meter") {
		t.Fatalf("err = %v, want Stripe message surfaced", err)
	}
}

func TestMeterEventClient_Send_EmptyCustomer(t *testing.T) {
	t.Parallel()
	c := billing.NewMeterEventClient("sk_test_secret", "governed_operations")
	if err := c.Send(context.Background(), "", "op-1", 1, time.Now()); err == nil {
		t.Fatal("expected error for empty customer id")
	}
}
