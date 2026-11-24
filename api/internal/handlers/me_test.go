package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/tenant"
)

// meRig wraps the MeHandler under a synthetic auth so we can
// drive it from tests. Returns the wrapped mux so tests can hit
// /v1/me directly with the configured identity already on the
// context.
type meRig struct {
	mux      http.Handler
	tenantID string
	tenant   tenant.Tenant
}

// newMeRig provisions a tenant with the given metadata, mounts
// the MeHandler under a fake-identity wrapper, and returns the
// rig. Metadata is the operator-controlled bag tenant.PlanFor
// reads from; pass nil for the free_dev default, or
// {"plan": "builder"} / {"plan": "agency_team"} to exercise the
// paid tiers.
func newMeRig(t *testing.T, metadata map[string]any) *meRig {
	t.Helper()
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "tenant-me",
		DisplayName: "Tenant Me",
		Status:      tenant.StatusActive,
		Metadata:    metadata,
	}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	h := handlers.NewMeHandler(store)
	innerMux := http.NewServeMux()
	h.Register(innerMux)

	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "principal-me",
		Email:   "ops@example.test",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
	}))

	return &meRig{mux: wrapped, tenantID: tnt.ID, tenant: tnt}
}

// driveGetMe hits GET /v1/me on the rig and returns the parsed
// JSON body for assertion. Fails the test on any non-200.
func driveGetMe(t *testing.T, rig *meRig) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body = %s", err, rec.Body.String())
	}
	return got
}

// TestMe_FreeDevTenantWireShape pins the JSON tags for the new
// plan + monthly_proposal_limit fields against a tenant with no
// plan metadata (defaults to free_dev). If a future change
// renames either tag the dashboard's apiclient.Me unmarshal
// silently zeroes the field, the dashboard renders free_dev for
// every tenant, and no end-to-end test catches it. This test
// asserts the wire shape directly so the regression surfaces
// immediately.
func TestMe_FreeDevTenantWireShape(t *testing.T) {
	t.Parallel()
	rig := newMeRig(t, nil)
	got := driveGetMe(t, rig)

	if got["plan"] != "trial" {
		t.Errorf(`got["plan"] = %v, want "trial"`, got["plan"])
	}
	if got["monthly_proposal_limit"] != float64(5000) {
		t.Errorf(`got["monthly_proposal_limit"] = %v, want the trial's 5000`, got["monthly_proposal_limit"])
	}
	// Sanity: tenant fields are still on the wire under their
	// existing tags. Catches an accidental rename of either.
	if got["tenant_id"] != rig.tenantID {
		t.Errorf(`got["tenant_id"] = %v, want %s`, got["tenant_id"], rig.tenantID)
	}
	if got["tenant_slug"] != "tenant-me" {
		t.Errorf(`got["tenant_slug"] = %v, want "tenant-me"`, got["tenant_slug"])
	}
}

// TestMe_BuilderTenantWireShape pins the wire shape for a paid
// tenant. The dashboard renders the "paid" pill iff
// apiclient.Me.Plan unmarshals to "builder" (or another
// non-free_dev tier), so a JSON-tag rename here would silently
// downgrade every paid tenant's UI to the free_dev experience.
func TestMe_BuilderTenantWireShape(t *testing.T) {
	t.Parallel()
	rig := newMeRig(t, map[string]any{tenant.MetadataKeyPlan: "builder"})
	got := driveGetMe(t, rig)

	if got["plan"] != "builder" {
		t.Errorf(`got["plan"] = %v, want "builder"`, got["plan"])
	}
	if got["monthly_proposal_limit"] != float64(5000) {
		t.Errorf(`got["monthly_proposal_limit"] = %v, want the trial's 5000 (a legacy plan is the trial's)`, got["monthly_proposal_limit"])
	}
}

// TestMe_CanonicalAndBillingFields pins the additions: the web
// reads canonical_plan + billing_status + has_billing directly off
// /v1/me. A legacy alias ("builder") must surface its canonical plan
// ("growth"); the billing fields come from the webhook-written tenant
// metadata. A JSON-tag rename here silently breaks the dashboard's
// plan card and the past_due banner, so assert the wire shape.
func TestMe_CanonicalAndBillingFields(t *testing.T) {
	t.Parallel()
	rig := newMeRig(t, map[string]any{
		tenant.MetadataKeyPlan: "builder",
		"billing_status":       "past_due",
		"stripe_customer_id":   "cus_test123",
	})
	got := driveGetMe(t, rig)

	if got["plan"] != "builder" {
		t.Errorf(`got["plan"] = %v, want "builder" (raw alias preserved)`, got["plan"])
	}
	if got["canonical_plan"] != "trial" {
		t.Errorf(`got["canonical_plan"] = %v, want "trial" (a legacy plan is the trial's)`, got["canonical_plan"])
	}
	if got["billing_status"] != "past_due" {
		t.Errorf(`got["billing_status"] = %v, want "past_due"`, got["billing_status"])
	}
	if got["has_billing"] != true {
		t.Errorf(`got["has_billing"] = %v, want true`, got["has_billing"])
	}
}

// TestMe_NoBillingOmitsFields confirms a tenant that never carried a
// subscription reports no billing_status and has_billing false/absent,
// so the dashboard shows no banner and hides the Manage-billing action.
func TestMe_NoBillingOmitsFields(t *testing.T) {
	t.Parallel()
	rig := newMeRig(t, nil)
	got := driveGetMe(t, rig)

	if _, present := got["billing_status"]; present {
		t.Errorf(`billing_status should be omitted for a never-subscribed tenant, got %v`, got["billing_status"])
	}
	if v, present := got["has_billing"]; present && v == true {
		t.Errorf(`has_billing should be false/omitted, got %v`, v)
	}
	if got["canonical_plan"] != "trial" {
		t.Errorf(`got["canonical_plan"] = %v, want "trial"`, got["canonical_plan"])
	}
}

// A key bound to an agent names that agent, so a caller holding only
// the key can build proposals for it; an unbound key names none.
func TestMe_AgentID(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-agent", Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatal(err)
	}
	inner := http.NewServeMux()
	handlers.NewMeHandler(store).Register(inner)
	for _, agent := range []string{"coding-agent", ""} {
		mux := withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "k", Method: auth.MethodAPIKey, APIKey: true, AgentID: agent})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if v, ok := got["agent_id"]; agent == "" && ok || agent != "" && v != agent {
			t.Errorf("agent %q: agent_id = %v", agent, v)
		}
	}
}

func TestMe_TimeZone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		want     string
	}{
		{"legacy", nil, "UTC"},
		{"saved", map[string]any{"time_zone": "Europe/Paris"}, "Europe/Paris"},
		{"invalid", map[string]any{"time_zone": "Local"}, "UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := driveGetMe(t, newMeRig(t, tc.metadata))
			if got["time_zone"] != tc.want {
				t.Fatalf("time_zone=%v want %s", got["time_zone"], tc.want)
			}
		})
	}
}

// /v1/me says when the caller is a phone's session bound to its key, so
// the gateway lets it read but not change the account.
func TestMe_SaysWhenTheCallerIsAPhone(t *testing.T) {
	t.Parallel()
	rig := newMeRig(t, nil)
	h := handlers.NewMeHandler(tenantStoreWith(t, rig.tenant))
	inner := http.NewServeMux()
	h.Register(inner)
	for name, id := range map[string]auth.Identity{
		"phone":   {Tenant: rig.tenantID, Subject: "p", Method: auth.MethodDeveloper, DeviceKey: []byte{0x30}, DeviceSessionID: "s"},
		"browser": {Tenant: rig.tenantID, Subject: "p", Method: auth.MethodSession},
	} {
		rec := httptest.NewRecorder()
		withFakeIdentity(inner, id).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
		var got map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if phone, _ := got["device_session"].(bool); rec.Code != http.StatusOK || phone != (name == "phone") {
			t.Errorf("%s: %d device_session=%v", name, rec.Code, got["device_session"])
		}
	}
}

// /v1/me says how a device's session approves (approval_mode), so a
// computer that approves with a passkey isn't shown as view-only, and says
// nothing of it to a browser or a key.
func TestMe_SaysHowADeviceSessionApproves(t *testing.T) {
	t.Parallel()
	rig := newMeRig(t, nil)
	h := handlers.NewMeHandler(tenantStoreWith(t, rig.tenant))
	inner := http.NewServeMux()
	h.Register(inner)
	device := func(mode string, approvalKey []byte, keyStore string) auth.Identity {
		return auth.Identity{Tenant: rig.tenantID, Subject: "p", Method: auth.MethodDeveloper, DeviceKey: []byte{0x30}, DeviceSessionID: "s",
			ApprovalMode: mode, ApprovalKey: approvalKey, KeyStore: keyStore}
	}
	for name, c := range map[string]struct {
		id   auth.Identity
		want string
	}{
		"approval key":           {device(auth.ApprovalModeKey, []byte{1}, ""), "key"},
		"passkey":                {device(auth.ApprovalModePasskey, nil, ""), "passkey"},
		"view":                   {device(auth.ApprovalModeView, nil, ""), "view"},
		"software key store":     {device(auth.ApprovalModeKey, nil, auth.KeyStoreSoftware), "view"},
		"software, security key": {device(auth.ApprovalModePasskey, nil, auth.KeyStoreSoftware), "passkey"},
		"before modes, key":      {device("", []byte{1}, ""), "key"},
		"before modes, no key":   {device("", nil, ""), "device_key"},
		"browser":                {auth.Identity{Tenant: rig.tenantID, Subject: "p", Method: auth.MethodSession}, ""},
		"cli":                    {auth.Identity{Tenant: rig.tenantID, Subject: "p", Method: auth.MethodDeveloper}, ""},
	} {
		rec := httptest.NewRecorder()
		withFakeIdentity(inner, c.id).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
		var got map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		mode, present := got["approval_mode"]
		if rec.Code != http.StatusOK || (c.want == "" && present) || (c.want != "" && mode != c.want) {
			t.Errorf("%s: %d approval_mode=%v, want %q", name, rec.Code, mode, c.want)
		}
	}
}

func tenantStoreWith(t *testing.T, tnt tenant.Tenant) *tenant.MemoryStore {
	t.Helper()
	store := tenant.NewMemoryStore()
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatal(err)
	}
	return store
}

// emailLookup is Clerk's lookup, faked: one email by subject, or an error.
type emailLookup struct {
	email string
	err   error
	asked []string
}

func (e *emailLookup) EmailForSubject(_ context.Context, subject string) (string, error) {
	e.asked = append(e.asked, subject)
	return e.email, e.err
}

// meAs serves GET /v1/me as id, with emails resolving the email.
func meAs(t *testing.T, id auth.Identity, emails *emailLookup) map[string]any {
	t.Helper()
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-me", DisplayName: "Tenant Me", Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatal(err)
	}
	id.Tenant = tnt.ID
	inner := http.NewServeMux()
	handlers.NewMeHandler(store, handlers.WithMeEmails(emails, nil)).Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, id))
	return driveGetMe(t, &meRig{mux: wrapped, tenantID: tnt.ID, tenant: tnt})
}

// A phone's developer session carries no email, so /v1/me reads it from
// Clerk. It's never stored. A failed lookup leaves the email out and
// /v1/me still answers. A browser session isn't looked up (the dashboard has
// the email from its own sign-in), and nor is a key's caller.
func TestMe_EmailIsResolvedForAPhone(t *testing.T) {
	phone := auth.Identity{Subject: "user_1", Method: auth.MethodDeveloper, DeviceKey: []byte{1}}
	emails := &emailLookup{email: "owner@example.test"}
	if got := meAs(t, phone, emails); got["email"] != "owner@example.test" || len(emails.asked) != 1 || emails.asked[0] != "user_1" {
		t.Fatalf("phone: email %v, asked %v", got["email"], emails.asked)
	}
	browser := &emailLookup{email: "owner@example.test"}
	if got := meAs(t, auth.Identity{Subject: "user_1", Method: auth.MethodSession}, browser); got["email"] != nil || len(browser.asked) != 0 {
		t.Fatalf("browser: email %v, asked %v; want no lookup", got["email"], browser.asked)
	}

	failing := &emailLookup{err: errors.New("clerk down")}
	if got := meAs(t, phone, failing); got["email"] != nil || got["tenant_slug"] != "tenant-me" {
		t.Fatalf("a failed lookup: %v; want 200 without email", got)
	}

	carried := &emailLookup{email: "other@example.test"}
	if got := meAs(t, auth.Identity{Subject: "user_1", Method: auth.MethodSession, Email: "own@example.test"}, carried); got["email"] != "own@example.test" || len(carried.asked) != 0 {
		t.Fatalf("an email the sign-in carries: %v, asked %v", got["email"], carried.asked)
	}

	key := &emailLookup{email: "owner@example.test"}
	if got := meAs(t, auth.Identity{Subject: "user_1", Method: auth.MethodAPIKey, APIKey: true}, key); got["email"] != nil || len(key.asked) != 0 {
		t.Fatalf("an API key: email %v, asked %v; want none", got["email"], key.asked)
	}
}

// /v1/me says which device a device's session belongs to and its platform
// (device_id, device_platform), from KIFF's own record of the verified
// session's device, so another service can tell a Chrome from a phone.
// A device kept from before platforms gives its id and no platform. A browser session, a session without a device, or a
// device that isn't the person's own, live, on this account gives neither,
// and nothing the request says changes what KIFF answers.
func TestMe_SaysTheSessionsDeviceAndItsPlatform(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rig := newMeRig(t, nil)
	devices := auth.NewMemoryDeviceStore()
	removed := time.Now()
	key := func() []byte { _, der := p256(t); return der }
	for _, d := range []auth.ApprovalDevice{
		{ID: "dev_phone", PublicKey: key(), Tenant: rig.tenantID, Subject: "p", Platform: "ios"},
		{ID: "dev_chrome", PublicKey: key(), Tenant: rig.tenantID, Subject: "p", Platform: "chrome"},
		{ID: "dev_old", PublicKey: key(), Tenant: rig.tenantID, Subject: "p"},
		{ID: "dev_other", PublicKey: key(), Tenant: rig.tenantID, Subject: "someone-else", Platform: "ios"},
		{ID: "dev_elsewhere", PublicKey: key(), Tenant: "another-account", Subject: "p", Platform: "chrome"},
		{ID: "dev_removed", PublicKey: key(), Tenant: rig.tenantID, Subject: "p", Platform: "chrome", RevokedAt: &removed},
	} {
		if err := devices.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	inner := http.NewServeMux()
	handlers.NewMeHandler(tenantStoreWith(t, rig.tenant), handlers.WithMeDevices(devices)).Register(inner)
	session := func(device string) auth.Identity {
		return auth.Identity{Tenant: rig.tenantID, Subject: "p", Method: auth.MethodDeveloper, DeviceKey: []byte{0x30}, DeviceSessionID: "s", DeviceID: device}
	}
	for name, c := range map[string]struct {
		id               auth.Identity
		device, platform string
	}{
		"a phone":                   {session("dev_phone"), "dev_phone", "ios"},
		"a Chrome":                  {session("dev_chrome"), "dev_chrome", "chrome"},
		"a device with no platform": {session("dev_old"), "dev_old", ""},
		"a browser":                 {auth.Identity{Tenant: rig.tenantID, Subject: "p", Method: auth.MethodSession, DeviceID: "dev_chrome"}, "", ""},
		"a session with no device":  {session(""), "", ""},
		"someone else's device":     {session("dev_other"), "", ""},
		"another account's device":  {session("dev_elsewhere"), "", ""},
		"a removed device":          {session("dev_removed"), "", ""},
		"an unknown device":         {session("dev_missing"), "", ""},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		// What the request claims is never read.
		req.Header.Set("X-Device-Platform", "ios")
		req.Header.Set("Device-Id", "dev_phone")
		rec := httptest.NewRecorder()
		withFakeIdentity(inner, c.id).ServeHTTP(rec, req)
		var got map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		device, _ := got["device_id"].(string)
		platform, _ := got["device_platform"].(string)
		if rec.Code != http.StatusOK || device != c.device || platform != c.platform {
			t.Errorf("%s: %d device_id=%q device_platform=%q, want %q %q", name, rec.Code, device, platform, c.device, c.platform)
		}
	}
}
