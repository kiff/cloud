package receipt

// CanonicalChainJSONForTest exposes the canonical encoder to the
// external test package. Test-only: this file is not compiled into the
// package binary, so the encoder stays unexported in production.
//
// The assertion has to run against the real encoder. A test that
// reimplements canonicalisation proves the reimplementation is stable,
// which is not the property anyone needs.
func CanonicalChainJSONForTest(chain []ChainStep) string {
	b, err := canonicalJSON(chain)
	if err != nil {
		return "canonicalJSON error: " + err.Error()
	}
	return string(b)
}
