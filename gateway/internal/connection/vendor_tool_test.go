package connection_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A relay's connection keeps the tool's own name at its vendor, as one line,
// so people read the vendor's tool, never a guess at the relay's name for
// it (the vendor tool's own name).
func TestAConnectionKeepsTheVendorsToolName(t *testing.T) {
	r := newRig(t, dev)
	body := r.refund(toolSecret)
	body["vendor_tool"] = "  create_refund\n"
	if status, out := r.do(t, "owner-a", http.MethodPost, "/v1/connections", body); status != http.StatusCreated || !strings.Contains(out, `"vendor_tool":"create_refund"`) {
		t.Fatalf("connect: %d %s", status, out)
	}
	conn, err := r.store.Get(context.Background(), "tenant-a", body["name"].(string))
	if err != nil || conn.VendorTool != "create_refund" {
		t.Fatalf("stored %+v %v", conn, err)
	}
}
