package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestDecodeHexSaltAcceptsLowercase pins the canonical happy path.
func TestDecodeHexSaltAcceptsLowercase(t *testing.T) {
	t.Parallel()

	got, err := decodeHexSalt("deadbeef")
	if err != nil {
		t.Fatalf("decodeHexSalt: %v", err)
	}
	want := []byte{0xde, 0xad, 0xbe, 0xef}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

// TestDecodeHexSaltAcceptsPrefix accepts both `0x` and bare hex.
// Operators paste salts from logs, dashboards, and the kiff-tenant
// CLI; any one of those could format with or without the prefix.
func TestDecodeHexSaltAcceptsPrefix(t *testing.T) {
	t.Parallel()

	cases := []string{"0xdeadbeef", "0Xdeadbeef", "deadbeef", " deadbeef "}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			got, err := decodeHexSalt(c)
			if err != nil {
				t.Fatalf("decodeHexSalt(%q): %v", c, err)
			}
			if len(got) != 4 {
				t.Fatalf("len: got %d, want 4", len(got))
			}
		})
	}
}

// TestDecodeHexSaltRejectsOddLength catches the most common copy-
// paste error before we feed the wrong number of bytes to keccak.
func TestDecodeHexSaltRejectsOddLength(t *testing.T) {
	t.Parallel()

	_, err := decodeHexSalt("deadbee")
	if err == nil {
		t.Fatal("expected odd-length error, got nil")
	}
	if !strings.Contains(err.Error(), "odd hex length") {
		t.Fatalf("error message: got %q, want contains 'odd hex length'", err.Error())
	}
}

// TestDecodeHexSaltRejectsNonHex catches paste-from-the-wrong-field
// errors (e.g. a slug or an email accidentally landed where the
// salt was supposed to be).
func TestDecodeHexSaltRejectsNonHex(t *testing.T) {
	t.Parallel()

	_, err := decodeHexSalt("notahex!!")
	if err == nil {
		t.Fatal("expected non-hex error, got nil")
	}
}

// TestDecodeHexSaltRejectsEmpty surfaces the zero-input case
// explicitly so the CLI doesn't silently treat "" as "no salt"
// (the no-salt path is reached by simply omitting --salt).
func TestDecodeHexSaltRejectsEmpty(t *testing.T) {
	t.Parallel()

	cases := []string{"", "  ", "0x", "0X "}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			_, err := decodeHexSalt(c)
			if err == nil {
				t.Fatalf("decodeHexSalt(%q): expected error, got nil", c)
			}
		})
	}
}
