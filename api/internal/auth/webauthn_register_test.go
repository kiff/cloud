package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

// cborEnc encodes the values the tests build: int, int64, []byte, string,
// []any and map[any]any (keys sorted as encoded, for a stable output), plus
// raw, written as it is.
type raw []byte

func cborEnc(v any) []byte {
	head := func(major byte, n uint64) []byte {
		switch {
		case n < 24:
			return []byte{major<<5 | byte(n)}
		case n < 1<<8:
			return []byte{major<<5 | 24, byte(n)}
		case n < 1<<16:
			b := []byte{major<<5 | 25, 0, 0}
			binary.BigEndian.PutUint16(b[1:], uint16(n))
			return b
		case n < 1<<32:
			b := []byte{major<<5 | 26, 0, 0, 0, 0}
			binary.BigEndian.PutUint32(b[1:], uint32(n))
			return b
		default:
			b := []byte{major<<5 | 27, 0, 0, 0, 0, 0, 0, 0, 0}
			binary.BigEndian.PutUint64(b[1:], n)
			return b
		}
	}
	switch x := v.(type) {
	case raw:
		return x
	case int:
		return cborEnc(int64(x))
	case int64:
		if x >= 0 {
			return head(0, uint64(x))
		}
		return head(1, uint64(-1-x))
	case []byte:
		return append(head(2, uint64(len(x))), x...)
	case string:
		return append(head(3, uint64(len(x))), x...)
	case []any:
		out := head(4, uint64(len(x)))
		for _, e := range x {
			out = append(out, cborEnc(e)...)
		}
		return out
	case map[any]any:
		type kv struct{ k, v []byte }
		var pairs []kv
		for k, e := range x {
			pairs = append(pairs, kv{cborEnc(k), cborEnc(e)})
		}
		sort.Slice(pairs, func(i, j int) bool { return bytes.Compare(pairs[i].k, pairs[j].k) < 0 })
		out := head(5, uint64(len(x)))
		for _, p := range pairs {
			out = append(append(out, p.k...), p.v...)
		}
		return out
	}
	panic("cborEnc: unsupported")
}

// creation is a registration the tests build and then spoil one part of.
type creation struct {
	key        *ecdsa.PrivateKey
	credential []byte
	clientType string
	origin     string
	cross      bool
	tenant     string
	subject    string
	issuedAt   string
	rpID       string
	flags      byte
	count      uint32
	cose       map[any]any
	format     string
	stmt       map[any]any
	extra      map[any]any // more top-level fields of the attestationObject
	trailing   []byte      // after the COSE key, in authData
	idLen      int         // the credential id length written, when not its own
}

func newCreation(t *testing.T, now time.Time) *creation {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := &creation{key: k, credential: bytes.Repeat([]byte{0xC7}, 32), clientType: "webauthn.create", origin: "https://kiff.dev",
		tenant: "tenant-1", subject: "user-abc", issuedAt: now.Format(time.RFC3339), rpID: "kiff.dev",
		flags: flagUserPresent | flagUserVerified | flagAttested, format: "none", stmt: map[any]any{}}
	x, y := make([]byte, 32), make([]byte, 32)
	k.PublicKey.X.FillBytes(x)
	k.PublicKey.Y.FillBytes(y)
	c.cose = map[any]any{1: 2, 3: -7, -1: 1, -2: x, -3: y}
	return c
}

func (c *creation) clientData() []byte {
	digest := sha256.Sum256(RegistrationMessage(c.tenant, c.subject, c.issuedAt))
	b, _ := json.Marshal(map[string]any{"type": c.clientType, "challenge": base64.RawURLEncoding.EncodeToString(digest[:]),
		"origin": c.origin, "crossOrigin": c.cross})
	return b
}

func (c *creation) authData() []byte {
	rp := sha256.Sum256([]byte(c.rpID))
	out := append(rp[:], c.flags, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(out[33:], c.count)
	out = append(out, make([]byte, 16)...) // aaguid: zero, as attestation "none" gives it
	n := len(c.credential)
	if c.idLen != 0 {
		n = c.idLen
	}
	out = append(out, byte(n>>8), byte(n))
	out = append(out, c.credential...)
	out = append(out, cborEnc(c.cose)...)
	return append(out, c.trailing...)
}

func (c *creation) attestation() []byte {
	m := map[any]any{"fmt": c.format, "attStmt": c.stmt, "authData": c.authData()}
	for k, v := range c.extra {
		m[k] = v
	}
	return cborEnc(m)
}

func (c *creation) verify(rp RelyingParty, now time.Time) (Registered, error) {
	return VerifyRegistration(rp, "tenant-1", "user-abc", c.issuedAt, c.clientData(), c.attestation(), now)
}

func TestARegistrationProvesItsKey(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 30, 0, 0, time.UTC)
	c := newCreation(t, now)
	c.count = 7
	got, err := c.verify(KiffRelyingParty, now)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := x509.MarshalPKIXPublicKey(&c.key.PublicKey)
	if !bytes.Equal(got.CredentialID, c.credential) || !bytes.Equal(got.PublicKey, want) || got.SignCount != 7 || got.Synced || got.BackupEligible {
		t.Fatalf("registered %+v", got)
	}
	// The key it read signs as a passkey's: an assertion by it verifies.
	if _, err := deviceKey(got.PublicKey); err != nil {
		t.Fatal(err)
	}

	// Synced, and extensions ED says are there.
	c = newCreation(t, now)
	c.flags |= flagBackupElig | flagBackedUp | flagExtensions
	c.trailing = cborEnc(map[any]any{"credProtect": 2})
	if got, err := c.verify(KiffRelyingParty, now); err != nil || !got.Synced || !got.BackupEligible {
		t.Fatalf("a synced passkey with extensions: %+v %v", got, err)
	}
	// Issued up to ten minutes either side of KIFF's clock.
	for _, d := range []time.Duration{-RegistrationMaxAge, RegistrationMaxAge} {
		c = newCreation(t, now.Add(d))
		if _, err := c.verify(KiffRelyingParty, now); err != nil {
			t.Errorf("issued %v from now: %v", d, err)
		}
	}
}

func TestARegistrationIsRefused(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 30, 0, 0, time.UTC)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	for name, c := range map[string]struct {
		spoil func(c *creation)
		want  error
	}{
		// 1. clientDataJSON.
		"a get, not a create":     {func(c *creation) { c.clientType = "webauthn.get" }, ErrRegistrationType},
		"another origin":          {func(c *creation) { c.origin = "https://kiff.dev.evil.example" }, ErrRegistrationOrigin},
		"an app not on the list":  {func(c *creation) { c.origin = "android:apk-key-hash:" + strings.Repeat("A", 43) }, ErrRegistrationOrigin},
		"framed in another site":  {func(c *creation) { c.cross = true }, ErrRegistrationOrigin},
		"another person's":        {func(c *creation) { c.subject = "user-zed" }, ErrRegistrationChallenge},
		"another account's":       {func(c *creation) { c.tenant = "tenant-2" }, ErrRegistrationChallenge},
		"issued 11 minutes ago":   {func(c *creation) { c.issuedAt = now.Add(-11 * time.Minute).Format(time.RFC3339) }, ErrRegistrationIssued},
		"issued 11 minutes ahead": {func(c *creation) { c.issuedAt = now.Add(11 * time.Minute).Format(time.RFC3339) }, ErrRegistrationIssued},
		"issued_at not a time":    {func(c *creation) { c.issuedAt = "yesterday" }, ErrRegistrationIssued},
		// 2. The attestation.
		"fmt packed":              {func(c *creation) { c.format = "packed" }, ErrRegistrationAttestation},
		"a statement with none":   {func(c *creation) { c.stmt = map[any]any{"sig": []byte{1}} }, ErrRegistrationAttestation},
		"another top-level field": {func(c *creation) { c.extra = map[any]any{"epAtt": int64(1)} }, ErrRegistrationShape},
		// 3. authData.
		"another RP ID":              {func(c *creation) { c.rpID = "evil.example" }, ErrRegistrationOrigin},
		"no user verification":       {func(c *creation) { c.flags &^= flagUserVerified }, ErrRegistrationUser},
		"no user presence":           {func(c *creation) { c.flags &^= flagUserPresent }, ErrRegistrationUser},
		"no attested credential":     {func(c *creation) { c.flags &^= flagAttested }, ErrRegistrationShape},
		"bytes after the key":        {func(c *creation) { c.trailing = []byte{0} }, ErrRegistrationShape},
		"ED but no extensions":       {func(c *creation) { c.flags |= flagExtensions }, ErrRegistrationShape},
		"ED with a non-map":          {func(c *creation) { c.flags |= flagExtensions; c.trailing = cborEnc(int64(1)) }, ErrRegistrationShape},
		"an empty credential id":     {func(c *creation) { c.credential = nil }, ErrRegistrationShape},
		"an id longer than 1023":     {func(c *creation) { c.credential = make([]byte, 1024) }, ErrRegistrationShape},
		"an id length past the data": {func(c *creation) { c.idLen = 4000 }, ErrRegistrationShape},
		// 4. The COSE key.
		"EdDSA (alg -8)":   {func(c *creation) { c.cose[3] = -8 }, ErrRegistrationKey},
		"P-384 (crv 2)":    {func(c *creation) { c.cose[-1] = 2 }, ErrRegistrationKey},
		"an OKP key":       {func(c *creation) { c.cose[1] = 1 }, ErrRegistrationKey},
		"RS256 (alg -257)": {func(c *creation) { c.cose[3] = -257 }, ErrRegistrationKey},
		"x of 31 bytes":    {func(c *creation) { c.cose[-2] = c.cose[-2].([]byte)[1:] }, ErrRegistrationKey},
		"a point off the curve": {func(c *creation) {
			y := append([]byte(nil), c.cose[-3].([]byte)...)
			y[31] ^= 1
			c.cose[-3] = y
		}, ErrRegistrationKey},
		"another field in the key": {func(c *creation) { c.cose[-4] = []byte{1} }, ErrRegistrationKey},
		"no y":                     {func(c *creation) { delete(c.cose, -3) }, ErrRegistrationKey},
		"y as a text string":       {func(c *creation) { c.cose[-3] = string(c.cose[-3].([]byte)) }, ErrRegistrationKey},
		"another key's x":          {func(c *creation) { x := make([]byte, 32); other.PublicKey.X.FillBytes(x); c.cose[-2] = x }, ErrRegistrationKey},
	} {
		cr := newCreation(t, now)
		c.spoil(cr)
		if _, err := cr.verify(KiffRelyingParty, now); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}

	// What isn't CBOR, or isn't the object, and sizes past the limits.
	c := newCreation(t, now)
	for name, obj := range map[string][]byte{
		"empty":           nil,
		"not CBOR":        []byte("{}"),
		"an array":        cborEnc([]any{"none"}),
		"cut short":       c.attestation()[:20],
		"a byte after it": append(c.attestation(), 0),
		"over 4 KiB":      make([]byte, maxAttestation+1),
	} {
		if _, err := VerifyRegistration(KiffRelyingParty, "tenant-1", "user-abc", c.issuedAt, c.clientData(), obj, now); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
	if _, err := VerifyRegistration(KiffRelyingParty, "tenant-1", "user-abc", c.issuedAt, []byte("not json"), c.attestation(), now); !errors.Is(err, ErrRegistrationShape) {
		t.Errorf("clientDataJSON not JSON: %v", err)
	}
}

func TestAnAndroidAppsOriginIsOnTheListWhenConfigured(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 30, 0, 0, time.UTC)
	fp := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	apps, err := ParseAndroidApps(" dev.kiff.tafs = " + fp + " ,")
	if err != nil || len(apps) != 1 || apps[0].Package != "dev.kiff.tafs" {
		t.Fatalf("%+v %v", apps, err)
	}
	origin := "android:apk-key-hash:" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xAB}, 32))
	if apps[0].Origin() != origin {
		t.Fatalf("origin %s", apps[0].Origin())
	}
	rp := PasskeyRelyingParty(apps)
	c := newCreation(t, now)
	c.origin = origin
	if _, err := c.verify(rp, now); err != nil {
		t.Fatalf("the configured app: %v", err)
	}
	if _, err := c.verify(KiffRelyingParty, now); !errors.Is(err, ErrRegistrationOrigin) {
		t.Fatalf("the app with none configured: %v", err)
	}
	if len(KiffRelyingParty.Origins) != 1 {
		t.Fatal("PasskeyRelyingParty changed KiffRelyingParty")
	}
	for _, bad := range []string{"dev.kiff.tafs", "tafs=" + fp, "dev.kiff.tafs=" + fp[:len(fp)-3], "dev.kiff.tafs=" + strings.ReplaceAll(fp, ":", ""), "1dev.kiff=" + fp} {
		if _, err := ParseAndroidApps(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if apps, err := ParseAndroidApps(""); err != nil || len(apps) != 0 {
		t.Errorf("empty: %v %v", apps, err)
	}
}

func TestTheCBORReaderRefusesWhatItDoesntRead(t *testing.T) {
	deep := cborEnc([]any{[]any{[]any{[]any{[]any{}}}}})
	for name, b := range map[string][]byte{
		"indefinite array":    {0x9f, 0xff},
		"indefinite bytes":    {0x5f, 0xff},
		"a tag":               {0xc0, 0x00},
		"a float":             {0xfa, 0, 0, 0, 0},
		"true":                {0xf5},
		"reserved length":     {0x1c},
		"a uint past int64":   {0x1b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"a string past input": {0x45, 1, 2},
		"a count past input":  {0x9a, 0x7f, 0xff, 0xff, 0xff},
		"a duplicate key":     {0xa2, 0x01, 0x00, 0x01, 0x00},
		"a bytes key":         {0xa1, 0x41, 0x00, 0x00},
		"five deep":           deep,
		"cut short":           {0x19, 0x01},
		"empty":               nil,
	} {
		if _, err := decodeCBORWhole(b); !errors.Is(err, ErrCBOR) {
			t.Errorf("%s: %v", name, err)
		}
	}
	v, err := decodeCBORWhole(cborEnc(map[any]any{1: int64(-7), "a": []byte{1}, -3: []any{"x", int64(1 << 40)}}))
	m, _ := v.(cborMap)
	if err != nil || m[int64(1)] != int64(-7) || !bytes.Equal(m["a"].([]byte), []byte{1}) || m[int64(-3)].([]any)[1] != int64(1<<40) {
		t.Fatalf("read %#v %v", v, err)
	}
}

func FuzzDecodeCBOR(f *testing.F) {
	now := time.Date(2026, 11, 15, 0, 30, 0, 0, time.UTC)
	f.Add(cborEnc(map[any]any{"fmt": "none", "attStmt": map[any]any{}, "authData": []byte{1, 2, 3}}))
	f.Add([]byte{0x9f, 0xff})
	f.Add([]byte{0xa2, 0x01, 0x00, 0x01, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		if v, n, err := decodeCBOR(b); err == nil && (n <= 0 || n > len(b) || v == nil && n == 0) {
			t.Fatalf("read %d of %d", n, len(b))
		}
		// Never panics, whatever the attestationObject.
		_, _ = VerifyRegistration(KiffRelyingParty, "t", "s", now.Format(time.RFC3339), []byte(`{"type":"webauthn.create"}`), b, now)
	})
}
