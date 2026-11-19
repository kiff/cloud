package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// KIFF renders a profile's words, and the gateway applies a profile only
// when the owner's signed approval covers that profile and those words
// (the connection-profiles design, Appendix A).
func TestProfileWordsAndApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	members := membership.NewMemoryStore()
	svc := membership.NewService(members)
	if _, err := svc.EnsureFounder(ctx, "t1", "alice"); err != nil {
		t.Fatal(err)
	}
	_, tok, err := svc.CreateInvite(ctx, "t1", membership.Actor{Subject: "alice"}, membership.InviteRequest{Email: "carol@x.io", Role: membership.RoleApprover})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptInvite(ctx, tok, membership.Actor{Subject: "carol"}); err != nil {
		t.Fatal(err)
	}
	devices := auth.NewMemoryDeviceStore()
	type device struct {
		id  string
		key *ecdsa.PrivateKey
	}
	enrol := func(subject string) device {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: "t1", Subject: subject, Name: "phone", Platform: "ios", PublicKey: der, CreatedAt: time.Now()}
		if err := devices.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
		return device{d.ID, k}
	}
	alice, carol := enrol("alice"), enrol("carol")

	mux := http.NewServeMux()
	handlers.NewProfilesHandler(devices, svc).Register(mux)
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{auth.RoleConnector}}
	post := func(as auth.Identity, path string, body []byte) (int, map[string]any) {
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, as).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// The words are the golden vector's, for anyone on the account.
	prof, err := os.ReadFile("../profile/testdata/valid/stripe-refunds.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	golden, _ := os.ReadFile("../profile/testdata/valid/stripe-refunds.expected.json")
	_ = json.Unmarshal(golden, &want)
	code, words := post(auth.Identity{Tenant: "t1", Subject: "carol", Method: auth.MethodSession}, "/v1/profiles/words", prof)
	if code != http.StatusOK || words["profile_sha256"] != want["profile_sha256"] || words["words_sha256"] != want["words_sha256"] {
		t.Fatalf("words: %d %v", code, words)
	}
	// ?v=2 answers kiff.words.v2, its golden vector's; any other v is refused.
	var want2 map[string]any
	golden2, _ := os.ReadFile("../profile/testdata/valid/v2/stripe-refunds.expected.json")
	_ = json.Unmarshal(golden2, &want2)
	code, words2 := post(auth.Identity{Tenant: "t1", Subject: "carol", Method: auth.MethodSession}, "/v1/profiles/words?v=2", prof)
	if code != http.StatusOK || words2["words"].(map[string]any)["v"] != "kiff.words.v2" || words2["words_sha256"] != want2["words_sha256"] || words2["profile_sha256"] != want["profile_sha256"] {
		t.Fatalf("v2 words: %d %v", code, words2)
	}
	if code, out := post(connector, "/v1/profiles/words?v=1", prof); code != http.StatusOK || out["words_sha256"] != want["words_sha256"] {
		t.Fatalf("v1 words asked by name: %d %v", code, out)
	}
	if code, out := post(connector, "/v1/profiles/words?v=3", prof); code != http.StatusBadRequest || out["error"] != "invalid_request" {
		t.Fatalf("an unknown version: %d %v", code, out)
	}
	if code, out := post(connector, "/v1/profiles/words", []byte(`{"v":"kiff.profile.v1"}`)); code != http.StatusUnprocessableEntity || out["error"] != "invalid_profile" {
		t.Fatalf("an invalid profile: %d %v", code, out)
	}

	approveBody := func(profileSHA, wordsSHA string) []byte {
		b, _ := json.Marshal(map[string]any{"tools": []string{"create_refund"},
			"kiff_profile": map[string]string{"profile_sha256": profileSHA, "words_sha256": wordsSHA}})
		return b
	}
	message := func(tenant, uri string, body []byte, at time.Time) string {
		sum := sha256.Sum256(body)
		return strings.Join([]string{"planque.request.v1", "https://planque.kiff.dev", tenant, "POST", uri,
			hex.EncodeToString(sum[:]), at.UTC().Format(time.RFC3339)}, "\n")
	}
	sign := func(d device, msg string) string {
		digest := sha256.Sum256([]byte(msg))
		sig, _ := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
		return base64.StdEncoding.EncodeToString(sig)
	}
	check := func(as auth.Identity, profileJSON []byte, d device, signer device, msg string, body []byte) (int, map[string]any) {
		b, _ := json.Marshal(map[string]any{"profile": json.RawMessage(profileJSON), "approval": map[string]string{
			"message": base64.StdEncoding.EncodeToString([]byte(msg)), "body": base64.StdEncoding.EncodeToString(body),
			"signature": sign(signer, msg), "device_id": d.id}})
		return post(as, "/v1/me/profile-approvals", b)
	}
	uri := "/v1/accounts/t1/grants/g1/approve"
	now := time.Now()
	good := approveBody(want["profile_sha256"].(string), want["words_sha256"].(string))

	if code, out := check(connector, prof, alice, alice, message("t1", uri, good, now), good); code != http.StatusOK || out["valid"] != true ||
		out["profile_sha256"] != want["profile_sha256"] || out["device_id"] != alice.id {
		t.Fatalf("the owner's approval: %d %v", code, out)
	}
	// An app that read v2 signs v2's digest: approved, and answered with it.
	good2 := approveBody(want["profile_sha256"].(string), want2["words_sha256"].(string))
	if code, out := check(connector, prof, alice, alice, message("t1", uri, good2, now), good2); code != http.StatusOK || out["valid"] != true ||
		out["words_sha256"] != want2["words_sha256"] {
		t.Fatalf("the owner's approval of v2 words: %d %v", code, out)
	}
	// A key-based grant's signed key request carries its tools, and applies
	// the profile the same way.
	keyURI := "/v1/accounts/t1/grants/g1/key"
	if code, out := check(connector, prof, alice, alice, message("t1", keyURI, good, now), good); code != http.StatusOK || out["valid"] != true {
		t.Fatalf("the owner's signed key request: %d %v", code, out)
	}
	other := bytes.Replace(prof, []byte(`"statuses": [400, 402, 404]`), []byte(`"statuses": [400, 402, 404, 409]`), 1)
	for name, c := range map[string]struct {
		profile   []byte
		d, signer device
		msg       string
		body      []byte
		reason    string
	}{
		"another profile than the one approved": {other, alice, alice, message("t1", uri, good, now), good, "profile_not_approved"},
		"words the owner didn't see":            {prof, alice, alice, message("t1", uri, approveBody(want["profile_sha256"].(string), strings.Repeat("0", 64)), now), approveBody(want["profile_sha256"].(string), strings.Repeat("0", 64)), "words_not_approved"},
		"a body other than the one signed":      {prof, alice, alice, message("t1", uri, []byte(`{}`), now), good, "body_mismatch"},
		"another account's approval":            {prof, alice, alice, message("t2", "/v1/accounts/t2/grants/g1/approve", good, now), good, "not_an_approval"},
		"another planque request":               {prof, alice, alice, message("t1", "/v1/accounts/t1/grants/g1/disconnect", good, now), good, "not_an_approval"},
		"an approval from two hours ago":        {prof, alice, alice, message("t1", uri, good, now.Add(-2*time.Hour)), good, "approval_expired"},
		"another device's signature":            {prof, alice, carol, message("t1", uri, good, now), good, "signature_invalid"},
		"an approver, who can't connect tools":  {prof, carol, carol, message("t1", uri, good, now), good, "not_allowed"},
	} {
		if code, out := check(connector, c.profile, c.d, c.signer, c.msg, c.body); code != http.StatusOK || out["valid"] != false || out["reason"] != c.reason {
			t.Errorf("%s: %d %v; want %s", name, code, out, c.reason)
		}
	}
	// Only a connector key checks an approval: an agent's key or a person
	// proposes a profile (its words), never applies one.
	for name, as := range map[string]auth.Identity{
		"a person":       {Tenant: "t1", Subject: "alice", Method: auth.MethodSession},
		"an agent's key": {Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true, AgentID: "coach"},
	} {
		if code, _ := check(as, prof, alice, alice, message("t1", uri, good, now), good); code != http.StatusForbidden {
			t.Errorf("%s checked an approval: %d", name, code)
		}
	}
}
