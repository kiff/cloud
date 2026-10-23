// Package testdb gives each gateway test package its own Postgres
// database, derived from KIFF_TEST_POSTGRES_URL, as apps/api's testdb
// does: packages run in parallel and must not share tables.
package testdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvVar names the base database URL. Unset skips Postgres tests.
const EnvVar = "KIFF_TEST_POSTGRES_URL"

var (
	once    sync.Once
	pkgURL  string
	initErr error
)

// Pool returns a pool on this package's fresh database, or skips the
// test when EnvVar is unset.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv(EnvVar)
	if base == "" {
		t.Skip(EnvVar + " not set; skipping Postgres-backed test")
	}
	once.Do(func() { pkgURL, initErr = create(base) })
	if initErr != nil {
		t.Fatalf("testdb: %v", initErr)
	}
	pool, err := pgxpool.New(context.Background(), pkgURL)
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9_]+`)

func create(base string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(dir))
	short := unsafeChars.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "_")
	db := "kiff_test_gw_" + short + "_" + hex.EncodeToString(sum[:4])
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	ident := pgx.Identifier{db}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
		return "", err
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = "/" + db
	return u.String(), nil
}
