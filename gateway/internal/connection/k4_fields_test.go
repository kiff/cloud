package connection_test

import (
	"net/http"
	"testing"
)

// A connector that already speaks K4 (planque since #495) sends app,
// destructive_hint and vendor_pins with each registration. This gateway
// accepts and ignores them, so a sign-in through that connector still connects.
func TestARegistrationWithK4FieldsStillConnects(t *testing.T) {
	r := newRig(t, dev)
	body := r.refund(toolSecret)
	body["app"] = "qonto"
	body["destructive_hint"] = true
	body["vendor_pins"] = []map[string]any{{"path": "status", "value": "draft"}}
	if status, out := r.do(t, "owner-a", http.MethodPost, "/v1/connections", body); status != http.StatusCreated {
		t.Fatalf("status %d: %s", status, out)
	}
}
