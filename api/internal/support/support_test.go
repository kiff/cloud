package support_test

import (
	"context"
	"errors"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
)

// runStoreSuite exercises the Store contract against two tenant ids.
// Memory accepts any ids; Postgres needs real tenant rows (ULID ids),
// which the caller seeds, because the tenant_id foreign key requires
// them.
func runStoreSuite(t *testing.T, store support.Store, tenantA, tenantB string) {
	ctx := context.Background()
	now := time.Now().UTC()

	tk := support.Ticket{
		ID: "tk-1", TenantID: tenantA, AuthorSubject: "alice", AuthorEmail: "alice@example.com",
		Title: "Refund flow confusing", Body: "The activate step isn't clear.",
		Category: "bug", Status: support.StatusOpen, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateTicket(ctx, tk); err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	// GetForTenant scopes: tenantA sees it, tenantB does not.
	if got, err := store.GetForTenant(ctx, tenantA, "tk-1"); err != nil || got.Title != tk.Title {
		t.Fatalf("GetForTenant tenantA: got %+v err=%v", got, err)
	}
	if _, err := store.GetForTenant(ctx, tenantB, "tk-1"); !errors.Is(err, support.ErrNotFound) {
		t.Fatalf("GetForTenant tenantB: got %v, want ErrNotFound (cross-tenant)", err)
	}

	// Operator GetByID crosses tenants.
	if _, err := store.GetByID(ctx, "tk-1"); err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if _, err := store.GetByID(ctx, "nope"); !errors.Is(err, support.ErrNotFound) {
		t.Fatalf("GetByID unknown: got %v, want ErrNotFound", err)
	}

	// Thread: user then operator reply, oldest first.
	if err := store.AddMessage(ctx, support.Message{
		ID: "m1", TicketID: "tk-1", AuthorKind: support.AuthorUser, AuthorSubject: "alice",
		Body: "any update?", CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("AddMessage user: %v", err)
	}
	if err := store.AddMessage(ctx, support.Message{
		ID: "m2", TicketID: "tk-1", AuthorKind: support.AuthorOperator, AuthorSubject: "ops",
		Body: "looking into it", CreatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("AddMessage operator: %v", err)
	}
	msgs, err := store.ListMessages(ctx, "tk-1")
	if err != nil || len(msgs) != 2 {
		t.Fatalf("ListMessages: got %d err=%v, want 2", len(msgs), err)
	}
	if msgs[0].ID != "m1" || msgs[1].ID != "m2" {
		t.Errorf("ListMessages order = [%s,%s], want [m1,m2]", msgs[0].ID, msgs[1].ID)
	}
	// AddMessage to an unknown ticket is ErrNotFound.
	if err := store.AddMessage(ctx, support.Message{ID: "mX", TicketID: "ghost", AuthorKind: support.AuthorUser, Body: "x", CreatedAt: now}); !errors.Is(err, support.ErrNotFound) {
		t.Errorf("AddMessage to ghost: got %v, want ErrNotFound", err)
	}

	// SetStatus.
	if err := store.SetStatus(ctx, "tk-1", support.StatusAnswered); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got, _ := store.GetByID(ctx, "tk-1"); got.Status != support.StatusAnswered {
		t.Errorf("status = %q, want answered", got.Status)
	}
	if err := store.SetStatus(ctx, "ghost", support.StatusClosed); !errors.Is(err, support.ErrNotFound) {
		t.Errorf("SetStatus ghost: got %v, want ErrNotFound", err)
	}

	// A second tenant's ticket, for list scoping + ListAll filter.
	if err := store.CreateTicket(ctx, support.Ticket{
		ID: "tk-2", TenantID: tenantB, Title: "Deal close question", Body: "?",
		Category: "general", Status: support.StatusOpen, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateTicket tk-2: %v", err)
	}
	if list, _ := store.ListForTenant(ctx, tenantA); len(list) != 1 || list[0].ID != "tk-1" {
		t.Errorf("ListForTenant tenantA = %+v, want [tk-1]", list)
	}
	if all, _ := store.ListAll(ctx, ""); len(all) != 2 {
		t.Errorf("ListAll = %d, want 2", len(all))
	}
	if open, _ := store.ListAll(ctx, support.StatusOpen); len(open) != 1 || open[0].ID != "tk-2" {
		t.Errorf("ListAll(open) = %+v, want [tk-2]", open)
	}
}

func TestMemoryStore(t *testing.T) {
	t.Parallel()
	runStoreSuite(t, support.NewMemoryStore(), "t1", "t2")
}

func TestPostgresStore(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := support.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// Clean slate.
	if _, err := pool.Exec(ctx, "TRUNCATE cloud_meta.support_ticket_messages, cloud_meta.support_tickets, cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "TRUNCATE cloud_meta.support_ticket_messages, cloud_meta.support_tickets, cloud_meta.tenants CASCADE")
	})

	store := tenant.NewPostgresStore(pool)
	seed := func() string {
		id := tenant.NewID()
		tt := tenant.Tenant{ID: id, Slug: "s-" + id, DisplayName: id, Status: tenant.StatusProvisioning}
		if err := store.Create(ctx, tt); err != nil {
			t.Fatalf("seed tenant %s: %v", id, err)
		}
		now := time.Now().UTC()
		if err := store.SetStatus(ctx, id, tenant.StatusActive, &now); err != nil {
			t.Fatalf("activate tenant %s: %v", id, err)
		}
		return id
	}
	runStoreSuite(t, support.NewPostgresStore(pool), seed(), seed())
}
