// Package appstoretest signs App Store payloads the way Apple does, with a
// made-up chain (root, intermediate, leaf), for tests. Nothing here is
// Apple's: a verifier trusts it only when built with NewTestVerifier.
package appstoretest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"
	"time"
)

// Signer signs payloads with a leaf issued under its own root.
type Signer struct {
	Root  *x509.Certificate
	chain []string // x5c: leaf, intermediate, root
	key   *ecdsa.PrivateKey
}

func cert(t testing.TB, tmpl *x509.Certificate, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func key(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// New makes a chain valid from a year ago to a year from now.
func New(t testing.TB) *Signer {
	t.Helper()
	from, to := time.Now().AddDate(-1, 0, 0), time.Now().AddDate(1, 0, 0)
	ca := func(n int64, name string) *x509.Certificate {
		return &x509.Certificate{SerialNumber: big.NewInt(n), Subject: pkix.Name{CommonName: name}, NotBefore: from, NotAfter: to,
			IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	}
	rootKey, interKey, leafKey := key(t), key(t), key(t)
	root := cert(t, ca(1, "Test Root"), nil, &rootKey.PublicKey, rootKey)
	inter := cert(t, ca(2, "Test Intermediate"), root, &interKey.PublicKey, rootKey)
	leaf := cert(t, &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "Test Leaf"}, NotBefore: from, NotAfter: to,
		KeyUsage: x509.KeyUsageDigitalSignature}, inter, &leafKey.PublicKey, interKey)
	enc := base64.StdEncoding.EncodeToString
	return &Signer{Root: root, key: leafKey, chain: []string{enc(leaf.Raw), enc(inter.Raw), enc(root.Raw)}}
}

// Sign is payload as a compact JWS (ES256, x5c), as Apple sends it.
func (s *Signer) Sign(t testing.TB, payload any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "ES256", "x5c": s.chain})
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	input := enc(header) + "." + enc(body)
	digest := sha256.Sum256([]byte(input))
	r, ss, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	return input + "." + enc(sig)
}
