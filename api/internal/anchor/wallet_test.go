package anchor

import (
	"strings"
	"testing"
)

func TestNewWalletFromHexRoundTrip(t *testing.T) {
	t.Parallel()

	w1, err := NewRandomWallet()
	if err != nil {
		t.Fatalf("NewRandomWallet: %v", err)
	}

	// Recreate the same wallet from its hex private key.
	hex := privateKeyHex(t, w1)
	w2, err := NewWalletFromHex(hex)
	if err != nil {
		t.Fatalf("NewWalletFromHex: %v", err)
	}
	if w1.Address() != w2.Address() {
		t.Fatalf("address mismatch: %s vs %s", w1.Address(), w2.Address())
	}

	// Same with a 0x-prefixed key.
	w3, err := NewWalletFromHex("0x" + hex)
	if err != nil {
		t.Fatalf("NewWalletFromHex(0x...): %v", err)
	}
	if w1.Address() != w3.Address() {
		t.Fatalf("address mismatch with 0x prefix: %s vs %s", w1.Address(), w3.Address())
	}
}

func TestNewWalletFromHexRejectsBadInput(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "0x", "zz", strings.Repeat("0", 64)} {
		if _, err := NewWalletFromHex(in); err == nil {
			t.Fatalf("NewWalletFromHex(%q) returned no error", in)
		}
	}
}

// privateKeyHex returns the wallet's private key as a 64-char hex
// string, no 0x prefix. Used only by tests.
func privateKeyHex(t *testing.T, w *Wallet) string {
	t.Helper()
	const hex = "0123456789abcdef"
	d := w.PrivateKey().D.Bytes()
	// Left-pad to 32 bytes.
	if len(d) < 32 {
		pad := make([]byte, 32)
		copy(pad[32-len(d):], d)
		d = pad
	}
	out := make([]byte, len(d)*2)
	for i, b := range d {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out)
}
