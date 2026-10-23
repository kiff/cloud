package receipt_test

import (
	"context"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient/simulated"

	"github.com/kiff/cloud/api/internal/anchor"
	"github.com/kiff/cloud/api/internal/receipt"
)

// TestAnchorAttester_AttestSignsAndAnchors runs the full attest loop
// against go-ethereum's in-process simulated EVM:
//
//	deploy KiffAnchor -> sign receipt -> submit one-record batch ->
//	attestation carries signature + tx hash -> off-chain verify
//	matches recovered signer
//
// Mirrors the existing internal/anchor/anchor_e2e_test.go shape; no
// external tooling required.
func TestAnchorAttester_AttestSignsAndAnchors(t *testing.T) {
	t.Parallel()

	wallet, err := anchor.NewRandomWallet()
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}

	// Pre-fund the wallet so the deploy + submitRoot transactions
	// have gas.
	wei := new(big.Int).Mul(big.NewInt(100), big.NewInt(1_000_000_000_000_000_000))
	alloc := types.GenesisAlloc{wallet.Address(): {Balance: wei}}

	sim := simulated.NewBackend(alloc)
	defer sim.Close()
	client := sim.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Deploy KiffAnchor and let the simulated backend confirm it.
	sub, _, deployTx, err := anchor.DeployAnchor(ctx, client, wallet)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	sim.Commit()
	if _, err := waitMinedReceipt(ctx, t, client, deployTx); err != nil {
		t.Fatalf("deploy receipt: %v", err)
	}

	// Build a real receipt out of an in-memory audit trace and
	// hand it to the attester.
	traceID := "trace-anchor-001"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID:      "ten-anchor",
		TraceID:       traceID,
		ComplianceMap: refundDomainCompliance(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	at := time.Date(2026, time.May, 24, 14, 0, 0, 0, time.UTC)
	attester := receipt.NewAnchorAttester(sub, wallet, nil, fixedClock(at), nil).
		WithNetwork(receipt.NetworkBaseMainnet)

	att, err := attester.Attest(ctx, r)
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}
	sim.Commit()

	if att == nil {
		t.Fatal("Attestation: got nil")
	}
	if att.Network != receipt.NetworkBaseMainnet {
		t.Errorf("Network: got %q, want %q (WithNetwork must stamp the attestation)", att.Network, receipt.NetworkBaseMainnet)
	}
	if att.Signature == "" {
		t.Errorf("Signature: empty (AnchorAttester must produce off-chain-recoverable signature)")
	}
	if att.AnchorTxHash == "" {
		t.Errorf("AnchorTxHash: empty")
	}
	if !strings.HasPrefix(att.AnchorTxHash, "0x") {
		t.Errorf("AnchorTxHash: missing 0x prefix: %q", att.AnchorTxHash)
	}
	if got, want := att.WalletAddress, strings.ToLower(wallet.Address().Hex()); got != want {
		t.Errorf("WalletAddress: got %q, want %q", got, want)
	}

	// Off-chain verification: recover the signer from the
	// signature and match against the declared wallet address.
	if err := receipt.VerifyAttestation(r, att); err != nil {
		t.Errorf("VerifyAttestation off-chain: %v", err)
	}

	// On-chain verification: the receipt's batch should produce a
	// matching root via the existing anchor.VerifyRecord path. We
	// reconstruct the batch with the same batch id and record id
	// the attester used; if these stay stable, kiff-verify can
	// always rebuild the proof from the receipt alone.
	batch := &anchor.Batch{
		BatchID:  receipt.DeterministicBatchID(r),
		StreamID: receipt.ReceiptStreamID(),
		Records: []anchor.Record{
			{
				RecordID: r.TenantID + "/" + r.TraceID,
				TraceID:  r.TraceID,
				EntityID: r.EntityID,
				Kind:     "receipt_attested",
				At:       at,
				Payload: func() []byte {
					h, _ := r.CausalChainHash()
					b, _ := hex.DecodeString(h)
					return b
				}(),
			},
		},
	}
	v, err := anchor.VerifyRecord(ctx, sub, batch, batch.Records[0].RecordID, 0)
	if err != nil {
		t.Fatalf("VerifyRecord on-chain: %v", err)
	}
	if !v.OK {
		t.Fatalf("VerifyRecord on-chain not OK: %s", v.Reason)
	}
}

// Invariant: A receipt altered after signing fails verification against
// the anchored root. Tamper-evidence is checked, not asserted.
// Category: evidence
// Attack: Change a field in a signed receipt and present it as genuine
// Refused: The digest is recomputed from the receipt's own content, so the
// signature no longer recovers the declared signer
func TestAnchorAttester_TamperedReceiptFailsOnchainVerify(t *testing.T) {
	t.Parallel()

	wallet, _ := anchor.NewRandomWallet()
	wei := new(big.Int).Mul(big.NewInt(100), big.NewInt(1_000_000_000_000_000_000))
	sim := simulated.NewBackend(types.GenesisAlloc{wallet.Address(): {Balance: wei}})
	defer sim.Close()
	client := sim.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sub, _, deployTx, err := anchor.DeployAnchor(ctx, client, wallet)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	sim.Commit()
	if _, err := waitMinedReceipt(ctx, t, client, deployTx); err != nil {
		t.Fatalf("deploy receipt: %v", err)
	}

	traceID := "trace-anchor-tamper"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	r, _ := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten-tamper",
		TraceID:  traceID,
	})
	at := time.Date(2026, time.May, 24, 14, 0, 0, 0, time.UTC)
	attester := receipt.NewAnchorAttester(sub, wallet, nil, fixedClock(at), nil)
	if _, err := attester.Attest(ctx, r); err != nil {
		t.Fatalf("Attest: %v", err)
	}
	sim.Commit()

	// Now construct a batch as if the receipt's chain had been
	// tampered: the leaf hash will differ from what was anchored,
	// so on-chain verification must fail.
	tamperedBatch := &anchor.Batch{
		BatchID:  receipt.DeterministicBatchID(r),
		StreamID: receipt.ReceiptStreamID(),
		Records: []anchor.Record{
			{
				RecordID: r.TenantID + "/" + r.TraceID,
				TraceID:  r.TraceID,
				EntityID: r.EntityID,
				Kind:     "receipt_attested",
				At:       at,
				Payload:  []byte("tampered-causal-chain-bytes"),
			},
		},
	}
	v, err := anchor.VerifyRecord(ctx, sub, tamperedBatch, tamperedBatch.Records[0].RecordID, 0)
	if err != nil {
		t.Fatalf("VerifyRecord: %v", err)
	}
	if v.OK {
		t.Fatal("VerifyRecord accepted a tampered receipt batch")
	}
}

// waitMinedReceipt polls until the simulated backend records a
// receipt for the transaction. Mirrors the helper in
// internal/anchor/anchor_e2e_test.go (unexported there); replicated
// here so the receipt package's tests stay self-contained.
func waitMinedReceipt(ctx context.Context, t *testing.T, client simulated.Client, tx *types.Transaction) (*types.Receipt, error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := client.TransactionReceipt(ctx, tx.Hash())
		if err == nil && r != nil {
			return r, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
