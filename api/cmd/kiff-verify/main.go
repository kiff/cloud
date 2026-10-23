// Package main is the kiff-verify CLI.
//
// kiff-verify proves that an audit record is committed to a Merkle
// root that has been anchored on-chain by KiffAnchor. This is the PoC
// verifier: it accepts a JSON batch on stdin (the off-chain context
// the cloud holds) plus an RPC URL and the contract address, and prints OK with the on-chain transaction hash if the
// proof verifies.
//
// Production form reads the batch from a sidecar table
// keyed by record id and resolves the contract address from the
// cloud's tenant registry; the PoC asks the user for both.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/kiff/cloud/api/internal/anchor"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run is the testable entrypoint. Returns nil on a successful proof
// (record OK), otherwise an error with a human-readable reason.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("kiff-verify", flag.ContinueOnError)
	fs.SetOutput(stderr)

	rpcURL := fs.String("rpc", "", "Ethereum JSON-RPC URL (Base Sepolia or simulated chain)")
	contractAddr := fs.String("contract", "", "KiffAnchor contract address (0x...)")
	recordID := fs.String("record-id", "", "RecordID to verify (must appear in the batch read from stdin)")
	batchPath := fs.String("batch", "-", "Path to the JSON batch (default '-' = stdin)")
	saltHex := fs.String("salt", "", "Per-tenant salt (hex, with or without 0x prefix). Overrides any Salt embedded in the batch JSON. Required for salted batches if the JSON envelope omits Salt.")
	fromBlock := fs.Uint64("from-block", 0, "Earliest block to scan for the Anchored event")
	timeoutSec := fs.Uint("timeout", 30, "RPC timeout in seconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *rpcURL == "" || *contractAddr == "" || *recordID == "" {
		fs.Usage()
		return errors.New("required flags: --rpc, --contract, --record-id")
	}
	if !common.IsHexAddress(*contractAddr) {
		return fmt.Errorf("invalid contract address: %s", *contractAddr)
	}

	batch, err := readBatch(*batchPath, stdin)
	if err != nil {
		return fmt.Errorf("read batch: %w", err)
	}

	// --salt overrides whatever the batch JSON carried. The flag
	// exists so an operator can verify a tenant's anchor without
	// embedding the salt in the batch artifact (the salt is the
	// cloud's secret; the batch metadata is not). When neither
	// the flag nor the envelope provides a salt, the verifier
	// runs against the unsalted leaf form, which is correct for
	// any batch anchored before salts were added.
	if *saltHex != "" {
		salt, err := decodeHexSalt(*saltHex)
		if err != nil {
			return fmt.Errorf("--salt: %w", err)
		}
		batch.Salt = salt
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	client, err := ethclient.DialContext(ctx, *rpcURL)
	if err != nil {
		return fmt.Errorf("dial rpc: %w", err)
	}
	defer client.Close()

	// kiff-verify does not need a wallet; it only reads logs. We pass
	// a throwaway random wallet because AttachAnchor's signature
	// expects one.
	w, err := anchor.NewRandomWallet()
	if err != nil {
		return fmt.Errorf("wallet: %w", err)
	}

	sub, err := anchor.AttachAnchor(ctx, client, w, common.HexToAddress(*contractAddr))
	if err != nil {
		return fmt.Errorf("attach: %w", err)
	}

	res, err := anchor.VerifyRecord(ctx, sub, batch, *recordID, *fromBlock)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if !res.OK {
		fmt.Fprintf(stdout, "FAIL: %s\n", res.Reason)
		if res.Anchored != nil {
			fmt.Fprintf(stdout, "  on-chain root: %s\n", res.Anchored.Root)
			fmt.Fprintf(stdout, "  signer:        %s\n", res.Anchored.Signer.Hex())
			fmt.Fprintf(stdout, "  tx:            %s\n", res.Anchored.TxHash.Hex())
		}
		return errors.New("verification failed")
	}

	fmt.Fprintf(stdout, "OK\n")
	fmt.Fprintf(stdout, "  record:        %s\n", *recordID)
	fmt.Fprintf(stdout, "  leaf index:    %d\n", res.LeafIndex)
	fmt.Fprintf(stdout, "  on-chain root: %s\n", res.Root)
	fmt.Fprintf(stdout, "  signer:        %s\n", res.Anchored.Signer.Hex())
	fmt.Fprintf(stdout, "  block:         %d\n", res.Anchored.Block)
	fmt.Fprintf(stdout, "  tx:            %s\n", res.Anchored.TxHash.Hex())
	fmt.Fprintf(stdout, "  explorer:      https://sepolia.basescan.org/tx/%s\n", res.Anchored.TxHash.Hex())
	return nil
}

func readBatch(path string, stdin io.Reader) (*anchor.Batch, error) {
	var src io.Reader
	switch path {
	case "", "-":
		src = stdin
	default:
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		src = f
	}

	var batch anchor.Batch
	if err := json.NewDecoder(src).Decode(&batch); err != nil {
		return nil, err
	}
	return &batch, nil
}

// decodeHexSalt parses a salt provided on the command line. Accepts
// the value with or without a 0x prefix; rejects anything that
// isn't a clean even-length hex string. Length is otherwise
// unconstrained — kiff-verify is the consumer of whatever salt the
// cloud emitted, and the cloud is the authority on size.
func decodeHexSalt(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	if s == "" {
		return nil, errors.New("salt is empty")
	}
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("salt has odd hex length: %d", len(s))
	}
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	return out, nil
}
