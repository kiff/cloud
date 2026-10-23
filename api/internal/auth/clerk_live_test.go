package auth_test

import (
	"context"
	"os"
	"testing"
	"time"

	clerksdk "github.com/clerk/clerk-sdk-go/v2"

	"github.com/kiff/cloud/api/internal/auth"
)

// TestClerkSessions_Live verifies a real Clerk-issued session JWT
// against the dev instance whose Backend API key is in
// CLERK_TEST_SECRET_KEY. The test catches drift between our
// adapter's claim assumptions and Clerk's actual JWT shape — the
// existing fakeVerifier-based tests in clerk_test.go cannot.
//
// Skipped by default; the env-gated pattern matches
// apikey_store_postgres_test.go so unrelated PRs are not affected.
//
// Required env vars (developer's local .env or CI secrets):
//
//	CLERK_TEST_SECRET_KEY    sk_test_... from Clerk → API Keys.
//	CLERK_TEST_SESSION_JWT   A live session JWT from the same instance.
//
// Clerk's session JWTs are short-lived (~60s default). For local
// dev, mint a fresh one from the dashboard's "Sessions → tokens"
// view and run the test promptly. CI flows that need durability
// should mint via the Backend API immediately before the test
// (out of scope for this file).
func TestClerkSessions_Live(t *testing.T) {
	secret := os.Getenv("CLERK_TEST_SECRET_KEY")
	token := os.Getenv("CLERK_TEST_SESSION_JWT")
	if secret == "" || token == "" {
		t.Skip("CLERK_TEST_SECRET_KEY / CLERK_TEST_SESSION_JWT not set; skipping live Clerk integration test")
	}

	// The Clerk SDK uses a package-level secret. Reading the env
	// here keeps the test self-contained — running it does not
	// require CLERK_SECRET_KEY (the production var) to be set.
	prev := readClerkPackageSecret()
	clerksdk.SetKey(secret)
	t.Cleanup(func() { clerksdk.SetKey(prev) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	id, err := auth.NewClerkSessions().VerifyToken(ctx, token)
	if err != nil {
		t.Fatalf("VerifyToken: %v\n\nCommon causes:\n  - JWT expired (Clerk session JWTs are ~60s by default; mint a fresh one)\n  - Wrong instance (the JWT and CLERK_TEST_SECRET_KEY must come from the same Clerk dev instance)\n  - Network blocked (the SDK fetches JWKS from clerk.com)", err)
	}
	if id.Subject == "" {
		t.Fatalf("expected non-empty Subject, got Identity=%+v", id)
	}
	if id.Method != auth.MethodSession {
		t.Fatalf("expected Method=%q, got %q", auth.MethodSession, id.Method)
	}
}

// readClerkPackageSecret returns the current package-level Clerk
// secret so the test can restore it on cleanup. The SDK does not
// expose a getter; we round-trip through SetKey by reading the env
// the binary uses at startup, which is the only other writer.
//
// In practice the test runs under `go test` where no other
// goroutine touches clerksdk.SetKey, so a missed restore would
// only affect the next test in the same package, and we have no
// other live tests in this file. Best effort.
func readClerkPackageSecret() string {
	return os.Getenv("CLERK_SECRET_KEY")
}
