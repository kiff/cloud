package candidate

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kiff/cloud/api/internal/guard"
)

// Fixture is a connected-runtime sample: the connection heartbeat plus the
// observed tool catalog, exactly as kiff-cloud would hold them after a
// guard runtime connects and reports. Used by the spike's test and CLI so
// there is one source of truth for the runtime-derived input.
type Fixture struct {
	Connection  guard.Connection  `json:"connection"`
	Observation guard.Observation `json:"observation"`
}

// LoadFixture reads a connected-runtime fixture JSON from disk.
func LoadFixture(path string) (Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, fmt.Errorf("read fixture %s: %w", path, err)
	}
	var f Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return Fixture{}, fmt.Errorf("decode fixture %s: %w", path, err)
	}
	return f, nil
}
