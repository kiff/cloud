package tenant

import "testing"

func TestModeFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		meta map[string]any
		want Mode
	}{
		{"nil metadata defaults to enforce", nil, ModeEnforce},
		{"missing key defaults to enforce", map[string]any{"plan": "team"}, ModeEnforce},
		{"explicit enforce", map[string]any{MetadataKeyMode: "enforce"}, ModeEnforce},
		{"explicit observe", map[string]any{MetadataKeyMode: "observe"}, ModeObserve},
		{"legacy shadow spelling still resolves to observe", map[string]any{MetadataKeyMode: "shadow"}, ModeObserve},
		{"observe is case-insensitive", map[string]any{MetadataKeyMode: "OBSERVE"}, ModeObserve},
		{"legacy shadow is case-insensitive", map[string]any{MetadataKeyMode: "SHADOW"}, ModeObserve},
		{"observe trims whitespace", map[string]any{MetadataKeyMode: "  observe  "}, ModeObserve},
		{"unknown value fails closed to enforce", map[string]any{MetadataKeyMode: "audit-only"}, ModeEnforce},
		{"empty string fails closed to enforce", map[string]any{MetadataKeyMode: ""}, ModeEnforce},
		{"non-string value fails closed to enforce", map[string]any{MetadataKeyMode: 42}, ModeEnforce},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ModeFor(Tenant{Metadata: tt.meta})
			if got != tt.want {
				t.Fatalf("ModeFor(%v) = %q, want %q", tt.meta, got, tt.want)
			}
		})
	}
}
