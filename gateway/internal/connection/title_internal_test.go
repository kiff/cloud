package connection

import (
	"testing"

	"github.com/kiff/cloud/gateway/internal/mcp"
)

// A vendor that gives no title: its tool's name in words, not KIFF's name.
func TestAToolWithoutAVendorTitleReadsItsName(t *testing.T) {
	got, msg := toolTitle("", "create_refund", &mcp.RemoteTool{Name: "create_refund"})
	if msg != "" || got != "Create refund" {
		t.Fatalf("toolTitle = %q %q", got, msg)
	}
	got, _ = toolTitle("", "create_refund", &mcp.RemoteTool{Annotations: &mcp.ToolAnnotations{Title: "Create a refund"}})
	if got != "Create a refund" {
		t.Fatalf("the annotations' title: %q", got)
	}
}
