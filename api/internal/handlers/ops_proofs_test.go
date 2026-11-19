package handlers

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/operatoralerts"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proofs"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

const proofTenant = "tnt_proof_test"

var proofOIDC = proofs.OIDCConfig{
	Audience:     "kiff-proofs",
	Repository:   "kiff/agents",
	RepositoryID: "700100",
	OwnerID:      "2176846",
	Ref:          "refs/heads/main",
	Workflow:     "kiff/agents/.github/workflows/proofs.yml@refs/heads/main",
}

// proofRig is the ingest behind a fake GitHub token issuer.
type proofRig struct {
	mux    *http.ServeMux
	store  *proofs.MemoryStore
	alerts *operatoralerts.MemoryStore
	key    *rsa.PrivateKey
	issuer string
	now    time.Time
}

func newProofRig(t *testing.T, versions proofs.VersionChecker) *proofRig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(jwks.Close)
	rig := &proofRig{store: proofs.NewMemoryStore(), alerts: operatoralerts.NewMemoryStore(), key: key,
		issuer: jwks.URL, now: time.Now().UTC().Truncate(time.Second)}
	clock := func() time.Time { return rig.now }
	verifier := proofs.NewOIDCVerifier(proofOIDC).WithJWKS(jwks.URL, jwks.URL).WithClock(clock)
	h := NewProofIngestHandler(verifier, rig.store, []string{proofTenant}, versions, rig.alerts, nil)
	if h == nil {
		t.Fatal("NewProofIngestHandler returned nil")
	}
	h.WithClock(clock)
	rig.mux = http.NewServeMux()
	h.Register(rig.mux)
	return rig
}

func (rig *proofRig) token(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	claims := map[string]any{
		"iss": rig.issuer, "aud": proofOIDC.Audience, "repository": proofOIDC.Repository,
		"repository_id": proofOIDC.RepositoryID, "repository_owner_id": proofOIDC.OwnerID,
		"ref": proofOIDC.Ref, "workflow_ref": proofOIDC.Workflow, "run_id": "77", "run_attempt": "3",
		"sha": strings.Repeat("a", 40),
		"exp": rig.now.Add(5 * time.Minute).Unix(), "iat": rig.now.Unix(),
	}
	if edit != nil {
		edit(claims)
	}
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rig.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (rig *proofRig) run(id, app, outcome string) map[string]any {
	started := rig.now.Add(-3 * time.Minute)
	checks := []map[string]any{{"name": "connects", "ok": true}, {"name": "card_limit_held", "ok": true}}
	limits := true
	switch outcome {
	case proofs.OutcomeFail:
		checks[0]["ok"], checks[0]["why"] = false, "the sign-in was refused"
	case proofs.OutcomeError:
		checks, limits = nil, false
	}
	return map[string]any{
		"schema": 1, "run_id": id, "app": app, "job": "refunds-in-time", "tenant": proofTenant,
		"trigger": "schedule", "thread": "thr_1",
		"started_at": started.Format(time.RFC3339), "ended_at": rig.now.Add(-time.Minute).Format(time.RFC3339),
		"outcome": outcome, "checks": checks, "limits_held": limits, "p50_ms": 200, "p95_ms": 800,
		"runner_version": strings.Repeat("a", 40),
		"events_key":     "proofs/" + id + "/events.jsonl",
		"entry":          app + "@3",
		"rules":          map[string]string{"coach_words": "sha256:ab12", "judge_limits": "sha256:cd34"},
	}
}

func (rig *proofRig) post(t *testing.T, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	switch b := body.(type) {
	case string:
		raw = []byte(b)
	default:
		raw, _ = json.Marshal(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/ops/proofs", strings.NewReader(string(raw)))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	rig.mux.ServeHTTP(w, req)
	return w
}

func TestTheIngestKeepsARunFromTheRunner(t *testing.T) {
	rig := newProofRig(t, proofs.AllowList{strings.Repeat("a", 40): true})
	w := rig.post(t, rig.token(t, nil), rig.run("run_00000001", "stripe", proofs.OutcomePass))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got struct {
		RunID   string `json:"run_id"`
		Counted bool   `json:"counted"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.RunID != "run_00000001" || !got.Counted {
		t.Fatalf("answer %+v", got)
	}
	kept, err := rig.store.Run(context.Background(), "run_00000001")
	if err != nil || !kept.Counted || kept.ReceivedAt.IsZero() || kept.GitHubRunID != "77" || kept.GitHubRunAttempt != "3" {
		t.Fatalf("kept %+v, %v", kept, err)
	}
}

func TestTheRunnerVersionIsTheTokensSha(t *testing.T) {
	rig := newProofRig(t, proofs.AllowList{strings.Repeat("a", 40): true, strings.Repeat("b", 40): true})
	other := rig.run("run_sha00001", "stripe", proofs.OutcomePass)
	other["runner_version"] = strings.Repeat("b", 40) // on main, but not the commit that ran
	if w := rig.post(t, rig.token(t, nil), other); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "runner_version_not_token") {
		t.Fatalf("a body naming another commit: %d %s", w.Code, w.Body)
	}
	noSha := rig.token(t, func(c map[string]any) { delete(c, "sha") })
	if w := rig.post(t, noSha, rig.run("run_sha00002", "stripe", proofs.OutcomePass)); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a token without sha: %d %s", w.Code, w.Body)
	}
	// The GitHub run is the token's, whatever the body says.
	forged := rig.run("run_sha00003", "stripe", proofs.OutcomePass)
	forged["github_run_id"], forged["github_run_attempt"] = "1", "9"
	if w := rig.post(t, rig.token(t, nil), forged); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if kept, _ := rig.store.Run(context.Background(), "run_sha00003"); kept.GitHubRunID != "77" || kept.GitHubRunAttempt != "3" {
		t.Fatalf("the body set the GitHub run: %+v", kept)
	}
}

func TestTheIngestAnswers404ToAnyBadToken(t *testing.T) {
	rig := newProofRig(t, nil)
	body := rig.run("run_00000002", "stripe", proofs.OutcomePass)
	cases := map[string]string{
		"no token":       "",
		"garbage":        "abc.def.ghi",
		"wrong repo":     rig.token(t, func(c map[string]any) { c["repository"] = "someone/agents" }),
		"wrong ref":      rig.token(t, func(c map[string]any) { c["ref"] = "refs/heads/dev" }),
		"wrong workflow": rig.token(t, func(c map[string]any) { c["workflow_ref"] = "kiff/agents/.github/workflows/x.yml@refs/heads/main" }),
		"wrong audience": rig.token(t, func(c map[string]any) { c["aud"] = "other" }),
		"wrong issuer":   rig.token(t, func(c map[string]any) { c["iss"] = "https://example.com" }),
		"expired":        rig.token(t, func(c map[string]any) { c["exp"] = rig.now.Add(-time.Hour).Unix() }),
		"wrong repo id":  rig.token(t, func(c map[string]any) { c["repository_id"] = "1" }),
		"wrong owner id": rig.token(t, func(c map[string]any) { c["repository_owner_id"] = "1" }),
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if w := rig.post(t, tok, body); w.Code != http.StatusNotFound {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
		})
	}
	if runs, _ := rig.store.RunsSince(context.Background(), time.Time{}); len(runs) != 0 {
		t.Fatalf("a refused token kept %d runs", len(runs))
	}
}

func TestTheIngestRefusesABadRun(t *testing.T) {
	rig := newProofRig(t, nil)
	tok := rig.token(t, nil)

	customer := rig.run("run_00000003", "stripe", proofs.OutcomePass)
	customer["tenant"] = "tnt_real_customer"
	if w := rig.post(t, tok, customer); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "tenant_not_test") {
		t.Fatalf("customer tenant: %d %s", w.Code, w.Body)
	}

	secret := rig.run("run_00000004", "stripe", proofs.OutcomeFail)
	secret["checks"].([]map[string]any)[0]["why"] = "refused key sk_" + "live_abcdefghijklmnop1234"
	if w := rig.post(t, tok, secret); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "secret_in_text") {
		t.Fatalf("secret: %d %s", w.Code, w.Body)
	}
	if strings.Contains(rig.post(t, tok, secret).Body.String(), "sk_"+"live") {
		t.Fatal("the refusal echoed the secret")
	}

	liar := rig.run("run_00000005", "stripe", proofs.OutcomePass)
	liar["checks"].([]map[string]any)[1]["ok"] = false
	if w := rig.post(t, tok, liar); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("pass with a failed check: %d %s", w.Code, w.Body)
	}

	extra := rig.run("run_00000006", "stripe", proofs.OutcomePass)
	extra["prompt"] = "the whole conversation"
	if w := rig.post(t, tok, extra); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", w.Code, w.Body)
	}

	if w := rig.post(t, tok, `{"schema":1,`+strings.Repeat(" ", 70<<10)+`}`); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %d", w.Code)
	}
}

func TestTheIngestIsAppendOnly(t *testing.T) {
	rig := newProofRig(t, nil)
	tok := rig.token(t, nil)
	body := rig.run("run_00000007", "stripe", proofs.OutcomeFail)
	if w := rig.post(t, tok, body); w.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	again := rig.run("run_00000007", "stripe", proofs.OutcomePass)
	if w := rig.post(t, tok, again); w.Code != http.StatusConflict {
		t.Fatalf("second: %d %s", w.Code, w.Body)
	}
	kept, _ := rig.store.Run(context.Background(), "run_00000007")
	if kept.Outcome != proofs.OutcomeFail {
		t.Fatal("a resend replaced the run")
	}
}

type failingVersions struct{}

func (failingVersions) OnMain(context.Context, string) (bool, error) {
	return false, errors.New("github down")
}

func TestAnUnconfirmedRunnerIsKeptUncounted(t *testing.T) {
	for name, versions := range map[string]proofs.VersionChecker{
		"not on main": proofs.AllowList{}, "github down": failingVersions{}, "no checker": nil,
	} {
		t.Run(name, func(t *testing.T) {
			rig := newProofRig(t, versions)
			if w := rig.post(t, rig.token(t, nil), rig.run("run_00000008", "stripe", proofs.OutcomePass)); w.Code != http.StatusCreated {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			kept, _ := rig.store.Run(context.Background(), "run_00000008")
			if kept.Counted {
				t.Fatal("counted")
			}
		})
	}
	// The runner can't claim it either.
	rig := newProofRig(t, proofs.AllowList{})
	body := rig.run("run_00000009", "stripe", proofs.OutcomePass)
	body["counted"] = true
	if w := rig.post(t, rig.token(t, nil), body); w.Code != http.StatusCreated || strings.Contains(w.Body.String(), `"counted":true`) {
		t.Fatalf("a runner-set counted: %d %s", w.Code, w.Body)
	}
	if kept, _ := rig.store.Run(context.Background(), "run_00000009"); kept.Counted {
		t.Fatal("the runner's counted was kept")
	}
}

func TestThreeErrorRunsRaiseAnOperatorAlert(t *testing.T) {
	rig := newProofRig(t, nil)
	for i, id := range []string{"run_err00001", "run_err00002", "run_err00003"} {
		rig.now = rig.now.Add(time.Hour)
		if w := rig.post(t, rig.token(t, nil), rig.run(id, "stripe", proofs.OutcomeError)); w.Code != http.StatusCreated {
			t.Fatalf("%d: %d %s", i, w.Code, w.Body)
		}
		if list, _ := rig.alerts.List(context.Background(), true, 10); i < 2 && len(list) != 0 {
			t.Fatalf("alerted after %d error runs", i+1)
		}
	}
	list, _ := rig.alerts.List(context.Background(), true, 10)
	if len(list) != 1 || list[0].Kind != proofs.AlertAppErrors+":stripe" || list[0].TenantID != proofTenant {
		t.Fatalf("alerts %+v", list)
	}
}

// opsProofsFixture is the ops console with the proofs mounted.
type opsProofsRig struct {
	mux   *http.ServeMux
	store *proofs.MemoryStore
	audit *operatoraudit.MemoryStore
	now   time.Time
}

type stubEvents map[string][]json.RawMessage

func (s stubEvents) Events(_ context.Context, key string) ([]json.RawMessage, error) {
	lines, ok := s[key]
	if !ok {
		return nil, proofs.ErrNoEvents
	}
	return lines, nil
}

func newOpsProofsRig(t *testing.T, events proofs.EventSource, releaseAnyway bool, extra ...OpsOption) *opsProofsRig {
	t.Helper()
	rig := &opsProofsRig{store: proofs.NewMemoryStore(), audit: operatoraudit.NewMemoryStore(),
		now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	opts := append([]OpsOption{WithOpsControlPlane(rig.audit, nil), WithOpsProofs(rig.store, events, releaseAnyway),
		func(h *OpsHandler) { h.now = func() time.Time { return rig.now } }}, extra...)
	h := NewOpsHandler([]string{opsSubject}, tenant.NewMemoryStore(), usage.NewMemoryStore(), receipt.NewMemoryStore(),
		proposals.NewMemoryReader(), posture.NewMemoryReader(), alert.NewMemoryDeliveryStore(), opts...)
	rig.mux = http.NewServeMux()
	h.Register(rig.mux)
	return rig
}

func (rig *opsProofsRig) do(t *testing.T, subject, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if subject != "" {
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: subject, Method: auth.MethodSession}))
	}
	w := httptest.NewRecorder()
	rig.mux.ServeHTTP(w, req)
	return w
}

func (rig *opsProofsRig) seed(t *testing.T, app string, outcomes ...string) {
	t.Helper()
	for i, o := range outcomes {
		end := rig.now.Add(time.Duration(i-len(outcomes)) * time.Hour)
		r := proofs.Run{Schema: 1, RunID: app + "_run_" + string(rune('a'+i)), App: app, Entry: app + "@1", Tenant: proofTenant,
			Trigger: proofs.TriggerSchedule, StartedAt: end.Add(-time.Minute), EndedAt: end, Outcome: o, Counted: true,
			EventsKey: "proofs/" + app + "/x.jsonl"}
		if o == proofs.OutcomeFail {
			r.Checks = []proofs.Check{{Name: proofs.CheckConnects, OK: false}, {Name: proofs.CheckCardLimitHeld, OK: true}}
		}
		if err := rig.store.Append(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheProofReadsAreForOperatorsOnly(t *testing.T) {
	rig := newOpsProofsRig(t, nil, false)
	rig.seed(t, "stripe", proofs.OutcomePass)
	paths := []struct{ method, path string }{
		{http.MethodGet, "/v1/ops/proofs"},
		{http.MethodGet, "/v1/ops/proofs/stripe_run_a"},
		{http.MethodPost, "/v1/ops/proofs/apps/stripe/hold"},
		{http.MethodPost, "/v1/ops/proofs/apps/stripe/release"},
		{http.MethodPost, "/v1/ops/proofs/apps/stripe/clear"},
	}
	for _, p := range paths {
		for _, who := range []string{"", tenantSubject} {
			w := rig.do(t, who, p.method, p.path, `{"reason":"x","until":"2026-11-18T00:00:00Z"}`)
			if w.Code != http.StatusNotFound {
				t.Errorf("%s %s as %q: %d", p.method, p.path, who, w.Code)
			}
		}
	}
	if overrides, _ := rig.store.Overrides(context.Background()); len(overrides) != 0 {
		t.Fatal("a non-operator saved an override")
	}
}

func TestTheMonitoringRows(t *testing.T) {
	rig := newOpsProofsRig(t, nil, false)
	rig.seed(t, "stripe", proofs.OutcomePass, proofs.OutcomePass)
	rig.seed(t, "gmail", proofs.OutcomeFail, proofs.OutcomeFail, proofs.OutcomeFail)
	w := rig.do(t, opsSubject, http.MethodGet, "/v1/ops/proofs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got struct {
		Apps []proofs.Row `json:"apps"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Apps) != 2 || got.Apps[0].App != "gmail" || got.Apps[0].State.State != proofs.StateRed || !got.Apps[0].State.Held ||
		got.Apps[1].State.State != proofs.StateGreen {
		t.Fatalf("rows %+v", got.Apps)
	}
}

func TestTheReplay(t *testing.T) {
	events := stubEvents{"proofs/stripe/x.jsonl": {
		json.RawMessage(`{"seq":1,"kind":"user_said","text":"Refund order 1042"}`),
		json.RawMessage(`{"seq":2,"kind":"tool_call","args":{"authorization":"Bearer abcdefghijklmnopqrstuvwxyz"}}`),
	}}
	rig := newOpsProofsRig(t, events, false)
	rig.seed(t, "stripe", proofs.OutcomePass)
	rig.seed(t, "gmail", proofs.OutcomeFail)

	w := rig.do(t, opsSubject, http.MethodGet, "/v1/ops/proofs/stripe_run_a", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got struct {
		Run      proofs.Run        `json:"run"`
		Events   []json.RawMessage `json:"events"`
		Redacted int               `json:"redacted"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Run.RunID != "stripe_run_a" || len(got.Events) != 2 || got.Redacted != 1 {
		t.Fatalf("replay %+v", got)
	}
	if strings.Contains(w.Body.String(), "Bearer abc") {
		t.Fatal("the replay showed a secret")
	}

	// No events for this key: the summary alone.
	w = rig.do(t, opsSubject, http.MethodGet, "/v1/ops/proofs/gmail_run_a", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"events_unavailable":true`) {
		t.Fatalf("no events: %d %s", w.Code, w.Body)
	}
	if w := rig.do(t, opsSubject, http.MethodGet, "/v1/ops/proofs/nope_nope_nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing run: %d", w.Code)
	}
}

func TestOverrides(t *testing.T) {
	rig := newOpsProofsRig(t, nil, false)
	rig.seed(t, "gmail", proofs.OutcomeFail, proofs.OutcomeFail, proofs.OutcomeFail)
	tomorrow := rig.now.Add(24 * time.Hour).Format(time.RFC3339)
	post := func(action, body string) *httptest.ResponseRecorder {
		return rig.do(t, opsSubject, http.MethodPost, "/v1/ops/proofs/apps/gmail/"+action, body)
	}
	if w := post("hold", `{"until":"`+tomorrow+`"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reason_required") {
		t.Fatalf("no reason: %d %s", w.Code, w.Body)
	}
	if w := post("hold", `{"reason":"vendor outage"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "until_required") {
		t.Fatalf("no until: %d %s", w.Code, w.Body)
	}
	far := rig.now.Add(40 * 24 * time.Hour).Format(time.RFC3339)
	if w := post("hold", `{"reason":"x","until":"`+far+`"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "until_too_far") {
		t.Fatalf("far: %d %s", w.Code, w.Body)
	}
	if w := post("hold", `{"reason":"vendor outage","until":"`+tomorrow+`"}`); w.Code != http.StatusOK {
		t.Fatalf("hold: %d %s", w.Code, w.Body)
	}
	// Release anyway is off without the flag, even with an owner's yes.
	if w := post("release", `{"reason":"x","until":"`+tomorrow+`","owner_approval":"owner said yes"}`); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), "release_needs_owner") {
		t.Fatalf("release without flag: %d %s", w.Code, w.Body)
	}
	entries, _ := rig.audit.List(context.Background(), "", 10)
	if len(entries) != 1 || entries[0].Action != operatoraudit.ActionProofHold || entries[0].OperatorSubject != opsSubject {
		t.Fatalf("audit %+v", entries)
	}
	overrides, _ := rig.store.Overrides(context.Background())
	if len(overrides) != 1 || overrides[0].Operator != opsSubject {
		t.Fatalf("overrides %+v", overrides)
	}
}

func TestReleaseAnywayNeedsTheOwnersYes(t *testing.T) {
	rig := newOpsProofsRig(t, nil, true)
	rig.seed(t, "gmail", proofs.OutcomeFail, proofs.OutcomeFail, proofs.OutcomeFail)
	tomorrow := rig.now.Add(24 * time.Hour).Format(time.RFC3339)
	post := func(body string) *httptest.ResponseRecorder {
		return rig.do(t, opsSubject, http.MethodPost, "/v1/ops/proofs/apps/gmail/release", body)
	}
	if w := post(`{"reason":"x","until":"` + tomorrow + `"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "owner_approval_required") {
		t.Fatalf("no owner yes: %d %s", w.Code, w.Body)
	}
	week := rig.now.Add(8 * 24 * time.Hour).Format(time.RFC3339)
	if w := post(`{"reason":"x","until":"` + week + `","owner_approval":"y"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("over 7 days: %d %s", w.Code, w.Body)
	}
	if w := post(`{"reason":"false alarm","until":"` + tomorrow + `","owner_approval":"owner's message 2026-11-17 11:58"}`); w.Code != http.StatusOK {
		t.Fatalf("release: %d %s", w.Code, w.Body)
	}
	w := rig.do(t, opsSubject, http.MethodGet, "/v1/ops/proofs", "")
	var got struct {
		Apps []proofs.Row `json:"apps"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Apps) != 1 || got.Apps[0].State.Held || got.Apps[0].State.Override != proofs.OverrideRelease {
		t.Fatalf("after release %+v", got.Apps)
	}
	entries, _ := rig.audit.List(context.Background(), "", 10)
	if len(entries) != 1 || entries[0].Metadata["owner_approval"] == nil {
		t.Fatalf("audit %+v", entries)
	}
	// The release ends after its until.
	rig.now = rig.now.Add(25 * time.Hour)
	w = rig.do(t, opsSubject, http.MethodGet, "/v1/ops/proofs", "")
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.Apps[0].State.Held {
		t.Fatal("an expired release still released")
	}
}
