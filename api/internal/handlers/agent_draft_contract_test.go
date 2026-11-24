package handlers

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateDraftContract = flag.Bool("update-draft-contract", false, "rewrite testdata/agent-draft-from-hold.json")

// TestAgentDraftFromHoldContract pins what GET /v1/me/agent-drafts/{id}
// answers for an "Allow this from now on" draft (kiff/tafs's
// docs/allow-from-now-on.md): the apps read it before they sign, and
// refuse a Card key they don't compare, so a key added here must reach
// them as a changed file, never first on a phone. The draft's own ids
// and the held call's are set to fixed words; the clock is the rig's.
//
//	Regenerate intentionally with: go test ./internal/handlers/ \
//	  -run TestAgentDraftFromHoldContract -update-draft-contract
func TestAgentDraftFromHoldContract(t *testing.T) {
	r := newDraftRig(t)
	r.startRae() // €150 a refund, €2,000 a day, €100 used
	d := r.authorize("rae", "p-big", 32000)
	if d.ExceptionID == "" {
		t.Fatalf("not held: %+v", d)
	}
	rr := do(t, r.mux, admin(), http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/allow-draft", map[string]any{})
	var offer map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &offer); err != nil || rr.Code != http.StatusCreated {
		t.Fatalf("allow-draft: %d %s", rr.Code, rr.Body)
	}
	draftID, _ := offer["draft"].(string)
	rr = do(t, r.mux, admin(), http.MethodGet, "/v1/me/agent-drafts/"+draftID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rr.Code, rr.Body)
	}
	got := strings.NewReplacer(draftID, "drf_fixed", d.ExceptionID, "exc_fixed").Replace(rr.Body.String())
	var answer any
	if err := json.Unmarshal([]byte(got), &answer); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"about":  "GET /v1/me/agent-drafts/{id} for an \"Allow this from now on\" draft: Rae's €150-a-refund Card, held at €320, revised to €320",
		"answer": answer,
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "agent-draft-from-hold.json")
	if *updateDraftContract {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (write it with -update-draft-contract)", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Fatalf("the draft's answer changed: the apps read %s. If the change is meant, write it again with -update-draft-contract and copy it to kiff/tafs's contracts/, where the apps' tests read it.\ngot:\n%s", path, buf.String())
	}
}
