package handlers

import (
	"crypto/ecdsa"
	"encoding/json"
	"testing"
)

// Test-only seams for handlers_test; this file is compiled only with tests.

// WithProposalsBlockVerifier checks blocks' signatures with verify in place
// of planque's pinned key, so a test can sign its own.
func WithProposalsBlockVerifier(verify func(json.RawMessage, string) bool) ProposalsOption {
	return func(h *ProposalsHandler) { h.verifyBlock = verify }
}

// SignedBlock is a block signed by key, as planque signs one (testBlock).
func SignedBlock(t *testing.T, key *ecdsa.PrivateKey, tenant, proposal string, edit func(map[string]any)) json.RawMessage {
	return testBlock(t, key, tenant, proposal, edit)
}

// VerifyWith is a block verifier for key's signatures under planque's label.
func VerifyWith(key *ecdsa.PublicKey) func(json.RawMessage, string) bool {
	return func(block json.RawMessage, sig string) bool { return verifyWith(key, block, sig) }
}
