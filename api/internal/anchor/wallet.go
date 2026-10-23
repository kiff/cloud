package anchor

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Wallet is the demo signer used by the PoC. v0.1 holds a single
// secp256k1 private key in memory; production replaces this with a
// KMS-backed signer per tenant.
type Wallet struct {
	privateKey *ecdsa.PrivateKey
	address    common.Address
}

// NewWalletFromHex builds a wallet from a hex-encoded secp256k1
// private key, with or without a "0x" prefix. Used when the demo
// wallet's key comes from an environment variable.
func NewWalletFromHex(s string) (*Wallet, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return nil, errors.New("anchor: empty private key")
	}
	bytes, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("anchor: decode private key hex: %w", err)
	}
	key, err := crypto.ToECDSA(bytes)
	if err != nil {
		return nil, fmt.Errorf("anchor: parse private key: %w", err)
	}
	return walletFromKey(key), nil
}

// NewRandomWallet generates a fresh wallet using crypto/rand. Used by
// tests; never used by production.
func NewRandomWallet() (*Wallet, error) {
	key, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("anchor: generate key: %w", err)
	}
	return walletFromKey(key), nil
}

func walletFromKey(key *ecdsa.PrivateKey) *Wallet {
	return &Wallet{
		privateKey: key,
		address:    crypto.PubkeyToAddress(key.PublicKey),
	}
}

// Address returns the wallet's Ethereum address.
func (w *Wallet) Address() common.Address { return w.address }

// PrivateKey returns the wallet's underlying ECDSA private key. The
// chain client uses it to construct a signed transaction; nothing
// else should rely on this accessor.
func (w *Wallet) PrivateKey() *ecdsa.PrivateKey { return w.privateKey }
