// Package testdb gives each test package its own Postgres database.
//
// `go test ./...` runs packages in parallel, one process each. When they
// all pointed at the single database in KIFF_TEST_POSTGRES_URL, packages
// truncating the same shared tables (cloud_meta.*, tenants) deadlocked or
// deleted each other's rows mid-test. URL derives a database per package
// from that base URL, so packages never share tables.
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
)

// EnvVar names the base database URL. Unset means "skip Postgres tests",
// and URL returns "" so callers keep their existing skip.
const EnvVar = "KIFF_TEST_POSTGRES_URL"

var (
	once    sync.Once
	pkgURL  string
	initErr error
)

// URL returns a connection URL for this test package's own database,
// creating it on first use in the process. The database is dropped and
// recreated at the start of each run, so a run never sees a previous
// run's rows; it is left in place afterwards for inspection.
func URL(t testing.TB) string {
	t.Helper()
	base := os.Getenv(EnvVar)
	if base == "" {
		return ""
	}
	once.Do(func() { pkgURL, initErr = create(base) })
	if initErr != nil {
		t.Fatalf("testdb: %v", initErr)
	}
	return pkgURL
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9_]+`)

// name is stable per package directory: a readable suffix plus a hash of
// the full path, so two packages with the same base name never collide.
func name() (string, error) {
	dir, err := os.Getwd() // go test runs each package in its own directory
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(dir))
	short := unsafeChars.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "_")
	if len(short) > 30 {
		short = short[:30]
	}
	return "kiff_test_" + short + "_" + hex.EncodeToString(sum[:4]), nil
}

func create(base string) (string, error) {
	db, err := name()
	if err != nil {
		return "", err
	}
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
