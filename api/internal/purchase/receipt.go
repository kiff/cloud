package purchase

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/kiff/cloud/api/internal/permit"
)

// ReceiptSchema is the signed receipt's schema id.
const ReceiptSchema = "kiff.purchase-receipt.v1"

// Receipt is KIFF's signed account of a purchase: what was decided and what
// happened, unknown said as unknown. Sig is Ed25519 over the canonical JSON
// of the receipt without Sig.
type Receipt struct {
	Schema       string            `json:"schema"`
	ReceiptID    string            `json:"receipt_id"`
	CaseID       string            `json:"case_id"`
	Version      int               `json:"version"`
	Digest       string            `json:"digest"`
	Path         string            `json:"path"`
	DecidedBy    string            `json:"decided_by"`
	Approver     Approver          `json:"approver"`
	Merchant     ReceiptMerchant   `json:"merchant"`
	Amounts      Amounts           `json:"amounts"`
	States       States            `json:"states"`
	Order        *Order            `json:"order,omitempty"`
	Verification string            `json:"verification"`
	IssuedAt     string            `json:"issued_at"`
	Words        map[string]string `json:"words"`
	Kid          string            `json:"kid"`
	Sig          string            `json:"sig,omitempty"`
}

// Approver is who decided, as the reader sees it.
type Approver struct {
	Role   string `json:"role"`
	Self   bool   `json:"self"`
	Method string `json:"method"`
}

// ReceiptMerchant puts the issuer's merchant beside the case's, so a
// mismatch is visible.
type ReceiptMerchant struct {
	Case   NameCategory `json:"case"`
	Issuer NameCategory `json:"issuer"`
}

// NameCategory is a merchant's name and category.
type NameCategory struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

// Amounts are the receipt's figures, in minor units.
type Amounts struct {
	ApprovedMax int64  `json:"approved_max"`
	Authorized  int64  `json:"authorized"`
	Captured    int64  `json:"captured"`
	Refunded    int64  `json:"refunded"`
	Currency    string `json:"currency"`
}

// States are the three statuses, kept apart.
type States struct {
	Case  string `json:"case"`
	Money string `json:"money"`
	Order string `json:"order"`
}

// Order is the order the shop confirmed.
type Order struct {
	Reference   string `json:"reference"`
	ConfirmedAt string `json:"confirmed_at"`
}

// SignReceipt signs r with KIFF's receipt key, over its canonical JSON
// without the signature.
func SignReceipt(k permit.Key, r Receipt, at time.Time) (Receipt, error) {
	r.Schema, r.Kid, r.Sig = ReceiptSchema, k.ID, ""
	if r.IssuedAt == "" {
		r.IssuedAt = at.UTC().Format(time.RFC3339)
	}
	msg, err := receiptBytes(r)
	if err != nil {
		return Receipt{}, err
	}
	r.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(k.Priv, msg))
	return r, nil
}

// VerifyReceipt checks r's signature with KIFF's published key.
func VerifyReceipt(pub permit.PublicKey, r Receipt) error {
	if r.Kid != pub.ID {
		return errors.New("purchase: the receipt names another key")
	}
	sig, err := base64.RawURLEncoding.DecodeString(r.Sig)
	if err != nil {
		return errors.New("purchase: the receipt's signature isn't base64url")
	}
	r.Sig = ""
	msg, err := receiptBytes(r)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub.Pub, msg, sig) {
		return errors.New("purchase: the receipt's signature doesn't hold")
	}
	return nil
}

func receiptBytes(r Receipt) ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return permit.Canonicalize(b)
}
