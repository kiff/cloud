package auth_test

import "crypto/tls"

// newDummyTLS returns a non-nil ConnectionState so tests can mark
// requests as TLS without standing up a real TLS endpoint. Used
// by the ClerkProvider tests to confirm the absoluteCallback
// scheme picks "https" when r.TLS is set.
func newDummyTLS() *tls.ConnectionState {
	return &tls.ConnectionState{}
}
