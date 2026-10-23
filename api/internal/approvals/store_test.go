package approvals_test

import (
	"context"
	"errors"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"
	kiffpostgres "github.com/kiff/kiff/pkg/kiff/store/postgres"

	"github.com/kiff/cloud/api/internal/approvals"
	"github.com/kiff/cloud/api/internal/auth"
)

const (
	domainA = "orders-refunds"
	domainB = "orders-fulfilment"
)

// newSchema gives each test its own Postgres schema, so tests can run in
// parallel against one database and a failure leaves inspectable state
// under a name tied to the test.
func newSchema(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()

	schema := "t_" + sanitize(t.Name())
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.MaxConns = 4
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET search_path TO "+schema+", public")
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open scoped pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// The framework owns kiff_approvals; the cloud adds the domain
	// dimension on top. Applying both here mirrors provisioning.
	if _, err := pool.Exec(ctx, kiffpostgres.SchemaSQL()); err != nil {
		t.Fatalf("apply framework schema: %v", err)
	}
	mustTx(t, pool, func(tx pgx.Tx) error { return approvals.ApplySchema(ctx, tx) })
	return pool
}

func sanitize(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 50 {
		out = out[:50]
	}
	return string(out)
}

func mustTx(t *testing.T, pool *pgxpool.Pool, fn func(pgx.Tx) error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("tx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func pending(id, entityID, action string) kiffapproval.Approval {
	return kiffapproval.Approval{
		ID:          id,
		EntityID:    entityID,
		EntityType:  "Order",
		ActionName:  action,
		RequestedBy: "key-requester",
		Status:      kiffapproval.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
}

func granted(id, entityID, action string) kiffapproval.Approval {
	a := pending(id, entityID, action)
	a.Status = kiffapproval.StatusGranted
	a.ReviewedBy = "key-reviewer"
	a.ReviewedAt = time.Now().UTC()
	return a
}

func storeFor(t *testing.T, pool *pgxpool.Pool, domainID string) *approvals.DomainScopedStore {
	t.Helper()
	s, err := approvals.NewDomainScoped(pool, domainID)
	if err != nil {
		t.Fatalf("new store %s: %v", domainID, err)
	}
	return s
}

// A store must refuse to be built without a domain: an empty domain
// would silently scope to the legacy partition rather than to
// everything, which is a confusing failure rather than a safe default.
func TestNewDomainScoped_RejectsEmptyDomain(t *testing.T) {
	t.Parallel()
	if _, err := approvals.NewDomainScoped(&pgxpool.Pool{}, ""); err == nil {
		t.Fatal("expected an error for an empty domain id")
	}
}

// Get and List must not cross a domain boundary. Another domain's
// approval reads as absent rather than as an error: its existence is
// itself information.
func TestCrossDomain_GetAndListAreIsolated(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()
	a, b := storeFor(t, pool, domainA), storeFor(t, pool, domainB)

	if err := a.Save(ctx, pending("ap-1", "order-1", "REFUND")); err != nil {
		t.Fatalf("save into A: %v", err)
	}

	if _, ok, err := a.Get(ctx, "ap-1"); err != nil || !ok {
		t.Fatalf("A should see its own approval: ok=%v err=%v", ok, err)
	}
	if _, ok, err := b.Get(ctx, "ap-1"); err != nil || ok {
		t.Fatalf("B must not see A's approval: ok=%v err=%v", ok, err)
	}

	got, err := b.List(ctx, "")
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("B listed %d approvals, want 0", len(got))
	}
}

// The authorization fix: an approval granted under one domain must not
// satisfy the same (entity_id, action_name) under another. This is
// reachable precisely because generic action names repeat across
// domains, which is a supported pattern.
func TestCrossDomain_IsGrantedDoesNotLeakAcrossDomains(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()
	a, b := storeFor(t, pool, domainA), storeFor(t, pool, domainB)

	if err := a.Save(ctx, granted("ap-shared", "order-9", "APPROVE")); err != nil {
		t.Fatalf("save granted into A: %v", err)
	}

	ok, err := a.IsGranted(ctx, "ap-shared", "order-9", "APPROVE")
	if err != nil || !ok {
		t.Fatalf("A must honour its own grant: ok=%v err=%v", ok, err)
	}

	ok, err = b.IsGranted(ctx, "ap-shared", "order-9", "APPROVE")
	if err != nil {
		t.Fatalf("B IsGranted: %v", err)
	}
	if ok {
		t.Fatal("A's grant authorized the same action under B — cross-domain approval reuse")
	}
}

// Save is an upsert, because a review is persisted by re-saving the same
// id with a new status. The conflict branch is therefore a boundary too:
// a domain must not be able to overwrite another domain's approval by
// reusing its id, and the attempt must be reported rather than silently
// affecting no rows.
func TestCrossDomain_UpsertIsGuarded(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()
	a, b := storeFor(t, pool, domainA), storeFor(t, pool, domainB)

	if err := a.Save(ctx, pending("ap-2", "order-2", "REFUND")); err != nil {
		t.Fatalf("save into A: %v", err)
	}

	err := b.Save(ctx, granted("ap-2", "order-2", "REFUND"))
	if !errors.Is(err, approvals.ErrCrossDomain) {
		t.Fatalf("B overwriting A's approval: err = %v, want ErrCrossDomain", err)
	}

	// A's row must be untouched — still pending, still A's.
	got, ok, err := a.Get(ctx, "ap-2")
	if err != nil || !ok {
		t.Fatalf("re-read A: ok=%v err=%v", ok, err)
	}
	if got.Status != kiffapproval.StatusPending {
		t.Fatalf("status = %q, want pending (the guarded upsert must not have applied)", got.Status)
	}
}

// A domain may review its own approval: the same guard must not block
// the legitimate update path.
func TestSameDomain_UpsertReviewSucceeds(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()
	a := storeFor(t, pool, domainA)

	if err := a.Save(ctx, pending("ap-3", "order-3", "REFUND")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := a.Save(ctx, granted("ap-3", "order-3", "REFUND")); err != nil {
		t.Fatalf("review save: %v", err)
	}
	got, ok, err := a.Get(ctx, "ap-3")
	if err != nil || !ok {
		t.Fatalf("re-read: ok=%v err=%v", ok, err)
	}
	if got.Status != kiffapproval.StatusGranted || got.ReviewedBy != "key-reviewer" {
		t.Fatalf("review did not persist: %+v", got)
	}
}

// Legacy rows carry no domain. They must be invisible to every scoped
// store — fail closed — and visible to the management reader, which is
// the only surface that can honestly present them as unattributed.
func TestLegacyRows_ScopedInvisibleManagementVisible(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()

	// Write a row the way the framework store would: no domain_id, so
	// it takes the column default.
	if _, err := pool.Exec(ctx, `
		INSERT INTO kiff_approvals (id, entity_id, entity_type, action_name,
		                            requested_by, status, created_at)
		VALUES ('ap-legacy', 'order-legacy', 'Order', 'REFUND', 'someone', 'pending', NOW())
	`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	scoped := storeFor(t, pool, domainA)
	if _, ok, err := scoped.Get(ctx, "ap-legacy"); err != nil || ok {
		t.Fatalf("legacy row must be invisible to a scoped store: ok=%v err=%v", ok, err)
	}
	if ok, err := scoped.IsGranted(ctx, "ap-legacy", "order-legacy", "REFUND"); err != nil || ok {
		t.Fatalf("legacy row must not authorize: ok=%v err=%v", ok, err)
	}

	all, err := approvals.ListAllForTenant(ctx, poolRunner{pool}, "ignored", "")
	if err != nil {
		t.Fatalf("management list: %v", err)
	}
	if len(all) != 1 || all[0].ID != "ap-legacy" {
		t.Fatalf("management view = %+v, want the legacy row", all)
	}
	if all[0].DomainID != approvals.LegacyDomainID {
		t.Fatalf("domain = %q, want the legacy sentinel", all[0].DomainID)
	}
}

// The management reader spans domains, which is the whole reason it
// exists separately from the runtime's scoped store.
func TestTenantReader_SpansDomains(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()
	a, b := storeFor(t, pool, domainA), storeFor(t, pool, domainB)

	if err := a.Save(ctx, pending("ap-a", "order-a", "REFUND")); err != nil {
		t.Fatalf("save A: %v", err)
	}
	if err := b.Save(ctx, pending("ap-b", "order-b", "SHIP")); err != nil {
		t.Fatalf("save B: %v", err)
	}

	all, err := approvals.ListAllForTenant(ctx, poolRunner{pool}, "ignored", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("management view has %d rows, want 2", len(all))
	}
	byID := map[string]string{}
	for _, a := range all {
		byID[a.ID] = a.DomainID
	}
	if byID["ap-a"] != domainA || byID["ap-b"] != domainB {
		t.Fatalf("domains not annotated correctly: %v", byID)
	}
}

// requireURL returns the test database URL or skips.
func requireURL(t *testing.T) string {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	return url
}

// frameworkOnlySchema builds a schema carrying the framework's tables
// but not the cloud's domain migration, so a test can observe what
// applying that migration does — and what rolling it back undoes.
func frameworkOnlySchema(t *testing.T, url string) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	schema := "t_" + sanitize(t.Name())

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg.MaxConns = 2
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET search_path TO "+schema+", public")
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, kiffpostgres.SchemaSQL()); err != nil {
		t.Fatalf("framework schema: %v", err)
	}
	return pool, schema
}

// poolRunner adapts a search_path-pinned pool to the TenantConnRunner
// the management reader takes. Production passes tenant.SchemaPool,
// which pins the path per call; here the pool is already pinned to the
// test's schema, so the tenant id is unused.
type poolRunner struct{ pool *pgxpool.Pool }

func (p poolRunner) WithTenantConn(ctx context.Context, _ string, fn func(pgx.Tx) error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func withKey(keyID string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "owner", Method: auth.MethodAPIKey, APIKey: true,
		Metadata: map[string]string{"api_key_id": keyID},
	})
}

// The store records which key asked for an approval, with the approval,
// so that key can be refused its review. A review saved by another key
// does not overwrite it, a person's request records none, and another
// domain cannot read it.
func TestRequesterKey_RecordedWithTheApproval(t *testing.T) {
	pool := newSchema(t)
	a := storeFor(t, pool, domainA)
	b := storeFor(t, pool, domainB)

	if err := a.Save(withKey("key-asker"), pending("appr-key", "order-1", "ISSUE_REFUND")); err != nil {
		t.Fatal(err)
	}
	if got, err := a.RequesterKey(context.Background(), "appr-key"); err != nil || got != "key-asker" {
		t.Fatalf("requester = %q, %v; want key-asker", got, err)
	}
	// Saved again as pending by another key (a second request for the
	// same id), then reviewed by a third: the first asker is kept.
	if err := a.Save(withKey("key-second"), pending("appr-key", "order-1", "ISSUE_REFUND")); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(withKey("key-reviewer"), granted("appr-key", "order-1", "ISSUE_REFUND")); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.RequesterKey(context.Background(), "appr-key"); got != "key-asker" {
		t.Errorf("requester after a re-request and a review = %q, want key-asker", got)
	}
	if got, err := b.RequesterKey(context.Background(), "appr-key"); err != nil || got != "" {
		t.Errorf("another domain read the requester: %q, %v", got, err)
	}

	session := auth.WithIdentity(context.Background(), auth.Identity{Subject: "owner", Method: auth.MethodSession})
	if err := a.Save(session, pending("appr-person", "order-2", "ISSUE_REFUND")); err != nil {
		t.Fatal(err)
	}
	if got, err := a.RequesterKey(context.Background(), "appr-person"); err != nil || got != "" {
		t.Errorf("a person's request recorded key %q, %v", got, err)
	}

	// A cross-domain write records nothing either.
	if err := b.Save(withKey("key-intruder"), pending("appr-person", "order-2", "ISSUE_REFUND")); !errors.Is(err, approvals.ErrCrossDomain) {
		t.Fatalf("cross-domain save: %v", err)
	}
	if got, _ := a.RequesterKey(context.Background(), "appr-person"); got != "" {
		t.Errorf("a refused cross-domain save recorded key %q", got)
	}
}

// A tenant provisioned before the requesters table gets it from
// ApplyRequesterSchema, which the resolver runs when it opens the
// tenant's pool, and it is safe to run again.
func TestApplyRequesterSchema_CreatesTheTableOnFirstUse(t *testing.T) {
	pool := newSchema(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "DROP TABLE cloud_approval_requesters"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := approvals.ApplyRequesterSchema(ctx, pool); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}
	a := storeFor(t, pool, domainA)
	if err := a.Save(withKey("key-asker"), pending("appr-1", "order-1", "ISSUE_REFUND")); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.RequesterKey(ctx, "appr-1"); got != "key-asker" {
		t.Errorf("requester = %q", got)
	}
}

// A tenant schema without kiff_approvals is left alone rather than
// failing: the resolver runs this when it opens any tenant's pool, so an
// error would fail every request for that tenant.
func TestApplyRequesterSchema_SkipsASchemaWithoutApprovals(t *testing.T) {
	pool := newSchema(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "DROP TABLE cloud_approval_requesters, kiff_approvals"); err != nil {
		t.Fatal(err)
	}
	if err := approvals.ApplyRequesterSchema(ctx, pool); err != nil {
		t.Fatalf("apply on a schema without kiff_approvals: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('cloud_approval_requesters') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Errorf("requesters table created without kiff_approvals: %v, %v", exists, err)
	}
}
