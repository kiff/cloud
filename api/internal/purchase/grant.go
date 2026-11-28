package purchase

import (
	"time"

	"github.com/kiff/cloud/api/internal/permit"
)

// GrantType is the execution grant's JOSE typ: a relay refuses any other,
// so a grant never passes as a permit, nor a permit as a grant.
const GrantType = "kiff-purchase+jwt"

// GrantClaims are what KIFF signs for the one execution of an approved
// case: the relay makes the case's card or token only from these, copied,
// never widened, and puts the case id and the grant's jti in the card's
// metadata. It expires with the case's decision.
type GrantClaims struct {
	Iss      string        `json:"iss"`
	Aud      string        `json:"aud"`
	Sub      string        `json:"sub"`
	Iat      int64         `json:"iat"`
	Exp      int64         `json:"exp"`
	Jti      string        `json:"jti"`
	Purchase GrantPurchase `json:"purchase"`
}

// GrantPurchase is the case as the relay executes it.
type GrantPurchase struct {
	Tenant     string   `json:"tenant"`
	CaseID     string   `json:"case_id"`
	Version    int      `json:"version"`
	Digest     string   `json:"digest"`
	State      string   `json:"state"`
	Path       string   `json:"path"`
	Holder     string   `json:"holder"`
	Cap        int64    `json:"cap"`
	Currency   string   `json:"currency"`
	Categories []string `json:"categories"`
	Seller     string   `json:"seller"`
	Delivery   Address  `json:"delivery"`
	ValidUntil string   `json:"valid_until"`
}

// MintGrant signs the execution grant for a case whose lease r's holder
// took: only an executing case gets one, and it never outlives the
// decision's valid_until.
func MintGrant(k permit.Key, iss, aud string, r Row, now time.Time) (string, string, error) {
	if r.State != StateExecuting || r.LeaseHolder == "" || !now.Before(r.ValidUntil) {
		return "", "", ErrNotHeld
	}
	c := r.Case
	jti := permit.NewJTI()
	tok, err := k.SignAs(GrantType, GrantClaims{
		Iss: iss, Aud: aud, Sub: c.Agent, Iat: now.Unix(), Exp: r.ValidUntil.Unix(), Jti: jti,
		Purchase: GrantPurchase{Tenant: c.Tenant, CaseID: c.CaseID, Version: c.Version, Digest: r.Digest, State: StateApproved, Path: c.Path,
			Holder: r.LeaseHolder, Cap: c.Grant.Cap, Currency: c.Currency, Categories: c.Grant.Categories, Seller: c.Merchant.Site,
			Delivery: c.Delivery, ValidUntil: r.ValidUntil.UTC().Format(time.RFC3339)},
	})
	return tok, jti, err
}
