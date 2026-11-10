package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
)

// Invariant: a device sign-in waiting for approval is kept across a deploy,
// and the kept row holds no secret: the device code only as its sha256,
// and never the session an approval mints.
// Category: privacy
// Attack: read cloud_meta.device_sign_ins for a device code or a session token
// Refused: the table has no column for either; a row holds the code's hash, the short user code, the device's public keys and name, and two times
func TestPostgresWaitingSignInsKeepNoSecretAndExpire(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if err := auth.ApplyDeviceSignInsSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE cloud_meta.device_sign_ins`); err != nil {
		t.Fatal(err)
	}
	s := auth.NewPostgresWaitingSignIns(pool)
	now := time.Date(2026, 11, 10, 16, 0, 0, 0, time.UTC)
	w := auth.WaitingSignIn{DeviceHash: "h1", UserCode: "BCDF-GHJK", DeviceKey: []byte{1, 2}, ApprovalKey: []byte{3}, DeviceName: "Pixel",
		Platform: "android", PlatformSent: true, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	if err := s.Put(ctx, w); err != nil {
		t.Fatal(err)
	}
	// Found by either code, as it was kept.
	for name, get := range map[string]func() (*auth.WaitingSignIn, error){
		"hash": func() (*auth.WaitingSignIn, error) { return s.ByHash(ctx, "h1", now.Add(time.Minute)) },
		"code": func() (*auth.WaitingSignIn, error) { return s.ByUserCode(ctx, "BCDF-GHJK", now.Add(time.Minute)) },
	} {
		got, err := get()
		if err != nil || got == nil || got.DeviceName != "Pixel" || got.Platform != "android" || !got.PlatformSent || string(got.DeviceKey) != "\x01\x02" ||
			!got.ExpiresAt.Equal(w.ExpiresAt) {
			t.Fatalf("by %s: %+v %v", name, got, err)
		}
	}
	// A second sign-in can't take a waiting user code.
	if err := s.Put(ctx, auth.WaitingSignIn{DeviceHash: "h2", UserCode: "BCDF-GHJK", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}); !errors.Is(err, auth.ErrUserCodeTaken) {
		t.Fatalf("a taken user code: %v", err)
	}
	// Nothing in it that signs anyone in.
	var cols []string
	rows, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'cloud_meta' AND table_name = 'device_sign_ins' ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		cols = append(cols, c)
	}
	rows.Close()
	for _, c := range cols {
		if c == "device_code" || c == "token" || c == "access_token" || c == "session_token" || c == "token_once" {
			t.Fatalf("device_sign_ins keeps a secret in %s: %v", c, cols)
		}
	}
	// Expired: not found, and swept.
	if got, err := s.ByHash(ctx, "h1", now.Add(11*time.Minute)); err != nil || got != nil {
		t.Fatalf("an expired sign-in was found: %+v %v", got, err)
	}
	if n, err := s.Sweep(ctx, now.Add(11*time.Minute)); err != nil || n != 0 {
		t.Fatalf("after the sweep: %d %v", n, err)
	}
	// A re-link is one at a time; deleting one leaves the others.
	for _, r := range []auth.WaitingSignIn{
		{DeviceHash: "r1", UserCode: "AAAA-AAAA", Subject: "alice", Tenant: "t1", RelinkSession: "dsess-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{DeviceHash: "r2", UserCode: "BBBB-BBBB", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	} {
		if err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DropRelinks(ctx, "dsess-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ByHash(ctx, "r1", now); got != nil {
		t.Fatal("a replaced re-link was kept")
	}
	if err := s.Delete(ctx, "r2"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Sweep(ctx, now); n != 0 {
		t.Fatalf("left after deletes: %d", n)
	}
}
