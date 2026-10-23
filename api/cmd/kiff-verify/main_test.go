package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestReadBatchFromStdin parses a small JSON batch through the same
// stdin/file resolver the CLI uses. The full chain integration is
// covered by internal/anchor's e2e test; this just guards the CLI's
// JSON contract.
func TestReadBatchFromStdin(t *testing.T) {
	t.Parallel()

	const sample = `{
		"BatchID": 7,
		"StreamID": "0xa172b4a02c8b85e30daff5d3eb12d51d20a8624bc16b56cd47fb35bd24cd87b6",
		"Records": [
			{
				"RecordID": "rec-1",
				"TraceID":  "tr-1",
				"EntityID": "order-1",
				"Kind":     "action_executed",
				"At":       "2026-05-22T12:00:00Z",
				"Payload":  "cGF5bG9hZA=="
			}
		]
	}`

	stdin := strings.NewReader(sample)
	batch, err := readBatch("-", stdin)
	if err != nil {
		t.Fatalf("readBatch: %v", err)
	}
	if batch.BatchID != 7 {
		t.Fatalf("BatchID: got %d, want 7", batch.BatchID)
	}
	if len(batch.Records) != 1 || batch.Records[0].RecordID != "rec-1" {
		t.Fatalf("records: got %+v", batch.Records)
	}
}

func TestRunRequiresFlags(t *testing.T) {
	t.Parallel()

	var out, errBuf bytes.Buffer
	err := run([]string{}, strings.NewReader("{}"), &out, &errBuf)
	if err == nil {
		t.Fatal("run() returned no error when required flags are missing")
	}
}
