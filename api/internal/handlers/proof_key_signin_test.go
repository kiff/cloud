package handlers_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// The proof runner's test phone key is never signed in, made an approval
// key, or paired on a tenant outside the test tenants.

func restrictTo(key *[]byte, listed *bool, tenants ...string) *auth.KeyRestriction {
	return auth.NewKeyRestriction(func(der []byte) bool { return *listed && bytes.Equal(der, *key) }, tenants)
}

func TestATestKeySignsInOnlyOnATestTenant(t *testing.T) {
	t.Parallel()
	_, testDER := p256(t)
	listed := true
	for _, c := range []struct {
		tenant string
		want   int
	}{{"tenant-customer", http.StatusForbidden}, {"tenant-test", http.StatusOK}} {
		sessions := auth.NewMemoryDeveloperSessionStore()
		h := handlers.NewDeviceAuthHandler(sessions, handlers.WithDeviceKeyRestriction(restrictTo(&testDER, &listed, "tenant-test")))
		pub := http.NewServeMux()
		h.RegisterPublic(pub)
		inner := http.NewServeMux()
		h.RegisterAuthenticated(inner)

		body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(testDER)})
		rec := httptest.NewRecorder()
		pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", bytes.NewReader(body)))
		var start struct {
			UserCode string `json:"user_code"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &start) != nil {
			t.Fatalf("start: %d %s", rec.Code, rec.Body)
		}
		approve, _ := json.Marshal(map[string]string{"user_code": start.UserCode})
		rec = httptest.NewRecorder()
		withFakeIdentity(inner, auth.Identity{Tenant: c.tenant, Subject: "user-abc", Method: auth.MethodSession}).
			ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/device/approve", bytes.NewReader(approve)))
		if rec.Code != c.want {
			t.Fatalf("approve on %s: %d %s, want %d", c.tenant, rec.Code, rec.Body, c.want)
		}
		if c.want == http.StatusForbidden && !bytes.Contains(rec.Body.Bytes(), []byte(auth.KeyRestrictedCode)) {
			t.Fatalf("refusal without %s: %s", auth.KeyRestrictedCode, rec.Body)
		}
	}
}

func TestATestKeyIsNeverMadeAnApprovalKeyOnACustomersTenant(t *testing.T) {
	t.Parallel()
	_, testDER := p256(t)
	listed := true
	r := newPairRig(t, handlers.WithDeviceKeyRestriction(restrictTo(&testDER, &listed, "tenant-test")))
	body, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(testDER)})
	code, out := send(r.authed, &r.phone, http.MethodPost, "/v1/me/device/approval-key", string(body),
		signRequest(t, r.device, r.phone.Tenant, http.MethodPost, "/v1/me/device/approval-key", string(body), r.now))
	if code != http.StatusForbidden || out["error"] != auth.KeyRestrictedCode {
		t.Fatalf("registering the test key on a customer's tenant: %d %v", code, out)
	}
}

func TestATestKeyPairsOnlyOnATestTenant(t *testing.T) {
	t.Parallel()
	listed := true
	var testDER []byte
	r := newPairRig(t, handlers.WithDeviceKeyRestriction(restrictTo(&testDER, &listed, "tenant-test")))
	testDER = r.pcDER // the computer being paired holds the test key

	// The phone approves on tenant-1, a customer's: refused at the approval.
	if code, out := r.approve(r.qr()); code != http.StatusForbidden || out["error"] != auth.KeyRestrictedCode {
		t.Fatalf("approving the test key's pairing on a customer's tenant: %d %v", code, out)
	}

	// Approved before the key was listed, then claimed after: refused at the claim.
	listed = false
	q := r.qr()
	if code, out := r.approve(q); code != http.StatusCreated {
		t.Fatalf("approval before the key was listed: %d %v", code, out)
	}
	listed = true
	r.now = r.now.Add(time.Second)
	if code, out := r.claim(q["nonce"].(string), r.pcDER, r.pcKey); code != http.StatusForbidden || out["error"] != auth.KeyRestrictedCode {
		t.Fatalf("claiming the test key's pairing on a customer's tenant: %d %v", code, out)
	}

	// On a test tenant it pairs and claims.
	t2 := newPairRig(t, handlers.WithDeviceKeyRestriction(restrictTo(&testDER, &listed, r.phone.Tenant)))
	testDER = t2.pcDER
	q = t2.qr()
	if code, out := t2.approve(q); code != http.StatusCreated {
		t.Fatalf("approval on the test tenant: %d %v", code, out)
	}
	if code, out := t2.claim(q["nonce"].(string), t2.pcDER, t2.pcKey); code != http.StatusOK {
		t.Fatalf("claim on the test tenant: %d %v", code, out)
	}
}
