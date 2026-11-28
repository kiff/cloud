package connection_test

import (
	"net/http"
	"strings"
	"testing"
)

// A connection never names its amount, resource or idempotency argument
// after one of KIFF's own parameters (kiff_class, kiff_owner_ask…): the
// agent's value would stand in for KIFF's.
func TestAConnectionCantNameAnArgumentAfterKIFFsOwn(t *testing.T) {
	r := newRig(t, dev)
	for field, name := range map[string]string{"resource_argument": "kiff_class", "amount_argument": "KIFF_class_by", "idempotency_argument": "kiff_owner_ask"} {
		body := r.refund(toolSecret)
		body[field] = name
		if status, out := r.do(t, "owner-a", http.MethodPost, "/v1/connections", body); status != http.StatusBadRequest || !strings.Contains(out, "kiff_") {
			t.Errorf("%s %s: %d %s", field, name, status, out)
		}
	}
	if status, out := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("an ordinary connection: %d %s", status, out)
	}
}
