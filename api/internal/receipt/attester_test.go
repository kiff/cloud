package receipt_test

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/anchor"
	"github.com/kiff/cloud/api/internal/receipt"
)

// fixedClock returns a deterministic clock for attestation tests so
// the AnchoredAt timestamps are predictable.
func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

func TestLocalAttester_Attest_PopulatesAllFields(t *testing.T) {
	t.Parallel()

	wallet, err := anchor.NewRandomWallet()
	if err != nil {
		t.Fatalf("NewRandomWallet: %v", err)
	}

	at := time.Date(2026, time.May, 24, 12, 30, 0, 0, time.UTC)
	attester := receipt.NewLocalAttester(wallet, fixedClock(at))

	traceID := "trace-attest-001"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten-demo",
		TraceID:  traceID,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	att, err := attester.Attest(context.Background(), r)
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}
	if att == nil {
		t.Fatal("Attestation: got nil")
	}
	if att.Algorithm != receipt.AttestationAlgorithm {
		t.Errorf("Algorithm: got %q, want %q", att.Algorithm, receipt.AttestationAlgorithm)
	}
	if att.Signature == "" {
		t.Errorf("Signature: empty")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(att.Signature, "0x")); err != nil {
		t.Errorf("Signature not hex: %v", err)
	}
	if att.WalletAddress == "" {
		t.Errorf("WalletAddress: empty")
	}
	if got, want := att.WalletAddress, strings.ToLower(wallet.Address().Hex()); got != want {
		t.Errorf("WalletAddress: got %q, want %q", got, want)
	}
	if att.AnchorTxHash != "" {
		t.Errorf("AnchorTxHash: got %q, want empty (LocalAttester does not anchor)", att.AnchorTxHash)
	}
	if !att.AnchoredAt.Equal(at) {
		t.Errorf("AnchoredAt: got %s, want %s", att.AnchoredAt, at)
	}
}

func TestVerifyAttestation_RoundTrip(t *testing.T) {
	t.Parallel()

	// Sign with a wallet, verify with VerifyAttestation. Confirm
	// the recovered signer matches the wallet and the call returns
	// nil error.
	wallet, _ := anchor.NewRandomWallet()
	attester := receipt.NewLocalAttester(wallet, nil)

	traceID := "trace-verify-001"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten-demo",
		TraceID:  traceID,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	att, err := attester.Attest(context.Background(), r)
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}

	if err := receipt.VerifyAttestation(r, att); err != nil {
		t.Errorf("VerifyAttestation: %v", err)
	}
}

func TestVerifyAttestation_DetectsTamperedReceipt(t *testing.T) {
	t.Parallel()

	// Sign over the original receipt, then verify against a
	// tampered receipt with one chain step's data hash mutated.
	// Verification must fail because the recovered signer no longer
	// matches the declared wallet (the digest changed underneath).
	wallet, _ := anchor.NewRandomWallet()
	attester := receipt.NewLocalAttester(wallet, nil)

	traceID := "trace-tamper-001"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	original, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten-demo",
		TraceID:  traceID,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	att, err := attester.Attest(context.Background(), original)
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}

	tampered := original
	tampered.CausalChain = append([]receipt.ChainStep(nil), original.CausalChain...)
	tampered.CausalChain[len(tampered.CausalChain)-1].DataHash = strings.Repeat("ee", 32)

	err = receipt.VerifyAttestation(tampered, att)
	if err == nil {
		t.Fatal("VerifyAttestation: got nil, want error for tampered receipt")
	}
	if !errors.Is(err, receipt.ErrAttestationWalletAddr) {
		t.Errorf("error: got %v, want ErrAttestationWalletAddr", err)
	}
}

func TestVerifyAttestation_RejectsWrongAlgorithm(t *testing.T) {
	t.Parallel()

	wallet, _ := anchor.NewRandomWallet()
	attester := receipt.NewLocalAttester(wallet, nil)

	traceID := "trace-alg-001"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	r, _ := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten-demo",
		TraceID:  traceID,
	})
	att, _ := attester.Attest(context.Background(), r)
	att.Algorithm = "ed25519"

	err := receipt.VerifyAttestation(r, att)
	if !errors.Is(err, receipt.ErrAttestationAlgorithm) {
		t.Errorf("error: got %v, want ErrAttestationAlgorithm", err)
	}
}

func TestVerifyAttestation_RejectsMalformedSignature(t *testing.T) {
	t.Parallel()

	r := receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion,
		TenantID:      "ten-demo",
		TraceID:       "trace-malformed",
	}
	cases := []struct {
		name string
		sig  string
	}{
		{"empty", ""},
		{"too short", "deadbeef"},
		{"not hex", "not-a-hex-string-at-all-zzz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := receipt.VerifyAttestation(r, &receipt.Attestation{
				Algorithm:     receipt.AttestationAlgorithm,
				Signature:     tc.sig,
				WalletAddress: "0xabc",
			})
			if !errors.Is(err, receipt.ErrAttestationSignature) {
				t.Errorf("got %v, want ErrAttestationSignature", err)
			}
		})
	}
}

func TestVerifyAttestation_RejectsMissingAttestation(t *testing.T) {
	t.Parallel()

	err := receipt.VerifyAttestation(receipt.Receipt{}, nil)
	if !errors.Is(err, receipt.ErrAttestationMissing) {
		t.Errorf("got %v, want ErrAttestationMissing", err)
	}
}

func TestDeterministicBatchID_StableAndDistinct(t *testing.T) {
	t.Parallel()

	a := receipt.Receipt{TenantID: "ten-1", TraceID: "trace-A"}
	b := receipt.Receipt{TenantID: "ten-1", TraceID: "trace-A"}
	c := receipt.Receipt{TenantID: "ten-1", TraceID: "trace-B"}
	d := receipt.Receipt{TenantID: "ten-2", TraceID: "trace-A"}

	if receipt.DeterministicBatchID(a) != receipt.DeterministicBatchID(b) {
		t.Errorf("batch id not stable for identical receipts")
	}
	if receipt.DeterministicBatchID(a) == receipt.DeterministicBatchID(c) {
		t.Errorf("batch id collides across distinct traces")
	}
	if receipt.DeterministicBatchID(a) == receipt.DeterministicBatchID(d) {
		t.Errorf("batch id collides across distinct tenants")
	}
}
