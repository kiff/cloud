package handlers_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

// setupBackend stands in for the handlers behind the signed setup calls:
// each POST /v1/keys mints a key, and each POST /v1/me/mandates sets the
// Card's limit to the body's.
type setupBackend struct {
	mu    sync.Mutex
	keys  int
	limit int
}

func (b *setupBackend) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/keys", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		b.keys++
		b.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /v1/me/mandates", func(w http.ResponseWriter, r *http.Request) {
		var card struct{ Limit int }
		if err := json.NewDecoder(r.Body).Decode(&card); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		b.limit = card.Limit
		b.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	return mux
}

// signingPhone is a phone's session with its device key.
type signingPhone struct {
	t   *testing.T
	key *ecdsa.PrivateKey
	id  auth.Identity
}

func newSigningPhone(t *testing.T) signingPhone {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	return signingPhone{t: t, key: key, id: auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper,
		Roles: []string{"tenant_owner"}, DeviceKey: der, DeviceSessionID: "sess-phone"}}
}

func (p signingPhone) sign(path, body string, at time.Time) string {
	ts := at.UTC().Format(time.RFC3339)
	digest := sha256.Sum256(auth.RequestMessage(p.id.Tenant, http.MethodPost, path, []byte(body), ts))
	sig, err := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)
}

// post sends a signed setup call through an API process: a route policy
// over the record made, in front of the backend.
func (p signingPhone) post(made auth.SignedRequests, b *setupBackend, path, body, header string) *httptest.ResponseRecorder {
	policy := auth.DefaultRoutePolicy().WithSignedRequests(made)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set(auth.DeviceSignatureHeader, header)
	rec := httptest.NewRecorder()
	withFakeIdentity(policy.Wrap(b.handler()), p.id).ServeHTTP(rec, req)
	return rec
}

// A signed setup call is made once, even when the API restarts in
// between. Each post builds a new policy, as a restarted process would, over
// the same record.
func testSignedCallsSurviveARestart(t *testing.T, made auth.SignedRequests) {
	phone := newSigningPhone(t)
	b := &setupBackend{}
	now := time.Now()

	keyBody := `{"label":"rae","roles":["gateway_agent"],"agent_id":"rae"}`
	mint := phone.sign("/v1/keys", keyBody, now)
	if rec := phone.post(made, b, "/v1/keys", keyBody, mint); rec.Code != http.StatusCreated {
		t.Fatalf("signed key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := phone.post(made, b, "/v1/keys", keyBody, mint); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "signature_replayed") {
		t.Fatalf("key replayed after a restart: %d %s", rec.Code, rec.Body.String())
	}
	if b.keys != 1 {
		t.Fatalf("keys minted: %d, want 1", b.keys)
	}

	// The Card is issued, then tightened by a later signed call. Replaying
	// the first after a restart must not loosen it again.
	issue := phone.sign("/v1/me/mandates", `{"limit":100}`, now)
	if rec := phone.post(made, b, "/v1/me/mandates", `{"limit":100}`, issue); rec.Code != http.StatusCreated {
		t.Fatalf("issue: %d %s", rec.Code, rec.Body.String())
	}
	tighten := phone.sign("/v1/me/mandates", `{"limit":50}`, now.Add(time.Second))
	if rec := phone.post(made, b, "/v1/me/mandates", `{"limit":50}`, tighten); rec.Code != http.StatusCreated {
		t.Fatalf("tighten: %d %s", rec.Code, rec.Body.String())
	}
	if rec := phone.post(made, b, "/v1/me/mandates", `{"limit":100}`, issue); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "signature_replayed") {
		t.Fatalf("Card replayed after a restart: %d %s", rec.Code, rec.Body.String())
	}
	if b.limit != 50 {
		t.Fatalf("the Card's limit is %d after the replay, want 50", b.limit)
	}
}

func TestSignedCallsSurviveARestart(t *testing.T) {
	t.Parallel()
	testSignedCallsSurviveARestart(t, auth.NewMemorySignedRequests())
}

func requireSignedRequestsPostgres(t *testing.T) string {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping signed-request record test")
	}
	pool := openPool(t, url)
	if err := tenant.ApplyCloudMeta(context.Background(), pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := auth.ApplySignedRequestsSchema(context.Background(), pool); err != nil {
		t.Fatalf("ApplySignedRequestsSchema: %v", err)
	}
	return url
}

func openPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSignedCallsSurviveARestart_Postgres(t *testing.T) {
	url := requireSignedRequestsPostgres(t)
	testSignedCallsSurviveARestart(t, auth.NewPostgresSignedRequests(openPool(t, url)))
}

// Two API processes over one database: the second refuses a request the
// first accepted, and of two at once exactly one goes through.
func TestSignedCallsOnceAcrossProcesses_Postgres(t *testing.T) {
	url := requireSignedRequestsPostgres(t)
	first, second := auth.NewPostgresSignedRequests(openPool(t, url)), auth.NewPostgresSignedRequests(openPool(t, url))
	phone := newSigningPhone(t)
	b := &setupBackend{}
	now := time.Now()

	body := `{"label":"rae","roles":["gateway_agent"],"agent_id":"rae"}`
	h := phone.sign("/v1/keys", body, now)
	if rec := phone.post(first, b, "/v1/keys", body, h); rec.Code != http.StatusCreated {
		t.Fatalf("first process: %d %s", rec.Code, rec.Body.String())
	}
	if rec := phone.post(second, b, "/v1/keys", body, h); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "signature_replayed") {
		t.Fatalf("second process: %d %s", rec.Code, rec.Body.String())
	}

	body2 := `{"label":"rae-2","roles":["gateway_agent"],"agent_id":"rae"}`
	h2 := phone.sign("/v1/keys", body2, now)
	codes := make(chan int, 2)
	for _, made := range []auth.SignedRequests{first, second} {
		go func() { codes <- phone.post(made, b, "/v1/keys", body2, h2).Code }()
	}
	if a, c := <-codes, <-codes; a+c != http.StatusCreated+http.StatusConflict {
		t.Fatalf("two at once: %d and %d, want one 201 and one 409", a, c)
	}
	if b.keys != 2 {
		t.Fatalf("keys minted: %d, want 2", b.keys)
	}
}

// A record lasts as long as the signature could verify; after that the row
// is pruned, or taken over by the same ID.
func TestPostgresSignedRequests_Expiry(t *testing.T) {
	url := requireSignedRequestsPostgres(t)
	pool := openPool(t, url)
	made := auth.NewPostgresSignedRequests(pool)
	ctx := context.Background()
	now := time.Now().UTC()
	id, other := []byte("replay-"+now.String()), []byte("other-"+now.String())

	use := func(id []byte, at time.Time) bool {
		t.Helper()
		ok, err := made.Use(ctx, id, at, at.Add(10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	seen := func(id []byte, at time.Time) bool {
		t.Helper()
		ok, err := made.Seen(ctx, id, at)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if seen(id, now) {
		t.Fatal("a record seen before it was made")
	}
	if !use(id, now) {
		t.Fatal("a new request was refused")
	}
	// Seen while it lasts, not after (a device grant's issue record).
	if !seen(id, now.Add(9*time.Minute)) || seen(id, now.Add(11*time.Minute)) {
		t.Fatal("Seen doesn't follow the record's life")
	}
	if use(id, now.Add(9*time.Minute)) {
		t.Fatal("a request was accepted twice while its record lasts")
	}
	if !use(id, now.Add(11*time.Minute)) {
		t.Fatal("a record that passed still refuses")
	}

	// Using another prunes every record that passed.
	if !use(other, now.Add(30*time.Minute)) {
		t.Fatal("another request was refused")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.signed_requests WHERE replay_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a passed record wasn't pruned: %d rows", n)
	}
}

// failingRecord can't decide.
type failingRecord struct{}

func (failingRecord) Use(context.Context, []byte, time.Time, time.Time) (bool, error) {
	return false, errors.New("database unavailable")
}

func (failingRecord) Seen(context.Context, []byte, time.Time) (bool, error) {
	return false, errors.New("signed_requests: unavailable")
}

// When the record can't be read, the call is refused: a signed setup call
// never goes through unrecorded.
func TestSignedCallRefusedWhenTheRecordFails(t *testing.T) {
	t.Parallel()
	phone := newSigningPhone(t)
	b := &setupBackend{}
	body := `{"label":"rae","roles":["gateway_agent"],"agent_id":"rae"}`
	rec := phone.post(failingRecord{}, b, "/v1/keys", body, phone.sign("/v1/keys", body, time.Now()))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "signature_check_unavailable") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if b.keys != 0 {
		t.Fatalf("a key was minted unrecorded")
	}
}
