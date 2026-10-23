package anchor

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
)

// TestSubmitRoot_SequentialNoncesNoCollision submits several roots
// back-to-back without committing a block between them — the shape
// the anchor sweep produces against a public RPC. Before nonce
// management, each SubmitRoot re-fetched the same pending nonce
// and the second broadcast was rejected with "replacement
// transaction underpriced". With the Submitter's local nonce counter,
// the submits carry sequential nonces (n, n+1, n+2, ...) and all
// broadcast cleanly; a single Commit then mines them in order.
func TestSubmitRoot_SequentialNoncesNoCollision(t *testing.T) {
	t.Parallel()

	wallet, err := NewRandomWallet()
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	wei := new(big.Int).Mul(big.NewInt(100), big.NewInt(1_000_000_000_000_000_000))
	alloc := types.GenesisAlloc{wallet.Address(): {Balance: wei}}
	sim := simulated.NewBackend(alloc)
	defer sim.Close()
	client := sim.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Deploy and mine the contract so it exists for the submits.
	sub, _, deployTx, err := DeployAnchor(ctx, client, wallet)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	sim.Commit()
	if _, err := waitMined(ctx, t, client, deployTx); err != nil {
		t.Fatalf("deploy receipt: %v", err)
	}

	stream := StreamID("kiff.receipts")
	const n = 5
	txs := make([]*types.Transaction, 0, n)
	seenNonces := make(map[uint64]bool)

	// Submit n roots WITHOUT committing between them. Each is a
	// separate broadcast against the still-pending pool.
	for i := 0; i < n; i++ {
		var root Hash
		root[0] = byte(i + 1)
		tx, err := sub.SubmitRoot(ctx, root, uint64(i+1), stream)
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if seenNonces[tx.Nonce()] {
			t.Fatalf("submit %d reused nonce %d", i, tx.Nonce())
		}
		seenNonces[tx.Nonce()] = true
		txs = append(txs, tx)
	}

	// Nonces must be a contiguous ascending run.
	for i := 1; i < len(txs); i++ {
		if txs[i].Nonce() != txs[i-1].Nonce()+1 {
			t.Fatalf("nonces not sequential: tx[%d]=%d tx[%d]=%d",
				i-1, txs[i-1].Nonce(), i, txs[i].Nonce())
		}
	}

	// Mine them and confirm each was included successfully.
	sim.Commit()
	for i, tx := range txs {
		rcpt, err := waitMined(ctx, t, client, tx)
		if err != nil {
			t.Fatalf("wait mined %d: %v", i, err)
		}
		if rcpt.Status != types.ReceiptStatusSuccessful {
			t.Fatalf("submit %d reverted: status=%d", i, rcpt.Status)
		}
	}
}

// TestSubmitRoot_NonceResyncAfterReset verifies the counter recovers
// after an error drops it: a submit on a fresh Submitter resyncs from
// the node, and after a successful commit the next submit continues
// from the node's advanced pending nonce rather than a stale local
// value.
func TestSubmitRoot_NonceResyncAfterReset(t *testing.T) {
	t.Parallel()

	wallet, err := NewRandomWallet()
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	wei := new(big.Int).Mul(big.NewInt(100), big.NewInt(1_000_000_000_000_000_000))
	alloc := types.GenesisAlloc{wallet.Address(): {Balance: wei}}
	sim := simulated.NewBackend(alloc)
	defer sim.Close()
	client := sim.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sub, _, deployTx, err := DeployAnchor(ctx, client, wallet)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	sim.Commit()
	if _, err := waitMined(ctx, t, client, deployTx); err != nil {
		t.Fatalf("deploy receipt: %v", err)
	}

	stream := StreamID("kiff.receipts")

	// First submit resyncs from the node (cached nonce nil after
	// construction).
	var root1 Hash
	root1[0] = 1
	tx1, err := sub.SubmitRoot(ctx, root1, 1, stream)
	if err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	sim.Commit()
	if _, err := waitMined(ctx, t, client, tx1); err != nil {
		t.Fatalf("wait mined 1: %v", err)
	}

	// Force a resync by clearing the cached nonce (simulating the
	// post-error reset path). The next submit must pull the node's
	// advanced pending nonce, not reuse tx1's.
	sub.mu.Lock()
	sub.nonce = nil
	sub.mu.Unlock()

	var root2 Hash
	root2[0] = 2
	tx2, err := sub.SubmitRoot(ctx, root2, 2, stream)
	if err != nil {
		t.Fatalf("submit 2: %v", err)
	}
	if tx2.Nonce() <= tx1.Nonce() {
		t.Fatalf("resync did not advance: tx1 nonce=%d tx2 nonce=%d", tx1.Nonce(), tx2.Nonce())
	}
	sim.Commit()
	if _, err := waitMined(ctx, t, client, tx2); err != nil {
		t.Fatalf("wait mined 2: %v", err)
	}
}
