// Package appstore is the tafs app's Apple in-app purchases: Solo's
// auto-renewable subscription and the task packs, as App Store Server
// Notifications V2 and the app report them. Every payload is Apple's
// signed JWS, verified against Apple's root CA, pinned here; nothing is
// trusted before that.
package appstore

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

//go:embed AppleRootCA-G3.pem
var appleRootPEM []byte

// AppleRootG3SHA256 is the SHA-256 fingerprint of Apple Root CA - G3, as
// Apple publishes it. The embedded certificate must match it.
const AppleRootG3SHA256 = "63343abfb89a6a03ebb57e9b3f5fa7be7c4f5c756f3017b3a8c488c3653e9179"

// Apple's marker extensions: the leaf that signs App Store payloads, and
// the intermediate (Apple Worldwide Developer Relations) that issued it.
var (
	oidAppStoreLeaf          = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1}
	oidAppleWWDRIntermediate = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 2, 1}
)

// ErrInvalid is a payload that isn't Apple's, or isn't for this app.
var ErrInvalid = errors.New("appstore: invalid signed payload")

// AppleRoot is the pinned Apple Root CA - G3.
func AppleRoot() (*x509.Certificate, error) {
	block, _ := pem.Decode(appleRootPEM)
	if block == nil {
		return nil, fmt.Errorf("appstore: no root certificate")
	}
	if sum := sha256.Sum256(block.Bytes); hex.EncodeToString(sum[:]) != AppleRootG3SHA256 {
		return nil, fmt.Errorf("appstore: the root certificate isn't Apple Root CA - G3")
	}
	return x509.ParseCertificate(block.Bytes)
}

// Verifier checks Apple's signed payloads for one app.
type Verifier struct {
	roots *x509.CertPool
	// BundleID is the app's; a payload for another app is refused.
	bundleID string
	// environments are those accepted ("Production", "Sandbox").
	environments map[string]bool
	now          func() time.Time
	// markers: checking Apple's marker extensions; off only for tests,
	// whose chains are made up.
	markers bool
}

// NewVerifier verifies against the pinned Apple root, for bundleID, in
// environments.
func NewVerifier(bundleID string, environments []string) (*Verifier, error) {
	root, err := AppleRoot()
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	return newVerifier(pool, bundleID, environments, true), nil
}

// NewTestVerifier verifies against root, a test CA, without Apple's
// marker extensions. For tests only.
func NewTestVerifier(root *x509.Certificate, bundleID string, environments []string, now func() time.Time) *Verifier {
	pool := x509.NewCertPool()
	pool.AddCert(root)
	v := newVerifier(pool, bundleID, environments, false)
	v.now = now
	return v
}

func newVerifier(roots *x509.CertPool, bundleID string, environments []string, markers bool) *Verifier {
	env := map[string]bool{}
	for _, e := range environments {
		if e = strings.TrimSpace(e); e != "" {
			env[e] = true
		}
	}
	return &Verifier{roots: roots, bundleID: bundleID, environments: env, now: time.Now, markers: markers}
}

// BundleID is the app the verifier accepts payloads for.
func (v *Verifier) BundleID() string { return v.bundleID }

// accepts reports whether a payload's app and environment are this one's.
func (v *Verifier) accepts(bundleID, environment string) error {
	if bundleID != v.bundleID {
		return fmt.Errorf("%w: for another app", ErrInvalid)
	}
	if !v.environments[environment] {
		return fmt.Errorf("%w: environment %q not accepted", ErrInvalid, environment)
	}
	return nil
}

type jwsHeader struct {
	Alg string   `json:"alg"`
	X5c []string `json:"x5c"`
}

// Decode verifies a compact JWS signed by Apple and decodes its payload
// into out: ES256, its x5c chain (leaf, intermediate, root) verified up to
// the pinned root, and its signature by the leaf's key.
func (v *Verifier) Decode(token string, out any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: not a compact JWS", ErrInvalid)
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("%w: header", ErrInvalid)
	}
	var h jwsHeader
	if err := json.Unmarshal(rawHeader, &h); err != nil {
		return fmt.Errorf("%w: header", ErrInvalid)
	}
	if h.Alg != "ES256" || len(h.X5c) < 2 {
		return fmt.Errorf("%w: alg %q with %d certificates", ErrInvalid, h.Alg, len(h.X5c))
	}
	certs := make([]*x509.Certificate, len(h.X5c))
	for i, c := range h.X5c {
		der, err := base64.StdEncoding.DecodeString(c)
		if err != nil {
			return fmt.Errorf("%w: certificate %d", ErrInvalid, i)
		}
		if certs[i], err = x509.ParseCertificate(der); err != nil {
			return fmt.Errorf("%w: certificate %d", ErrInvalid, i)
		}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("%w: payload", ErrInvalid)
	}
	// The chain is checked at the time Apple signed the payload, as
	// Apple's own library does: a transaction signed before its signing
	// certificate expired stays valid.
	at := v.now()
	var signed struct {
		SignedDate int64 `json:"signedDate"`
	}
	if json.Unmarshal(payload, &signed) == nil && signed.SignedDate > 0 {
		at = time.UnixMilli(signed.SignedDate)
	}
	leaf := certs[0]
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	// The chain must end at the pinned root, whatever root the payload
	// carries: the x5c's own root is never trusted.
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: v.roots, Intermediates: inter, CurrentTime: at,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return fmt.Errorf("%w: chain: %v", ErrInvalid, err)
	}
	if v.markers && (!hasExtension(leaf, oidAppStoreLeaf) || !hasExtension(certs[1], oidAppleWWDRIntermediate)) {
		return fmt.Errorf("%w: not an App Store signing certificate", ErrInvalid)
	}
	key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("%w: leaf key", ErrInvalid)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return fmt.Errorf("%w: signature", ErrInvalid)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(key, digest[:], r, s) {
		return fmt.Errorf("%w: signature", ErrInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: payload: %v", ErrInvalid, err)
	}
	return nil
}

func hasExtension(c *x509.Certificate, oid asn1.ObjectIdentifier) bool {
	for _, e := range c.Extensions {
		if e.Id.Equal(oid) {
			return true
		}
	}
	return false
}

// Transaction is Apple's JWSTransactionDecodedPayload, the fields KIFF reads.
type Transaction struct {
	TransactionID         string `json:"transactionId"`
	OriginalTransactionID string `json:"originalTransactionId"`
	BundleID              string `json:"bundleId"`
	ProductID             string `json:"productId"`
	// Type is "Auto-Renewable Subscription" or "Consumable" here.
	Type            string `json:"type"`
	PurchaseDate    int64  `json:"purchaseDate"` // milliseconds
	ExpiresDate     int64  `json:"expiresDate"`  // milliseconds; subscriptions only
	AppAccountToken string `json:"appAccountToken"`
	RevocationDate  int64  `json:"revocationDate"`
	// OfferType 1 is an introductory offer; with OfferDiscountType
	// FREE_TRIAL, the one-week free trial.
	OfferType         int    `json:"offerType"`
	OfferDiscountType string `json:"offerDiscountType"`
	Environment       string `json:"environment"`
	Quantity          int    `json:"quantity"`
}

// Transaction types KIFF acts on.
const (
	TypeSubscription = "Auto-Renewable Subscription"
	TypeConsumable   = "Consumable"
)

// Purchased is when the transaction was bought.
func (t Transaction) Purchased() time.Time { return ms(t.PurchaseDate) }

// Expires is when a subscription's period ends.
func (t Transaction) Expires() time.Time { return ms(t.ExpiresDate) }

// Revoked reports that Apple refunded or revoked the transaction.
func (t Transaction) Revoked() bool { return t.RevocationDate > 0 }

// FreeTrial reports that the transaction is the introductory free trial.
func (t Transaction) FreeTrial() bool {
	return t.OfferType == 1 && (t.OfferDiscountType == "" || t.OfferDiscountType == "FREE_TRIAL")
}

func ms(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// Notification is Apple's responseBodyV2DecodedPayload.
type Notification struct {
	NotificationType string `json:"notificationType"`
	Subtype          string `json:"subtype"`
	NotificationUUID string `json:"notificationUUID"`
	SignedDate       int64  `json:"signedDate"`
	Data             struct {
		BundleID              string `json:"bundleId"`
		Environment           string `json:"environment"`
		SignedTransactionInfo string `json:"signedTransactionInfo"`
		SignedRenewalInfo     string `json:"signedRenewalInfo"`
	} `json:"data"`
}

// DecodeTransaction verifies a signed transaction and that it is this
// app's, in an accepted environment.
func (v *Verifier) DecodeTransaction(signed string) (Transaction, error) {
	var t Transaction
	if err := v.Decode(signed, &t); err != nil {
		return Transaction{}, err
	}
	if err := v.accepts(t.BundleID, t.Environment); err != nil {
		return Transaction{}, err
	}
	if t.TransactionID == "" || t.ProductID == "" {
		return Transaction{}, fmt.Errorf("%w: a transaction without its id or product", ErrInvalid)
	}
	return t, nil
}

// DecodeNotification verifies a notification's signed payload and the
// transaction it carries, if any.
func (v *Verifier) DecodeNotification(signedPayload string) (Notification, *Transaction, error) {
	var n Notification
	if err := v.Decode(signedPayload, &n); err != nil {
		return Notification{}, nil, err
	}
	if n.NotificationUUID == "" || n.NotificationType == "" {
		return Notification{}, nil, fmt.Errorf("%w: a notification without its id or type", ErrInvalid)
	}
	if err := v.accepts(n.Data.BundleID, n.Data.Environment); err != nil {
		return Notification{}, nil, err
	}
	if n.Data.SignedTransactionInfo == "" {
		return n, nil, nil
	}
	t, err := v.DecodeTransaction(n.Data.SignedTransactionInfo)
	if err != nil {
		return Notification{}, nil, err
	}
	return n, &t, nil
}
