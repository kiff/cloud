package guard

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreTouchList(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	first := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	row, err := store.Touch(ctx, "tenant-a", TouchInput{
		Project:     "finance",
		Environment: "prod",
		AgentID:     "ap-agent",
		Workflow:    "invoice-payments",
		Adapter:     "OpenClaw",
		SDKVersion:  "0.1.0",
		Mode:        "observe",
		SeenAt:      first,
	})
	if err != nil {
		t.Fatalf("Touch first: %v", err)
	}
	if row.FirstSeenAt != first || row.LastSeenAt != first || row.SeenCount != 1 {
		t.Fatalf("first heartbeat mismatch: %+v", row)
	}
	if row.Adapter != "openclaw" {
		t.Fatalf("adapter should normalize lowercase, got %q", row.Adapter)
	}

	row, err = store.Touch(ctx, "tenant-a", TouchInput{
		Project:     "finance",
		Environment: "prod",
		AgentID:     "ap-agent",
		Workflow:    "invoice-payments",
		Adapter:     "openclaw",
		SDKVersion:  "0.1.1",
		Mode:        "enforce",
		SeenAt:      second,
	})
	if err != nil {
		t.Fatalf("Touch second: %v", err)
	}
	if row.FirstSeenAt != first || row.LastSeenAt != second || row.SeenCount != 2 {
		t.Fatalf("second heartbeat mismatch: %+v", row)
	}
	if row.Mode != ModeEnforce || row.SDKVersion != "0.1.1" {
		t.Fatalf("metadata should update on heartbeat: %+v", row)
	}

	rows, err := store.List(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].AgentID != "ap-agent" {
		t.Fatalf("tenant-a rows mismatch: %+v", rows)
	}
	rows, err = store.List(ctx, "tenant-b")
	if err != nil {
		t.Fatalf("List tenant-b: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("tenant-b should not see tenant-a rows: %+v", rows)
	}
}

func TestTouchInputValidation(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		in   TouchInput
	}{
		{name: "missing agent", in: TouchInput{Adapter: "openclaw"}},
		{name: "missing adapter", in: TouchInput{AgentID: "agent"}},
		{name: "bad mode", in: TouchInput{AgentID: "agent", Adapter: "openclaw", Mode: "maybe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.in.normalized(now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
