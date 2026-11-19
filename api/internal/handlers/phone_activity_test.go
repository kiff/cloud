package handlers

import (
	"net/http"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
)

// A phone reads the account's activity (kiff/tafs#102): what its tafs did,
// for the app's Activity and Record. A read, so no signature.
func TestPhoneReadsTheActivity(t *testing.T) {
	reached := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me/activity", func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	})
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	p := newPhoneRig(t)
	if rr := serve(policed, p.on(owner()), http.MethodGet, "/v1/me/activity", "", nil); rr.Code != http.StatusOK || reached != 1 {
		t.Fatalf("a phone's read: %d %s (reached %d)", rr.Code, rr.Body.String(), reached)
	}
}

// A held call's reason says its amounts in their unit (kiff/tafs#102):
// "150.00 EUR is above the 100.00 EUR limit for one call", not
// "sum(amount) of 15000 is above the per-action limit of 10000".
func TestAHoldsReasonIsInTheAmountsUnit(t *testing.T) {
	per := map[string]any{"outcome": string(mandate.OutcomePerActionExceeded), "draw": int64(15000), "limit": int64(10000), "used": int64(0)}
	if got := reasonInUnit(per, "EUR", 2, langEN); got != "150.00 EUR is above the 100.00 EUR limit for one call" {
		t.Errorf("per call: %q", got)
	}
	total := map[string]any{"outcome": string(mandate.OutcomeLimitReached), "draw": int64(15000), "limit": int64(100000), "used": int64(90000)}
	if got := reasonInUnit(total, "EUR", 2, langEN); got != "150.00 EUR would go over the 1000.00 EUR limit: 900.00 EUR is used" {
		t.Errorf("total: %q", got)
	}
	if got := reasonInUnit(map[string]any{"outcome": "resource_not_on_card"}, "EUR", 2, langEN); got != "" {
		t.Errorf("another breach keeps KIFF's reason: %q", got)
	}
	for minor, want := range map[int64]string{5: "0.05 EUR", 100: "1.00 EUR", 123456: "1234.56 EUR", -250: "-2.50 EUR"} {
		if got := inUnit(minor, "EUR", 2, langEN); got != want {
			t.Errorf("inUnit(%d) = %q, want %q", minor, got, want)
		}
	}
	if got := inUnit(7, "JPY", 0, langEN); got != "7 JPY" {
		t.Errorf("scale 0: %q", got)
	}
}
