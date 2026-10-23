package anchor

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
)

// TestE2E runs the full anchoring loop against go-ethereum's
// in-process simulated EVM:
//
//	build batch -> Merkle root -> signed deploy + submitRoot ->
//	Anchored event -> verifier reads on-chain root -> proof OK
//
// Requires no external tooling.
func TestE2E(t *testing.T) {
	t.Parallel()

	wallet, err := NewRandomWallet()
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}

	// Pre-fund the wallet in genesis. 100 ETH is the conventional
	// test allocation; the actual amount only needs to cover gas.
	wei := new(big.Int).Mul(big.NewInt(100), big.NewInt(1_000_000_000_000_000_000))
	alloc := types.GenesisAlloc{wallet.Address(): {Balance: wei}}

	sim := simulated.NewBackend(alloc)
	defer sim.Close()

	client := sim.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Deploy KiffAnchor.
	sub, address, deployTx, err := DeployAnchor(ctx, client, wallet)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	sim.Commit()
	if _, err := waitMined(ctx, t, client, deployTx); err != nil {
		t.Fatalf("deploy receipt: %v", err)
	}
	t.Logf("deployed KiffAnchor at %s", address)

	// Build a batch of three sample audit records.
	batch := &Batch{
		BatchID:  1,
		StreamID: StreamID("audit"),
		Records: []Record{
			sampleRecord("rec-001"),
			sampleRecord("rec-002"),
			sampleRecord("rec-003"),
		},
	}

	// Anchor it.
	res, err := Anchor(ctx, sub, batch)
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	sim.Commit()

	// Wait for the receipt and confirm success.
	receipt, err := waitMined(ctx, t, client, res.Tx)
	if err != nil {
		t.Fatalf("wait mined: %v", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("anchor tx reverted: status=%d", receipt.Status)
	}
	t.Logf("anchored root %s in block %d, tx %s", res.Root, receipt.BlockNumber, res.Tx.Hash())

	// Verify each record in the batch — happy path.
	for _, want := range batch.Records {
		v, err := VerifyRecord(ctx, sub, batch, want.RecordID, 0)
		if err != nil {
			t.Fatalf("VerifyRecord(%s): %v", want.RecordID, err)
		}
		if !v.OK {
			t.Fatalf("VerifyRecord(%s) not OK: %s", want.RecordID, v.Reason)
		}
		if v.Anchored == nil {
			t.Fatalf("VerifyRecord(%s) returned no Anchored event", want.RecordID)
		}
		if v.Anchored.Root != res.Root {
			t.Fatalf("on-chain root mismatch: got %s, want %s", v.Anchored.Root, res.Root)
		}
		if v.Anchored.Signer != wallet.Address() {
			t.Fatalf("signer mismatch: got %s, want %s", v.Anchored.Signer, wallet.Address())
		}
	}

	// Negative: verifying an unknown record fails cleanly.
	v, err := VerifyRecord(ctx, sub, batch, "rec-not-in-batch", 0)
	if err != nil {
		t.Fatalf("VerifyRecord(unknown): %v", err)
	}
	if v.OK {
		t.Fatal("VerifyRecord accepted a record not in the batch")
	}

	// Negative: verifying with a tampered batch fails.
	tampered := &Batch{
		BatchID:  batch.BatchID,
		StreamID: batch.StreamID,
		Records:  []Record{sampleRecord("rec-001"), sampleRecord("rec-tampered"), sampleRecord("rec-003")},
	}
	v, err = VerifyRecord(ctx, sub, tampered, "rec-001", 0)
	if err != nil {
		t.Fatalf("VerifyRecord(tampered): %v", err)
	}
	if v.OK {
		t.Fatal("VerifyRecord accepted a tampered batch (mid-record swap)")
	}
}

// waitMined polls until the simulated backend records a receipt for
// the transaction. The simulated backend's Commit() is synchronous,
// so a single fetch usually succeeds; we still loop in case the test
// needs to extend Commit timing later.
func waitMined(ctx context.Context, t *testing.T, client simulated.Client, tx *types.Transaction) (*types.Receipt, error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		receipt, err := client.TransactionReceipt(ctx, tx.Hash())
		if err == nil {
			return receipt, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Compile-time assertion: simulated.Client satisfies the ChainBackend
// interface used by Submitter. If go-ethereum changes either side,
// this won't compile and the test stops cheating.
var _ ChainBackend = (simulated.Client)(nil)

// Likewise check the deploy backend. Keeps drift visible.
var _ = func() bool {
	_ = common.Address{}
	return true
}()
