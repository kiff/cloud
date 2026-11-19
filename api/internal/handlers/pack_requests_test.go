package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

type alertsSeen struct {
	mu     sync.Mutex
	events []alert.Event
}

func (a *alertsSeen) Notify(ev alert.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
}

type mailSeen struct {
	mu   sync.Mutex
	sent map[string]notify.AccountNotice
	done chan struct{}
	once sync.Once
}

func (m *mailSeen) MemberInvited(context.Context, string, notify.MemberInvite) error { return nil }

func (m *mailSeen) AccountNotice(_ context.Context, to string, n notify.AccountNotice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent[to] = n
	m.once.Do(func() { close(m.done) })
	return nil
}

type emailBook map[string]string

func (e emailBook) EmailForSubject(_ context.Context, subject string) (string, error) {
	return e[subject], nil
}

func packPerson(tenantID, subject string, role membership.Role) auth.Identity {
	return auth.Identity{Subject: subject, Tenant: tenantID, MemberRole: role, Roles: membership.IdentityRoles(role)}
}

// A member asks an admin for a pack: the request is recorded, the app's
// backend told, and the admins emailed. The member sees theirs, waiting,
// and may withdraw it; an admin sees every open one, and may decline it
// with a note. The first answer counts.
func TestPackRequests(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "acme", Status: tenant.StatusActive, Metadata: map[string]any{
		tenant.MetadataKeyPlan:        "solo",
		tenant.MetadataKeyPeriodStart: "2026-10-10T00:00:00Z", tenant.MetadataKeyPeriodEnd: "2026-11-10T00:00:00Z",
		// alice's app shows Italian: her email comes in it.
		personlang.MetadataKey: map[string]any{"people": map[string]any{"alice": "it"}}}}); err != nil {
		t.Fatal(err)
	}
	catalog, err := onSale()
	if err != nil {
		t.Fatal(err)
	}
	store := packs.NewMemory()
	alerts := &alertsSeen{}
	mail := &mailSeen{sent: map[string]notify.AccountNotice{}, done: make(chan struct{})}
	people := fakeMembers{
		{Subject: "alice", Role: membership.RoleAdmin, Status: membership.StatusActive},
		{Subject: "bob", Role: membership.RoleEditor, Status: membership.StatusActive},
		{Subject: "gone", Role: membership.RoleAdmin, Status: membership.StatusSuspended},
	}
	mux := http.NewServeMux()
	NewPackRequestsHandler(tenants, store, catalog, slog.Default()).WithAlerts(alerts).
		WithEmail(people, emailBook{"alice": "alice@example.test", "bob": "bob@example.test", "gone": "gone@example.test"}, mail, "https://app.kiff.dev").
		WithClock(func() time.Time { return now }).Register(mux)
	admin, member := packPerson(id, "alice", membership.RoleAdmin), packPerson(id, "bob", membership.RoleEditor)
	call := func(who auth.Identity, method, path, body string) (int, map[string]any) {
		t.Helper()
		rr := serve(mux, who, method, path, body, nil)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}

	code, req := call(member, http.MethodPost, "/v1/me/pack-requests", `{"pack": "rayo", "reason": "Refunds ran out"}`)
	if code != http.StatusCreated || req["status"] != "open" || req["pack"] != "rayo" || req["expires_at"] != "2026-11-10T00:00:00Z" {
		t.Fatalf("ask: %d %v", code, req)
	}
	reqID := req["id"].(string)
	if len(alerts.events) != 1 || alerts.events[0].Outcome != alert.PackRequested || alerts.events[0].EntityID != reqID ||
		alerts.events[0].Request == nil || alerts.events[0].Request.AskedBy != "bob" || alerts.events[0].Request.Pack != "rayo" {
		t.Fatalf("alerts: %+v", alerts.events)
	}
	select {
	case <-mail.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no admin was emailed")
	}
	mail.mu.Lock()
	n, ok := mail.sent["alice@example.test"]
	if len(mail.sent) != 1 || !ok || n.Lang != words.IT || n.Subject != "Una richiesta di pacchetto di crediti tafs" ||
		!strings.Contains(n.Body, "bob@example.test chiede un pacchetto di crediti: Rayo.") || !strings.Contains(n.Body, "Il motivo: Refunds ran out") {
		t.Fatalf("emails: %+v", mail.sent)
	}
	mail.mu.Unlock()

	for name, c := range map[string]struct {
		who  auth.Identity
		body string
		want int
	}{
		"asking twice":    {member, `{"pack": "pico"}`, http.StatusConflict},
		"an admin asking": {admin, `{"pack": "pico"}`, http.StatusConflict},
		"no such pack":    {packPerson(id, "carol", membership.RoleViewer), `{"pack": "giga"}`, http.StatusBadRequest},
		// Packs of tasks are no longer sold: nobody could answer the request.
		"a pack of tasks":   {packPerson(id, "carol", membership.RoleViewer), `{"pack": "forte"}`, http.StatusBadRequest},
		"a reason too long": {packPerson(id, "carol", membership.RoleViewer), `{"pack": "pico", "reason": "` + strings.Repeat("x", 501) + `"}`, http.StatusBadRequest},
		"an agent's key":    {auth.Identity{Subject: "k", Tenant: id, AgentID: "taf"}, `{"pack": "pico"}`, http.StatusForbidden},
	} {
		if code, out := call(c.who, http.MethodPost, "/v1/me/pack-requests", c.body); code != c.want {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}

	// The admin sees it open; the member sees theirs.
	if _, out := call(admin, http.MethodGet, "/v1/me/pack-requests", ""); len(out["items"].([]any)) != 1 {
		t.Fatalf("admin's list: %v", out)
	}
	if _, out := call(packPerson(id, "carol", membership.RoleViewer), http.MethodGet, "/v1/me/pack-requests", ""); len(out["items"].([]any)) != 0 {
		t.Fatalf("another member sees it: %v", out)
	}
	// A member can't decline; someone else can't withdraw it.
	if code, _ := call(member, http.MethodPost, "/v1/me/pack-requests/"+reqID+"/decline", `{}`); code != http.StatusForbidden {
		t.Fatalf("a member declined: %d", code)
	}
	if code, _ := call(packPerson(id, "carol", membership.RoleViewer), http.MethodPost, "/v1/me/pack-requests/"+reqID+"/withdraw", ""); code != http.StatusNotFound {
		t.Fatalf("withdrawn by another: %d", code)
	}
	code, out := call(admin, http.MethodPost, "/v1/me/pack-requests/"+reqID+"/decline", `{"note": "Next month"}`)
	if code != http.StatusOK || out["status"] != "declined" || out["note"] != "Next month" || out["answered_by"] != "alice" {
		t.Fatalf("decline: %d %v", code, out)
	}
	if code, _ := call(member, http.MethodPost, "/v1/me/pack-requests/"+reqID+"/withdraw", ""); code != http.StatusConflict {
		t.Fatalf("withdrawn after it was declined: %d", code)
	}
	// The answer names the member who asked and the admin's note, so the
	// app tells that member alone (kiff/tafs#215, item 4).
	if last := alerts.events[len(alerts.events)-1]; last.Outcome != alert.PackRequestAnswered || last.Request == nil ||
		last.Request.AskedBy != "bob" || last.Request.Status != "declined" || last.Request.Note != "Next month" {
		t.Fatalf("the answer wasn't told: %+v", last)
	}
	_, out = call(member, http.MethodGet, "/v1/me/pack-requests", "")
	if items := out["items"].([]any); len(items) != 1 || items[0].(map[string]any)["status"] != "declined" {
		t.Fatalf("the member's list: %v", out)
	}

	// Asking again, then withdrawing.
	_, req = call(member, http.MethodPost, "/v1/me/pack-requests", `{"pack": "pico"}`)
	if code, out := call(member, http.MethodPost, "/v1/me/pack-requests/"+req["id"].(string)+"/withdraw", ""); code != http.StatusOK || out["status"] != "withdrawn" {
		t.Fatalf("withdraw: %d %v", code, out)
	}
}

// From the app: asking, reading and withdrawing are a phone's session's,
// unsigned; declining is signed.
func TestPackRequestsFromThePhone(t *testing.T) {
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "acme", Status: tenant.StatusActive, Metadata: map[string]any{tenant.MetadataKeyPlan: "solo"}}); err != nil {
		t.Fatal(err)
	}
	catalog, _ := onSale()
	mux := http.NewServeMux()
	NewPackRequestsHandler(tenants, packs.NewMemory(), catalog, nil).Register(mux)
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	p := newPhoneRig(t)
	memberPhone := p.on(packPerson(id, "bob", membership.RoleEditor))
	rr := serve(policed, memberPhone, http.MethodPost, "/v1/me/pack-requests", `{"pack": "pico"}`, nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("ask from the phone: %d %s", rr.Code, rr.Body.String())
	}
	var req packs.Request
	_ = json.Unmarshal(rr.Body.Bytes(), &req)
	if rr := serve(policed, memberPhone, http.MethodGet, "/v1/me/pack-requests", "", nil); rr.Code != http.StatusOK {
		t.Fatalf("read from the phone: %d", rr.Code)
	}
	adminPhone := p.on(packPerson(id, "alice", membership.RoleAdmin))
	path := "/v1/me/pack-requests/" + req.ID + "/decline"
	body := `{"note": "no"}`
	if rr := serve(policed, adminPhone, http.MethodPost, path, body, nil); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "signature_required") {
		t.Fatalf("unsigned decline: %d %s", rr.Code, rr.Body.String())
	}
	sig := map[string]string{auth.DeviceSignatureHeader: p.signedRequest(id, http.MethodPost, path, body, time.Now())}
	if rr := serve(policed, adminPhone, http.MethodPost, path, body, sig); rr.Code != http.StatusOK {
		t.Fatalf("signed decline: %d %s", rr.Code, rr.Body.String())
	}
}

// On Company a member asks an admin to raise the spend limit: a request
// with no pack, told to the admins and emailed. A pack is Solo's: on
// Company the member is told to ask for a raise, and a raise has no place
// on Solo. Someone who manages billing raises it themselves.
func TestRaiseRequests(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	tenants := tenant.NewMemoryStore()
	team, solo := tenant.NewID(), tenant.NewID()
	for id, plan := range map[string]string{team: "team", solo: "solo"} {
		if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: plan, Status: tenant.StatusActive, Metadata: map[string]any{tenant.MetadataKeyPlan: plan}}); err != nil {
			t.Fatal(err)
		}
	}
	catalog, _ := onSale()
	alerts := &alertsSeen{}
	mail := &mailSeen{sent: map[string]notify.AccountNotice{}, done: make(chan struct{})}
	people := fakeMembers{{Subject: "alice", Role: membership.RoleAdmin, Status: membership.StatusActive}}
	mux := http.NewServeMux()
	NewPackRequestsHandler(tenants, packs.NewMemory(), catalog, slog.Default()).WithAlerts(alerts).
		WithEmail(people, emailBook{"alice": "alice@example.test", "bob": "bob@example.test"}, mail, "https://app.kiff.dev").
		WithClock(func() time.Time { return now }).Register(mux)
	call := func(who auth.Identity, body string) (int, map[string]any) {
		t.Helper()
		rr := serve(mux, who, http.MethodPost, "/v1/me/pack-requests", body, nil)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	code, req := call(packPerson(team, "bob", membership.RoleEditor), `{"kind": "raise", "reason": "Month end"}`)
	if _, named := req["pack"]; code != http.StatusCreated || req["kind"] != "raise" || named || req["status"] != "open" {
		t.Fatalf("a raise: %d %v", code, req)
	}
	if len(alerts.events) != 1 || alerts.events[0].Request == nil || alerts.events[0].Request.Kind != "raise" || alerts.events[0].Request.Pack != "" {
		t.Fatalf("the alert: %+v", alerts.events)
	}
	select {
	case <-mail.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no admin was emailed")
	}
	mail.mu.Lock()
	if n := mail.sent["alice@example.test"]; !strings.Contains(n.Subject, "spend limit") || !strings.Contains(n.Body, "raise the spend limit") || !strings.Contains(n.Body, "Month end") {
		t.Fatalf("the email: %+v", n)
	}
	mail.mu.Unlock()
	for name, c := range map[string]struct {
		who     auth.Identity
		body    string
		want    int
		refusal string
	}{
		"an admin raises it":   {packPerson(team, "alice", membership.RoleAdmin), `{"kind": "raise"}`, http.StatusConflict, "you_can_raise"},
		"a pack on Company":    {packPerson(team, "carol", membership.RoleViewer), `{"pack": "pico"}`, http.StatusConflict, "ask_for_a_raise"},
		"a raise on Solo":      {packPerson(solo, "carol", membership.RoleViewer), `{"kind": "raise"}`, http.StatusConflict, "no_spend_limit"},
		"an unknown kind":      {packPerson(team, "carol", membership.RoleViewer), `{"kind": "gift"}`, http.StatusBadRequest, ""},
		"asking again at once": {packPerson(team, "bob", membership.RoleEditor), `{"kind": "raise"}`, http.StatusConflict, "already_asked"},
	} {
		if code, out := call(c.who, c.body); code != c.want || (c.refusal != "" && out["error"] != c.refusal) {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}

// onSale is a table whose packs pico and rayo are packs of credits, the
// ones on sale, beside forte, a pack of tasks no longer sold.
func onSale() (packs.Catalog, error) {
	return packs.ParseCatalog([]byte(`{"packs": [
		{"id": "pico", "name": "Pico", "credits": 500, "apple_product": "bot.tafs.credits.pico"},
		{"id": "rayo", "name": "Rayo", "credits": 1500, "apple_product": "bot.tafs.credits.rayo"},
		{"id": "forte", "name": "Forte", "tasks": 200, "apple_product": "bot.tafs.pack.forte"}]}`))
}
