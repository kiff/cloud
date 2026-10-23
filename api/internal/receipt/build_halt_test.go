package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// A halt receipt signs one step whose DataHash covers exactly its Data, and
// says the agent, or the account, and what was done.
func TestBuildHaltReceipt(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	r, err := BuildHalt("t1", HaltStep{ID: "01J", AgentID: "refund-agent", Kind: "halt", At: at, By: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if r.TraceID != "halt-01J" || r.EntityID != "refund-agent" || r.EntityType != "agent" || r.Outcome != "halted" || len(r.CausalChain) != 1 {
		t.Fatalf("receipt %+v", r)
	}
	b, _ := json.Marshal(r.CausalChain[0].Data)
	if sum := sha256.Sum256(b); r.CausalChain[0].DataHash != hex.EncodeToString(sum[:]) {
		t.Fatal("the step's hash doesn't cover its data")
	}
	all, _ := BuildHalt("t1", HaltStep{ID: "01K", Kind: "resume", At: at, By: "alice"})
	if all.EntityType != "account" || all.EntityID != "t1" || all.Outcome != "resumed" {
		t.Fatalf("the account's resume: %+v", all)
	}
	if _, err := BuildHalt("t1", HaltStep{ID: "x", Kind: "pause"}); err == nil {
		t.Fatal("built a receipt for a kind that isn't a halt or a resume")
	}
}
