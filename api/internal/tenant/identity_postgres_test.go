package tenant_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
)

type fakeEraser struct {
	erased []string
	fail   error
}

func (f *fakeEraser) EraseIdentity(_ context.Context, subject string) error {
	if f.fail != nil {
		return f.fail
	}
	f.erased = append(f.erased, subject)
	return nil
}

// Erasing an account deletes the sign-in of each person it leaves with no
// account, and no one else's. A sign-in that can't be deleted stops
// the erasure, so the next sweep tries again.
func TestDeprovisionErasesTheSignInsItLeavesWithNoAccount(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	dropTenantSchemas(t, pool)
	ctx := context.Background()
	defer func() { dropTenantSchemas(t, pool); truncateTenants(t, pool) }()
	if err := tenant.ApplySubjectTenantsSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := membership.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := tenant.NewPostgresStore(pool)
	a, b := sampleTenant("e328-a"), sampleTenant("e328-b")
	for _, tt := range []tenant.Tenant{a, b} {
		if err := store.Create(ctx, tt); err != nil {
			t.Fatal(err)
		}
		if err := tenant.NewProvisioner(pool, store).Provision(ctx, tt.ID); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// alice: A's founder, nowhere else. carol: A's member and still B's.
	// dave: A's member, removed from B. erin: B's only.
	exec(`INSERT INTO cloud_meta.subject_tenants (subject, tenant_id) VALUES ('user_alice', $1), ('user_erin', $2)`, a.ID, b.ID)
	member := func(id, tenantID, subject, status string) {
		exec(`INSERT INTO cloud_meta.tenant_memberships (id, tenant_id, subject, role, status) VALUES ($1, $2, $3, 'editor', $4)`, id, tenantID, subject, status)
	}
	member("m1", a.ID, "user_alice", "active")
	member("m2", a.ID, "user_carol", "active")
	member("m3", b.ID, "user_carol", "active")
	member("m4", a.ID, "user_dave", "active")
	member("m5", b.ID, "user_dave", "removed")

	failing := &fakeEraser{fail: errors.New("clerk down")}
	if err := tenant.NewProvisioner(pool, store).WithIdentityEraser(failing).Deprovision(ctx, a.ID); err == nil {
		t.Fatal("a sign-in that can't be deleted must stop the erasure")
	}
	if got, err := store.Get(ctx, a.ID); err != nil || got.Status == tenant.StatusDeleted {
		t.Fatalf("the account was erased anyway: %v %v", got.Status, err)
	}

	e := &fakeEraser{}
	if err := tenant.NewProvisioner(pool, store).WithIdentityEraser(e).Deprovision(ctx, a.ID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if want := []string{"user_alice", "user_dave"}; !reflect.DeepEqual(e.erased, want) {
		t.Fatalf("erased sign-ins %v, want %v (carol is still B's, erin was never A's)", e.erased, want)
	}
}
