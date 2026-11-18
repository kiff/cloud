package newdevice

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

func requirePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test (run docker compose up -d first)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := ApplySchema(ctx, pool); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE cloud_meta.known_devices, cloud_meta.new_device_emails`); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Invariant: KIFF remembers a person's devices only as a hash of the sign-in
// provider's client id, never the id, an address or a place, and forgets a
// device unseen for 180 days and a waiting new-device email within minutes.
// Category: privacy
// Attack: Read the known-devices table for a client id, an IP or a city
// Refused: the table holds the subject, sha256 of the client id and two times; the waiting email holds no body and expires within the device-approval window
func TestPostgresKeepsNothingReadableAndForgets(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	s := NewPostgres(pool)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	laptop := SignIn{SessionID: "sess_1", Subject: "user_ana", ClientHash: Hash("client_laptop"), NewAccount: true, At: now}

	// An account's first sign-in is recorded, not held; a second device is
	// held; the same again isn't.
	if isNew, err := s.Seen(ctx, laptop, now); err != nil || isNew {
		t.Fatalf("first device: %v %v", isNew, err)
	}
	phone := laptop
	phone.SessionID, phone.ClientHash, phone.NewAccount = "sess_2", Hash("client_phone"), false
	if isNew, err := s.Seen(ctx, phone, now); err != nil || !isNew {
		t.Fatalf("second device: %v %v", isNew, err)
	}
	again := phone
	again.SessionID = "sess_3"
	if isNew, err := s.Seen(ctx, again, now.Add(time.Minute)); err != nil || isNew {
		t.Fatalf("the same device again: %v %v", isNew, err)
	}
	// No device on record and an older account: held, the device may not
	// be theirs.
	if isNew, err := s.Seen(ctx, SignIn{SessionID: "sess_b", Subject: "user_bea", ClientHash: Hash("client_b"), At: now}, now); err != nil || !isNew {
		t.Fatalf("an older account's first device on record: %v %v", isNew, err)
	}
	// The held email keeps no device and no place.
	var described string
	if err := pool.QueryRow(ctx, `SELECT device || browser || city || country FROM cloud_meta.new_device_emails WHERE session_id = 'sess_2'`).Scan(&described); err != nil || described != "" {
		t.Fatalf("a held email keeps %q %v", described, err)
	}

	// Nothing readable in the devices: the hash, never the id.
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT client_hash FROM cloud_meta.known_devices WHERE subject = 'user_ana' ORDER BY first_seen, client_hash LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 32 || bytes.Contains(raw, []byte("client_")) {
		t.Fatalf("a device is kept as %x", raw)
	}
	var cols int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'cloud_meta' AND table_name = 'known_devices'`).Scan(&cols); err != nil || cols != 4 {
		t.Fatalf("known_devices has %d columns, want subject, client_hash, first_seen, last_seen", cols)
	}

	// A tafs approval marks the held email; another person's marks nothing.
	if err := s.Through(ctx, "sess_2", "user_bob", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Through(ctx, "sess_2", "user_ana", now); err != nil {
		t.Fatal(err)
	}
	// Not due before the hold; due once, after it.
	if due, err := s.TakeDue(ctx, now.Add(Hold-time.Second)); err != nil || len(due) != 0 {
		t.Fatalf("before the hold: %v %v", due, err)
	}
	due, err := s.TakeDue(ctx, now.Add(Hold))
	if len(due) == 2 && due[0].SessionID == "sess_b" {
		due = due[1:]
	} else if len(due) == 2 {
		due = due[:1]
	}
	if err != nil || len(due) != 1 || !due[0].Tafs || !due[0].At.Equal(now) {
		t.Fatalf("due: %+v %v", due, err)
	}
	if again, _ := s.TakeDue(ctx, now.Add(Hold)); len(again) != 0 {
		t.Fatal("an email was taken twice")
	}
	// A failed send is put back, due a minute later, with its expiry kept;
	// put back past the window, it's dropped.
	if !due[0].Until.Equal(now.Add(Window)) {
		t.Fatalf("until: %v", due[0].Until)
	}
	if err := s.Again(ctx, due[0], now.Add(Hold+RetryAfter)); err != nil {
		t.Fatal(err)
	}
	if back, err := s.TakeDue(ctx, now.Add(Hold+RetryAfter)); err != nil || len(back) != 1 || !back[0].At.Equal(now) || !back[0].Tafs || !back[0].Until.Equal(due[0].Until) {
		t.Fatalf("put back: %+v %v", back, err)
	}
	if err := s.Again(ctx, due[0], now.Add(Window+time.Second)); err != nil {
		t.Fatal(err)
	}

	// A marker no sign-in followed goes after the window.
	if err := s.Through(ctx, "sess_x", "user_ana", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeDue(ctx, now.Add(Window+time.Second)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.new_device_emails`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("waiting rows after the window: %d %v", left, err)
	}
	// A device unseen for 180 days is forgotten.
	if _, err := s.TakeDue(ctx, now.Add(Unseen+2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.known_devices`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("devices after 180 days unseen: %d %v", left, err)
	}
}
