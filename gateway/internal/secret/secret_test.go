package secret_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/secret"
)

func ring(t *testing.T, ids ...string) (*secret.Keyring, []string) {
	t.Helper()
	var parts []string
	for _, id := range ids {
		k, err := secret.NewKey(id)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, k)
	}
	k, err := secret.ParseKeyring(strings.Join(parts, ","))
	if err != nil {
		t.Fatal(err)
	}
	return k, parts
}

func TestSealOpenBoundToAccountAndTool(t *testing.T) {
	k, _ := ring(t, "v1")
	id, sealed, err := k.Seal("tenant-a", "issue_refund", "sk_live_secret")
	if err != nil || id != "v1" || strings.Contains(string(sealed), "sk_live_secret") {
		t.Fatalf("seal: %q %v", id, err)
	}
	if got, err := k.Open("tenant-a", "issue_refund", id, sealed); err != nil || got != "sk_live_secret" {
		t.Fatalf("open: %q %v", got, err)
	}
	for _, c := range [][2]string{{"tenant-b", "issue_refund"}, {"tenant-a", "deploy"}} {
		if _, err := k.Open(c[0], c[1], id, sealed); !errors.Is(err, secret.ErrUnreadable) {
			t.Fatalf("opened for %v: %v", c, err)
		}
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := k.Open("tenant-a", "issue_refund", id, sealed); !errors.Is(err, secret.ErrUnreadable) {
		t.Fatal("tampered ciphertext opened")
	}
}

// Rotation: a new current key seals; the old key still opens until it
// is removed from the ring.
func TestRotation(t *testing.T) {
	old, parts := ring(t, "v1")
	id, sealed, _ := old.Seal("t", "x", "tok")
	next, _ := secret.NewKey("v2")
	both, err := secret.ParseKeyring(next + "," + parts[0])
	if err != nil || both.Current() != "v2" {
		t.Fatalf("%v %v", both, err)
	}
	if got, err := both.Open("t", "x", id, sealed); err != nil || got != "tok" {
		t.Fatalf("old key no longer opens: %v", err)
	}
	if nid, _, _ := both.Seal("t", "x", "tok"); nid != "v2" {
		t.Fatalf("sealed with %s", nid)
	}
	onlyNew, _ := secret.ParseKeyring(next)
	if _, err := onlyNew.Open("t", "x", id, sealed); !errors.Is(err, secret.ErrUnreadable) {
		t.Fatal("a removed key still opened")
	}
}

func TestParseKeyringRejects(t *testing.T) {
	for _, spec := range []string{"", "v1", "v1:short", "bad id:" + strings.Repeat("A", 44)} {
		if _, err := secret.ParseKeyring(spec); err == nil {
			t.Errorf("accepted %q", spec)
		}
	}
	k, _ := secret.NewKey("v1")
	if _, err := secret.ParseKeyring(k + "," + k); err == nil {
		t.Error("accepted a duplicate id")
	}
}

func TestRedact(t *testing.T) {
	if got := secret.Redact(`401 from tool: bad token "sk_live_abc"`, "sk_live_abc", ""); strings.Contains(got, "sk_live_abc") {
		t.Fatal(got)
	}
}

// Every string in a JSON result is redacted, keys and escaped forms
// included; numbers and structure are kept.
func TestRedactJSON(t *testing.T) {
	cred := `sk"live/<abc>`
	raw := []byte(`{"content":[{"type":"text","text":"token sk\"live\/\u003cabc\u003e ok"}],"n":12345678901234567890,"sk\"live/<abc>":true}`)
	out := secret.RedactJSON(raw, cred)
	if strings.Contains(string(out), "live") || !strings.Contains(string(out), "12345678901234567890") ||
		!strings.Contains(string(out), "[redacted] ok") {
		t.Fatalf("%s", out)
	}
	if got := secret.RedactJSON([]byte("not json sk_abc"), "sk_abc"); string(got) != "not json [redacted]" {
		t.Fatalf("%s", got)
	}
}
