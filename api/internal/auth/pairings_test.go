package auth

import (
	"encoding/hex"
	"testing"
)

// The check code's shared vector (the desktop client contract, §5.2): the P-256 keys of
// private keys 1 and 2, which every implementation asserts as these literals.
func TestThePairingCodeMatchesTheSharedVector(t *testing.T) {
	pub, _ := hex.DecodeString("3059301306072a8648ce3d020106082a8648ce3d030107034200046b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c2964fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5")
	approval, _ := hex.DecodeString("3059301306072a8648ce3d020106082a8648ce3d030107034200047cf27b188d034f7e8a52380304b51ac3c08969e277f21b35a60b48fc4766997807775510db8ed040293d9ac69f7430dbba7dade63ce982299e04b79d227873d1")
	if got := PairingCode(pub, approval); got != "H6SJNU" {
		t.Errorf("both keys: %q, want H6SJNU", got)
	}
	if got := PairingCode(pub, nil); got != "LTJFF6" {
		t.Errorf("the device key alone: %q, want LTJFF6", got)
	}
}
