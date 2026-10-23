package anchor

import (
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// The KiffAnchor ABI and bytecode are compiled from the contract's
// Solidity source. They are embedded here so a
// fresh clone runs the e2e tests with no external tooling.

//go:embed contract_abi.json
var contractABIJSON string

//go:embed contract_bin.txt
var contractBinHex string

// ContractABI is the parsed ABI for KiffAnchor. Used by both deploy
// and call paths.
var ContractABI abi.ABI

// ContractBytecode is the runtime bytecode for KiffAnchor as a raw
// byte slice. Used by deploy.
var ContractBytecode []byte

func init() {
	parsed, err := abi.JSON(strings.NewReader(contractABIJSON))
	if err != nil {
		panic(fmt.Errorf("anchor: parse embedded ABI: %w", err))
	}
	ContractABI = parsed

	trimmed := strings.TrimSpace(contractBinHex)
	bytecode, err := hex.DecodeString(trimmed)
	if err != nil {
		panic(fmt.Errorf("anchor: decode embedded bytecode: %w", err))
	}
	ContractBytecode = bytecode
}
