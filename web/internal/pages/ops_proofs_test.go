package pages

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestOpsProofRun_EscapesEventsAndShowsAnErrorRunUnjudged(t *testing.T) {
	t.Parallel()
	v := apiclient.OpsProofReplay{
		Run: apiclient.OpsProofRun{RunID: "run_1", App: "gmail", Job: "j", Outcome: "error"},
		Events: []json.RawMessage{
			json.RawMessage(`{"seq":1,"kind":"<script>x</script>","text":"<img src=x onerror=alert(1)>"}`),
		},
	}
	var b strings.Builder
	if err := OpsProofRun(v, opsTestChrome()).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "<script>x") || strings.Contains(out, "<img src=x") {
		t.Fatalf("an event's text reached the page unescaped:\n%s", out)
	}
	for _, want := range []string{"Not judged", "dash-shell", "not counted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestOpsProofs_EmptyAndNoReleaseWhenOff(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	_ = OpsProofs(apiclient.OpsProofRows{}, false, opsTestChrome()).Render(context.Background(), &b)
	if !strings.Contains(b.String(), "No proof runs yet") {
		t.Fatal("empty page")
	}
	b.Reset()
	held := apiclient.OpsProofRows{Apps: []apiclient.OpsProofRow{{App: "gmail",
		State: apiclient.OpsProofState{State: "red", Held: true}}}}
	_ = OpsProofs(held, false, opsTestChrome()).Render(context.Background(), &b)
	if strings.Contains(b.String(), "/release") || !strings.Contains(b.String(), "/ops/proofs/apps/gmail/hold") {
		t.Fatal("release offered while release anyway is off")
	}
}
