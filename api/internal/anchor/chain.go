package anchor

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ChainBackend is the subset of go-ethereum's client interface we
// need. The simulated backend's Client and the real ethclient.Client
// both satisfy it; tests inject the simulated one.
type ChainBackend interface {
	bind.ContractBackend
	bind.DeployBackend
	ethereum.LogFilterer
	ethereum.ChainIDReader
}

// Submitter publishes Merkle roots to a deployed KiffAnchor contract.
//
// Nonce management: go-ethereum's NewKeyedTransactorWithChainID
// leaves auth.Nonce nil, so each Transact re-fetches the pending nonce
// from the node. Under the anchor sweep — many submitRoot calls
// back-to-back — the node has not yet seen the prior transaction in
// its pending pool, returns the same nonce, and the second submit is
// rejected with "replacement transaction underpriced". Submitter
// serializes submits and tracks the nonce locally: it sets auth.Nonce
// from a cached counter, advances on success, and resyncs from
// PendingNonceAt on error. Combined with the receipt package's
// ThrottledAttester pacing, this keeps the submit sequence both spaced
// and correctly nonced.
type Submitter struct {
	backend  ChainBackend
	wallet   *Wallet
	contract *bind.BoundContract
	address  common.Address
	chainID  *big.Int

	mu    sync.Mutex
	nonce *big.Int // next nonce to use; nil means "resync from node"
}

// DeployAnchor deploys a fresh KiffAnchor contract using the demo
// wallet. Returns the deployed address and a Submitter that wraps it.
// The simulated backend caller must Commit() between submitting the
// deploy transaction and observing it; production callers wait for
// confirmations via TransactionReceipt.
func DeployAnchor(ctx context.Context, backend ChainBackend, wallet *Wallet) (*Submitter, common.Address, *types.Transaction, error) {
	chainID, err := backend.ChainID(ctx)
	if err != nil {
		return nil, common.Address{}, nil, fmt.Errorf("anchor: chain id: %w", err)
	}

	auth, err := bind.NewKeyedTransactorWithChainID(wallet.PrivateKey(), chainID)
	if err != nil {
		return nil, common.Address{}, nil, fmt.Errorf("anchor: transactor: %w", err)
	}
	auth.Context = ctx

	address, tx, contract, err := bind.DeployContract(auth, ContractABI, ContractBytecode, backend)
	if err != nil {
		return nil, common.Address{}, nil, fmt.Errorf("anchor: deploy: %w", err)
	}
	return &Submitter{
		backend:  backend,
		wallet:   wallet,
		contract: contract,
		address:  address,
		chainID:  chainID,
	}, address, tx, nil
}

// AttachAnchor binds to an existing KiffAnchor at the given address.
// Used when the contract is deployed once and reused (real chain
// path).
func AttachAnchor(ctx context.Context, backend ChainBackend, wallet *Wallet, address common.Address) (*Submitter, error) {
	if (address == common.Address{}) {
		return nil, errors.New("anchor: zero address")
	}
	chainID, err := backend.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("anchor: chain id: %w", err)
	}
	contract := bind.NewBoundContract(address, ContractABI, backend, backend, backend)
	return &Submitter{
		backend:  backend,
		wallet:   wallet,
		contract: contract,
		address:  address,
		chainID:  chainID,
	}, nil
}

// Address returns the deployed contract address.
func (s *Submitter) Address() common.Address { return s.address }

// SubmitRoot calls KiffAnchor.submitRoot. Returns the broadcast
// transaction; the caller decides whether to wait for inclusion.
//
// Submits are serialized and locally nonced (see the Submitter doc):
// the cached nonce is set on the transactor, advanced on a successful
// broadcast, and dropped on error so the next call resyncs from the
// node's pending nonce. This eliminates the same-nonce collision that
// the public-RPC anchor sweep would otherwise hit.
func (s *Submitter) SubmitRoot(ctx context.Context, root Hash, batchID uint64, streamID Hash) (*types.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	auth, err := bind.NewKeyedTransactorWithChainID(s.wallet.PrivateKey(), s.chainID)
	if err != nil {
		return nil, fmt.Errorf("anchor: transactor: %w", err)
	}
	auth.Context = ctx

	// Resync the local nonce from the node when we don't hold one
	// (first submit, or after a prior error reset it).
	if s.nonce == nil {
		pending, err := s.backend.PendingNonceAt(ctx, s.wallet.Address())
		if err != nil {
			return nil, fmt.Errorf("anchor: pending nonce: %w", err)
		}
		s.nonce = new(big.Int).SetUint64(pending)
	}
	auth.Nonce = new(big.Int).Set(s.nonce)

	rootArg := common.Hash(root)
	streamArg := common.Hash(streamID)
	tx, err := s.contract.Transact(auth, "submitRoot", rootArg, batchID, streamArg)
	if err != nil {
		// Drop the cached nonce so the next submit resyncs from the
		// node rather than reusing a value the node may have rejected
		// or already advanced past.
		s.nonce = nil
		return nil, fmt.Errorf("anchor: submit root: %w", err)
	}
	// Advance for the next submit.
	s.nonce = new(big.Int).Add(s.nonce, big.NewInt(1))
	return tx, nil
}

// AnchoredEvent is the parsed shape of the contract's Anchored event.
type AnchoredEvent struct {
	Signer   common.Address
	StreamID Hash
	BatchID  uint64
	Root     Hash
	TxHash   common.Hash
	Block    uint64
}

// FilterAnchored returns Anchored events from the contract whose
// (streamId, batchId) match the filter. Used by kiff-verify to fetch
// the on-chain root for a known batch.
func (s *Submitter) FilterAnchored(ctx context.Context, fromBlock uint64, streamID Hash, batchID uint64) ([]AnchoredEvent, error) {
	event := ContractABI.Events["Anchored"]

	streamTopic := common.Hash(streamID)
	var batchTopic common.Hash
	// uint64 occupies the rightmost 8 bytes of a 32-byte topic.
	for i := 0; i < 8; i++ {
		batchTopic[31-i] = byte(batchID >> (8 * i))
	}

	query := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(fromBlock),
		Addresses: []common.Address{s.address},
		Topics: [][]common.Hash{
			{event.ID},
			nil, // any signer
			{streamTopic},
			{batchTopic},
		},
	}

	logs, err := s.backend.FilterLogs(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("anchor: filter logs: %w", err)
	}

	out := make([]AnchoredEvent, 0, len(logs))
	for _, l := range logs {
		// Topic[1] = signer (indexed address)
		// Topic[2] = streamId (indexed bytes32)
		// Topic[3] = batchId  (indexed uint64)
		// Data     = root     (non-indexed bytes32)
		if len(l.Topics) < 4 || len(l.Data) < 32 {
			continue
		}
		var rootBytes Hash
		copy(rootBytes[:], l.Data[:32])
		var streamBytes Hash
		copy(streamBytes[:], l.Topics[2][:])

		ev := AnchoredEvent{
			Signer:   common.BytesToAddress(l.Topics[1][:]),
			StreamID: streamBytes,
			BatchID:  batchID, // verified by topic match
			Root:     rootBytes,
			TxHash:   l.TxHash,
			Block:    l.BlockNumber,
		}
		out = append(out, ev)
	}
	return out, nil
}
