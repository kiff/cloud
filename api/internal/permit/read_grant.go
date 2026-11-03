package permit

import "time"

// ReadGrantType is the typ of a read grant: KIFF's word that a person may
// read another person's work with an agent, for one stated purpose,
// which KIFF recorded before signing it. Neither a permit nor a run grant
// verifies as one.
const ReadGrantType = "kiff-read-grant+jwt"

// ReadGrantLifetime is how long a read grant is good for: one look, not
// a standing view. Reading again asks, and is recorded, again.
const ReadGrantLifetime = 10 * time.Minute

// ReadGrant is one read KIFF allowed: who reads (Reader) whose work
// (Owner) with which agent, one session or, empty, the list of their
// sessions, and why.
type ReadGrant struct {
	Iss     string `json:"iss"`
	Iat     int64  `json:"iat"`
	Exp     int64  `json:"exp"`
	Jti     string `json:"jti"`
	Tenant  string `json:"tenant"`
	Agent   string `json:"agent"`
	Reader  string `json:"reader"`
	Owner   string `json:"owner"`
	Session string `json:"session,omitempty"`
	Purpose string `json:"purpose"`
}

// SignReadGrant returns a read grant from iss at now, with jti the read
// KIFF recorded.
func (k Key) SignReadGrant(iss string, g ReadGrant, now time.Time) (string, ReadGrant, error) {
	g.Iss, g.Iat, g.Exp = iss, now.Unix(), now.Add(ReadGrantLifetime).Unix()
	if g.Jti == "" {
		g.Jti = NewJTI()
	}
	tok, err := k.sign(ReadGrantType, g)
	return tok, g, err
}

// VerifyReadGrant checks a read grant against KIFF's keys and its expiry,
// and returns what it says.
func VerifyReadGrant(token string, keys []PublicKey, now time.Time) (ReadGrant, error) {
	var g ReadGrant
	if err := verify(token, ReadGrantType, keys, &g); err != nil {
		return ReadGrant{}, err
	}
	if g.Tenant == "" || g.Agent == "" || g.Reader == "" || g.Owner == "" || g.Purpose == "" || now.Unix() >= g.Exp {
		return ReadGrant{}, ErrInvalidToken
	}
	return g, nil
}
