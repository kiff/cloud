package handlers_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// option 2: a phone's session makes a taf's setup calls (agent,
// Card, gateway key) only with its key's signature over that exact
// request. The handler gets the body that was signed; a changed body, an
// old signature or a replay is refused.
func TestPhoneSetupCallsNeedTheKeysSignature(t *testing.T) {
	t.Parallel()
	var got []string
	inner := http.NewServeMux()
	for _, pattern := range []string{"POST /v1/me/agents", "POST /v1/me/mandates", "POST /v1/keys"} {
		inner.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = append(got, r.URL.Path+" "+string(b))
			w.WriteHeader(http.StatusCreated)
		})
	}
	policed := auth.DefaultRoutePolicy().Wrap(inner)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	phone := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: der, DeviceSessionID: "sess-phone"}
	sign := func(path, body string, at time.Time) string {
		ts := at.UTC().Format(time.RFC3339)
		digest := sha256.Sum256(auth.RequestMessage("tenant-1", http.MethodPost, path, []byte(body), ts))
		sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)
	}
	post := func(path, body, header string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		if header != "" {
			req.Header.Set(auth.DeviceSignatureHeader, header)
		}
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, phone).ServeHTTP(rec, req)
		return rec
	}

	now := time.Now()
	for _, c := range []struct{ path, body string }{
		{"/v1/me/agents", `{"id":"rae"}`},
		{"/v1/me/mandates", `{"subject":"rae","grants":[{"action":"refund"}]}`},
		{"/v1/keys", `{"label":"rae","roles":["gateway_agent"],"agent_id":"rae"}`},
	} {
		h := sign(c.path, c.body, now)
		if rec := post(c.path, c.body, h); rec.Code != http.StatusCreated {
			t.Fatalf("signed %s: %d %s", c.path, rec.Code, rec.Body.String())
		}
		if rec := post(c.path, c.body, h); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "signature_replayed") {
			t.Errorf("replayed %s: %d %s", c.path, rec.Code, rec.Body.String())
		}
	}
	if want := []string{`/v1/me/agents {"id":"rae"}`, `/v1/me/mandates {"subject":"rae","grants":[{"action":"refund"}]}`,
		`/v1/keys {"label":"rae","roles":["gateway_agent"],"agent_id":"rae"}`}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("handlers got %q", got)
	}

	// a review: an ECDSA signature has a second valid form, (r, n-s),
	// that anyone can make from the first. The request it signs is still
	// the one already made, so it is refused before the handler; so is a
	// fresh signature over the same request.
	at := now.Add(time.Second)
	keyBody := `{"label":"rae-2","roles":["gateway_agent"],"agent_id":"rae"}`
	first := sign("/v1/keys", keyBody, at)
	if rec := post("/v1/keys", keyBody, first); rec.Code != http.StatusCreated {
		t.Fatalf("signed key: %d %s", rec.Code, rec.Body.String())
	}
	for name, h := range map[string]string{"n-s twin": malleate(t, first), "fresh signature": sign("/v1/keys", keyBody, at)} {
		if h == first {
			t.Fatalf("%s is the same header", name)
		}
		if rec := post("/v1/keys", keyBody, h); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "signature_replayed") {
			t.Errorf("%s of a used request: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(got) != 4 {
		t.Fatalf("a replay reached a handler: %q", got)
	}

	if rec := post("/v1/me/agents", `{"id":"eve"}`, sign("/v1/me/agents", `{"id":"rae"}`, now)); rec.Code != http.StatusForbidden {
		t.Errorf("a changed body went through: %d", rec.Code)
	}
	if rec := post("/v1/keys", `{"agent_id":"rae"}`, sign("/v1/me/agents", `{"agent_id":"rae"}`, now)); rec.Code != http.StatusForbidden {
		t.Errorf("a signature for another route went through: %d", rec.Code)
	}
	if rec := post("/v1/me/agents", `{"id":"rae"}`, sign("/v1/me/agents", `{"id":"rae"}`, now.Add(-10*time.Minute))); rec.Code != http.StatusBadRequest {
		t.Errorf("an old signature: %d %s", rec.Code, rec.Body.String())
	}
	if len(got) != 4 {
		t.Errorf("a refused call reached its handler: %q", got)
	}
}

// malleate turns a KIFF-Device-Signature's (r, s) into (r, n-s): a second
// signature over the same message, made without the key.
func malleate(t *testing.T, header string) string {
	t.Helper()
	prefix, v, ok := strings.Cut(header, ",v1=")
	if !ok {
		t.Fatalf("header %q", header)
	}
	der, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		t.Fatal(err)
	}
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		t.Fatal(err)
	}
	sig.S = new(big.Int).Sub(elliptic.P256().Params().N, sig.S)
	out, err := asn1.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	return prefix + ",v1=" + base64.StdEncoding.EncodeToString(out)
}
