package appsignin_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/appsignin"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

// A consent is kept with KIFF's time, and erased with its account.
func TestPostgresConsentsKeepAndEraseWithTheAccount(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Twice: the schema is idempotent.
	for range 2 {
		if err := appsignin.ApplyConsentsSchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	id := tenant.NewID()
	if err := tenant.NewPostgresStore(pool).Create(ctx, tenant.Tenant{ID: id, Slug: "slug-" + id, DisplayName: "T", Status: tenant.StatusActive, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	if err := appsignin.NewPostgresConsents(pool).Record(ctx, appsignin.Consent{Tenant: id, Subject: "user_ana", TermsVersion: "2026-11-14",
		Client: appsignin.ClientID, DeviceID: "dev_1", SessionID: "ses_1", AcceptedAt: at}); err != nil {
		t.Fatal(err)
	}
	var version, client, device, session string
	var got time.Time
	if err := pool.QueryRow(ctx, `SELECT terms_version, client, device_id, session_id, accepted_at FROM cloud_meta.app_terms_consents
		WHERE tenant_id = $1 AND subject = 'user_ana'`, id).Scan(&version, &client, &device, &session, &got); err != nil {
		t.Fatal(err)
	}
	if version != "2026-11-14" || client != appsignin.ClientID || device != "dev_1" || session != "ses_1" || !got.Equal(at) {
		t.Fatalf("kept: %s %s %s %s %v", version, client, device, session, got)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM cloud_meta.tenants WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.app_terms_consents WHERE tenant_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("after the account is erased: %d %v", n, err)
	}
	// An unknown account is refused.
	if err := appsignin.NewPostgresConsents(pool).Record(ctx, appsignin.Consent{Tenant: "t_none", Subject: "s", TermsVersion: "v", Client: "c", SessionID: "x", AcceptedAt: at}); err == nil {
		t.Fatal("a consent for no account was kept")
	}
}
