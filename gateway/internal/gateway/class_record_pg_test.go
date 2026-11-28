package gateway_test

import (
	"context"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// The gateway's start writes each tool's class to the rows connected before
// it, and leaves rows that already say it.
func TestRecordClassesWritesEachToolsClass(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `DELETE FROM gateway.tools WHERE tenant_id = 'tenant-c';
		INSERT INTO gateway.tools (tenant_id, name, input_schema, upstream_url, upstream_tool, app, vendor_tool) VALUES
		('tenant-c', 'status', '{}', 'http://t/mcp', 'change_client_invoice_status', 'qonto', 'change_client_invoice_status'),
		('tenant-c', 'notes', '{}', 'http://t/mcp', 'update_note', '', '')`); err != nil {
		t.Fatal(err)
	}
	c := gateway.PostgresCatalog{Pool: r.pool}
	if _, err := c.RecordClasses(ctx); err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	rows, err := r.pool.Query(ctx, `SELECT name, class, COALESCE(class_opens::text, '') FROM gateway.tools WHERE tenant_id = 'tenant-c'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n, cl, op string
		if err := rows.Scan(&n, &cl, &op); err != nil {
			t.Fatal(err)
		}
		got[n] = [2]string{cl, op}
	}
	if got["status"] != [2]string{"irreversible", `[{"value": "finalize", "argument": "operation"}]`} || got["notes"] != [2]string{"write", ""} {
		t.Fatalf("recorded %v", got)
	}
	n, err := c.RecordClasses(ctx)
	if err != nil || n != 0 {
		t.Fatalf("again: wrote %d, %v", n, err)
	}
}
