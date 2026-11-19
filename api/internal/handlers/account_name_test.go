package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

type nameEmails map[string]string

func (e nameEmails) EmailForSubject(_ context.Context, subject string) (string, error) {
	if v, ok := e[subject]; ok {
		return v, nil
	}
	return "", errors.New("no such person")
}

func TestAccountNamePermissionsAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     auth.Identity
		body   string
		status int
		want   string
	}{
		{"owner", owner(), `{"name":"  Kiff Agents  "}`, 200, "Kiff Agents"},
		{"admin", auth.Identity{Tenant: "t1", Roles: []string{"admin"}}, `{"name":"Acme"}`, 200, "Acme"},
		{"viewer", auth.Identity{Tenant: "t1", MemberRole: "viewer"}, `{"name":"Acme"}`, 403, ""},
		{"runtime", runtimeKey(), `{"name":"Acme"}`, 403, ""},
		{"bound admin", auth.Identity{Tenant: "t1", AgentID: "agent", MemberRole: "admin", Roles: []string{"admin"}}, `{"name":"Acme"}`, 403, ""},
		{"empty", owner(), `{"name":"   "}`, 400, ""},
		{"too long", owner(), `{"name":"` + strings.Repeat("a", 81) + `"}`, 400, ""},
		{"two lines", owner(), `{"name":"Acme\nLtd"}`, 400, ""},
		{"extra object", owner(), `{"name":"Acme"} {}`, 400, ""},
		{"unknown field", owner(), `{"name":"Acme","slug":"acme"}`, 400, ""},
		{"unauthenticated", auth.Identity{}, `{"name":"Acme"}`, 401, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tenant.NewMemoryStore()
			tenantID, otherID := tenant.NewID(), tenant.NewID()
			if tc.id.Tenant != "" {
				tc.id.Tenant = tenantID
			}
			for _, id := range []string{tenantID, otherID} {
				if err := store.Create(context.Background(), tenant.Tenant{ID: id, Slug: "n-" + id, DisplayName: "Before", Status: tenant.StatusActive}); err != nil {
					t.Fatal(err)
				}
			}
			mux := http.NewServeMux()
			NewMeHandler(store).Register(mux)
			r := httptest.NewRequest(http.MethodPut, "/v1/me/account-name", strings.NewReader(tc.body))
			r = r.WithContext(auth.WithIdentity(r.Context(), tc.id))
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, r)
			if rr.Code != tc.status {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			a, _ := store.Get(context.Background(), tenantID)
			b, _ := store.Get(context.Background(), otherID)
			if name, _ := tenant.NameFor(b); name != "Before" {
				t.Fatalf("changed another account: %q", name)
			}
			want := tc.want
			if want == "" {
				want = "Before"
			}
			if name, _ := tenant.NameFor(a); name != want {
				t.Fatalf("name %q, want %q", name, want)
			}
		})
	}
}

// The account made at a first sign-in is named by that person's email, not
// their sign-in id, until an admin names it: for its owner, for
// anyone else on it, and once named, by its name.
func TestMeNamesTheAccountForPeople(t *testing.T) {
	ctx := context.Background()
	store := tenant.NewMemoryStore()
	subject := "user_ana"
	acct, err := tenant.EnsureForSubject(ctx, store, nopProvisioner{}, tenant.NewMemorySubjectStore(), subject)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewMeHandler(store, WithMeEmails(nameEmails{subject: "ana@example.com"}, nil)).Register(mux)
	me := func(id auth.Identity) string {
		t.Helper()
		id.Tenant = acct.ID
		r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, r)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		var got MeResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.DisplayName
	}

	// The owner, signed in on the web with their email: no lookup needed.
	if got := me(auth.Identity{Subject: subject, Email: "ana@web.example", MemberRole: "admin"}); got != "ana@web.example" {
		t.Fatalf("owner sees %q, want their email", got)
	}
	// Someone else on the account never sees the owner's email (the shared-tafs design
	// decision 12): an account nobody named is "Unnamed account" to them.
	if got := me(auth.Identity{Subject: "user_other", Email: "leo@example.com", MemberRole: "viewer"}); got != Unnamed || strings.Contains(got, "ana") {
		t.Fatalf("a viewer sees %q, want %q", got, Unnamed)
	}
	if err := store.SetMetadata(ctx, acct.ID, tenant.MetadataKeyAccountName, "Kiff Agents"); err != nil {
		t.Fatal(err)
	}
	if got := me(auth.Identity{Subject: "user_other", MemberRole: "viewer"}); got != "Kiff Agents" {
		t.Fatalf("named: %q, want Kiff Agents", got)
	}
}

func TestAccountLabelNeverAnID(t *testing.T) {
	ctx := context.Background()
	store := tenant.NewMemoryStore()
	subject := "user_ana"
	acct, err := tenant.EnsureForSubject(ctx, store, nopProvisioner{}, tenant.NewMemorySubjectStore(), subject)
	if err != nil {
		t.Fatal(err)
	}
	if got := accountLabel(ctx, acct, nameEmails{subject: "ana@example.com"}, subject); got != "ana@example.com" {
		t.Fatalf("label %q, want the owner's own email", got)
	}
	// No email to be had: the slug, still not the sign-in id.
	if got := accountLabel(ctx, acct, nameEmails{}, subject); got != acct.Slug || strings.Contains(got, subject) {
		t.Fatalf("label %q, want the slug %q", got, acct.Slug)
	}
	// Anyone else, or no one in particular (a notice): never the owner's email.
	for _, viewer := range []string{"user_other", ""} {
		if got := accountLabel(ctx, acct, nameEmails{subject: "ana@example.com"}, viewer); got != Unnamed {
			t.Fatalf("%q sees %q, want %q", viewer, got, Unnamed)
		}
	}
}

type nopProvisioner struct{}

func (nopProvisioner) Provision(context.Context, string) error   { return nil }
func (nopProvisioner) Deprovision(context.Context, string) error { return nil }
