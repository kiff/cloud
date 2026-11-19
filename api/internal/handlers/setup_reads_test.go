package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

const setupReadsYAML = `domain: gateway
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
  - name: list_accounts
    allowed_states: [requested]
    required_permissions: [tools.read]
    risk: low
    approval: never
    executor: cloud.proxy
  - name: other_list
    allowed_states: [requested]
    required_permissions: [tools.read]
    risk: low
    approval: never
    executor: cloud.proxy
  - name: create_refund
    allowed_states: [requested]
    required_permissions: [tools.read]
    risk: medium
    approval: never
    executor: cloud.proxy
permissions:
  roles:
    gateway_agent: [tools.read]
`

// setupReadTools is the gateway's tools as KIFF reads them: who connected
// each, and which only read.
type setupReadTools struct {
	owners   map[string]string
	readOnly map[string]bool
}

func (s setupReadTools) ToolOwners(context.Context, string) (map[string]string, error) {
	return s.owners, nil
}

func (s setupReadTools) ReadOnlyTools(context.Context, string) (map[string]bool, error) {
	return s.readOnly, nil
}

// Invariant: Setup's Card for its reads is issued only on the owner's
// approval of that connection, which KIFF checks itself (the device's
// signature over planque's approve request, a person who may connect tools,
// signed within a day), only by the connector key that connected the tools,
// and only for tools marked read-only. It allows a few reads a day and
// refuses past them.
// Category: authority
// Attack: planque's connector key, or anyone holding one, grants an agent authority the owner never approved: another connection's or another account's approval, an old one, another person's device, a tool that writes, or a tool another key connected
// Refused: each is a 403 and no Card is issued
func TestSetupReadsCardNeedsTheOwnersApproval(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
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
	enrol := func(tenant, subject string) device {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		d := auth.ApprovalDevice{ID: auth.NewDeviceID(), Tenant: tenant, Subject: subject, Name: "Alice's iPad", Platform: "ios", PublicKey: der, CreatedAt: now}
		if err := devices.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
		return device{d.ID, k}
	}
	sign := func(d device, msg string) string {
		digest := sha256.Sum256([]byte(msg))
		sig, _ := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
		return base64.StdEncoding.EncodeToString(sig)
	}
	const approveBody = `{"digest":"dg_1"}`
	bodySum := sha256.Sum256([]byte(approveBody))
	approve := func(tenant, grant string, at time.Time) string {
		return strings.Join([]string{"planque.request.v1", "https://planque.kiff.dev", tenant, "POST",
			"/v1/accounts/" + tenant + "/grants/" + grant + "/approve", hex.EncodeToString(bodySum[:]), at.Format(time.RFC3339)}, "\n")
	}
	alice, carol := enrol("t1", "alice"), enrol("t1", "carol")

	store := mandate.NewMemoryStore()
	tools := setupReadTools{
		owners:   map[string]string{"list_accounts": "key_conn", "other_list": "key_other", "create_refund": "key_conn"},
		readOnly: map[string]bool{"list_accounts": true, "other_list": true},
	}
	keys := auth.NewMemoryAPIKeyStore()
	for _, k := range []auth.APIKeyRecord{
		{ID: "key-setup", Tenant: "t1", Subject: "alice", AgentID: "setup", Roles: []string{auth.RoleSetupReader}, KeyHash: sha256.Sum256([]byte("s")), CreatedAt: now},
		{ID: "key-rae", Tenant: "t1", Subject: "alice", AgentID: "rae", Roles: []string{auth.RoleGatewayAgent}, KeyHash: sha256.Sum256([]byte("r")), CreatedAt: now},
	} {
		if err := keys.Insert(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	h := NewMandatesHandler(store, stubDomainSource{yaml: setupReadsYAML},
		WithMandatesClock(func() time.Time { return now }), WithMandatesMemberships(members),
		WithMandatesSetupReads(devices, svc, tools, keys))
	mux := http.NewServeMux()
	h.Register(mux)
	connector := auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{auth.RoleConnector}, Metadata: map[string]string{"api_key_id": "key_conn"}}
	askFor := func(as auth.Identity, agent, body string, d device, msg, sig string, toolNames ...string) int {
		t.Helper()
		return do(t, mux, as, http.MethodPost, "/v1/me/setup-reads", map[string]any{"agent": agent, "grant": "grt_1", "tools": toolNames,
			"approval": map[string]string{"device_id": d.id, "message": msg, "body": body, "signature": sig}}).Code
	}
	ask := func(as auth.Identity, d device, msg, sig string, toolNames ...string) int {
		t.Helper()
		return askFor(as, "setup", approveBody, d, msg, sig, toolNames...)
	}
	issued := func() []mandate.Mandate {
		ms, err := store.List(ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		return ms
	}

	ok := approve("t1", "grt_1", now.Add(-2*time.Hour))
	for name, c := range map[string]struct {
		as    auth.Identity
		d     device
		msg   string
		sig   string
		tools []string
	}{
		"not a connector key":        {auth.Identity{Tenant: "t1", Subject: "alice", Method: auth.MethodSession, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}, alice, ok, sign(alice, ok), []string{"list_accounts"}},
		"another connection":         {connector, alice, approve("t1", "grt_2", now.Add(-time.Hour)), sign(alice, approve("t1", "grt_2", now.Add(-time.Hour))), []string{"list_accounts"}},
		"another account":            {connector, alice, approve("t2", "grt_1", now.Add(-time.Hour)), sign(alice, approve("t2", "grt_1", now.Add(-time.Hour))), []string{"list_accounts"}},
		"approved over a day ago":    {connector, alice, approve("t1", "grt_1", now.Add(-25*time.Hour)), sign(alice, approve("t1", "grt_1", now.Add(-25*time.Hour))), []string{"list_accounts"}},
		"another device's signature": {connector, alice, ok, sign(carol, ok), []string{"list_accounts"}},
		"a person who can't connect": {connector, carol, ok, sign(carol, ok), []string{"list_accounts"}},
		"a tool that writes":         {connector, alice, ok, sign(alice, ok), []string{"create_refund"}},
		"another key's tool":         {connector, alice, ok, sign(alice, ok), []string{"other_list"}},
		"a tool not connected":       {connector, alice, ok, sign(alice, ok), []string{"nope"}},
	} {
		if code := ask(c.as, c.d, c.msg, c.sig, c.tools...); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	// The approval binds what was approved, and only an agent holding no
	// tools gets a read Card.
	if code := askFor(connector, "setup", `{"digest":"dg_other"}`, alice, ok, sign(alice, ok), "list_accounts"); code != http.StatusForbidden {
		t.Errorf("another body than the one signed: %d, want 403", code)
	}
	for _, agent := range []string{"rae", "nobody"} {
		if code := askFor(connector, agent, approveBody, alice, ok, sign(alice, ok), "list_accounts"); code != http.StatusForbidden {
			t.Errorf("a read Card for %s: %d, want 403", agent, code)
		}
	}
	if ms := issued(); len(ms) != 0 {
		t.Fatalf("a refused request issued %d Card(s)", len(ms))
	}

	if code := ask(connector, alice, ok, sign(alice, ok), "list_accounts"); code != http.StatusCreated {
		t.Fatalf("the owner's approval: %d", code)
	}
	// Asked again for the same connection, it replaces the Card.
	again := approve("t1", "grt_1", now.Add(-time.Minute))
	if code := ask(connector, alice, again, sign(alice, again), "list_accounts"); code != http.StatusCreated {
		t.Fatalf("asked again: %d", code)
	}
	ms := issued()
	if len(ms) != 1 {
		t.Fatalf("Cards: %+v", ms)
	}
	m := ms[0]
	if m.ID != SetupReadsCardID("grt_1") || m.Subject != "setup" || m.Domain != "gateway" || m.OnExceed != mandate.OnExceedRefuse ||
		len(m.Grants) != 1 || m.Grants[0].Action != "list_accounts" || len(m.Grants[0].PerAction) != 0 ||
		len(m.Aggregates) != 1 || m.Aggregates[0].Quantity.Kind != mandate.KindCount || m.Aggregates[0].Limit != SetupReadsPerDay ||
		m.Aggregates[0].Window != mandate.WindowCalendarDay {
		t.Fatalf("Setup's read Card: %+v", m)
	}
	revs, err := store.Revisions(ctx, "t1", m.ID)
	if err != nil || len(revs) == 0 || !strings.Contains(revs[len(revs)-1].By, "planque, on the approval of alice on Alice's iPad") {
		t.Fatalf("who issued it: %+v %v", revs, err)
	}
}
