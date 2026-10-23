package domain_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
)

// TestCafeYAMLValidatesAgainstDefaultRegistry asserts the bundled
// scripts/cafe.kiff.yaml parses cleanly against
// executors.DefaultRegistry() and produces a buildable
// kiffdomain.Definition. This is the contract test for slice A of
// kiff/kiff#4 — the cafe domain ships as YAML the cloud accepts.
//
// If a cafe contract changes (parameters, executor key, action name)
// either the YAML or the executor must move first; this test fails
// the build until they agree.
func TestCafeYAMLValidatesAgainstDefaultRegistry(t *testing.T) {
	t.Parallel()

	// The YAML lives next to the bootstrap script at the repo root;
	// the test path walks up from internal/domain/.
	yamlPath := filepath.Join("..", "..", "scripts", "cafe.kiff.yaml")
	bytes, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read %s: %v", yamlPath, err)
	}

	registry := executors.DefaultRegistry()
	parsed, err := domain.ParseAndBuild(bytes, registry)
	if err != nil {
		t.Fatalf("ParseAndBuild: %v", err)
	}

	def := parsed.Definition
	if got, want := def.Name, "ai-cafe-ops"; got != want {
		t.Errorf("Name: got %q, want %q", got, want)
	}
	if len(def.EntityTypes) != 1 || def.EntityTypes[0] != "Shift" {
		t.Errorf("EntityTypes: got %v, want [Shift]", def.EntityTypes)
	}

	// All six action names must round-trip from YAML through the
	// builder. Naming drift between the YAML and the framework's
	// constants is the most common drift source; assert each.
	wantActions := []string{
		"START_SHIFT",
		"AUTO_ORDER_INVENTORY",
		"ORDER_INVENTORY",
		"REQUEST_SPECIALTY",
		"SEND_STAFF_MESSAGE",
		"ESCALATE_SUPPLIER",
	}
	for _, name := range wantActions {
		if _, ok := def.Actions.Get(name); !ok {
			t.Errorf("missing action %q in parsed definition", name)
		}
	}

	// PermissionPolicy carries the three role names we ship.
	if parsed.Policy == nil {
		t.Fatal("nil PermissionPolicy")
	}
}
