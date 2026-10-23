package permit

import "time"

// HaltConfirmType is the typ of KIFF's ask that a planque confirm the
// owner's halt, signed with the permit key so planque
// checks it against the keys it already trusts. A permit verifier refuses
// it, its typ not being Type, so it can never be sent as a permit.
const HaltConfirmType = "kiff-halt-confirm+jwt"

// HaltConfirmHeader carries it on planque's
// POST /v1/accounts/{tenant}/halts/confirm.
const HaltConfirmHeader = "KIFF-Halt-Confirm"

// HaltConfirmLifetime is how long an ask is good for.
const HaltConfirmLifetime = 60 * time.Second

// HaltConfirm is the halt KIFF asks about: the account, the agent ("" for
// the whole account) and when it was halted.
type HaltConfirm struct {
	Iss      string `json:"iss"`
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
	Jti      string `json:"jti"`
	Tenant   string `json:"tenant"`
	Agent    string `json:"agent"`
	HaltedAt string `json:"halted_at"` // RFC 3339, as in the request's body
}

// SignHaltConfirm returns KIFF's ask, from iss at now, that a planque
// confirm agent's halt of tenant at haltedAt.
func (k Key) SignHaltConfirm(iss, tenant, agent string, haltedAt, now time.Time) (string, error) {
	return k.sign(HaltConfirmType, HaltConfirm{Iss: iss, Iat: now.Unix(), Exp: now.Add(HaltConfirmLifetime).Unix(), Jti: NewJTI(),
		Tenant: tenant, Agent: agent, HaltedAt: haltedAt.UTC().Format(time.RFC3339Nano)})
}
