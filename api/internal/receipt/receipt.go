// Package receipt builds Attested Action Receipts.
//
// A receipt is a cloud-side artifact that wraps one trace's worth
// of framework audit records into a deterministic, signable
// document. It is the smallest unit the cloud sells:
//
//	"every time your agent touches production, you get a receipt
//	 your auditor, your regulator, and your insurer all accept."
//
// This package ships v0.1 of the format. Breaking changes are
// allowed until the format is frozen at v1; consumers must check
// SchemaVersion before parsing.
//
// Slice 1 (this file): the data shape and a deterministic causal
// chain hash. Subsequent slices add the build-from-runtime entry
// point, the cloud-side signing/anchoring path, the persistence
// layer, the HTTP endpoint, and the dashboard view.
package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// SchemaVersion identifies the receipt format. v0.1 is explicitly
// experimental; the format may change without a deprecation window
// until SchemaVersion bumps to v1.
const SchemaVersion = "v0.1"

// Receipt is the v0.1 Attested Action Receipt. Six fields are the
// receipt's contract; the seventh (SchemaVersion) carries the
// version of this struct's encoding.
//
// Fields whose value is itself unproven at v0.1 are typed as
// pointers so they can ship as JSON null without confusing the
// JSON-zero-value with "we deliberately have no value here":
//
//   - RiskScore is null until the priors dataset ships.
//   - Attestation is null on the unanchored draft and populated
//     after the next hourly anchor commits.
//   - InsurerEnvelope is null until an insurer partnership exists
//     and the format is negotiated.
type Receipt struct {
	SchemaVersion string `json:"schema_version"`
	TraceID       string `json:"trace_id"`
	ProposalID    string `json:"proposal_id,omitempty"`
	TenantID      string `json:"tenant_id"`
	EntityID      string `json:"entity_id"`
	EntityType    string `json:"entity_type"`
	ActionName    string `json:"action_name"`
	// DomainID is the operational domain the decision was made
	// against, stamped from the decision record at
	// build time. Empty for receipts built before domain stamping.
	DomainID        string              `json:"domain_id,omitempty"`
	Outcome         string              `json:"outcome,omitempty"`
	IssuedAt        time.Time           `json:"issued_at"`
	CausalChain     []ChainStep         `json:"causal_chain"`
	Attestation     *Attestation        `json:"attestation"`
	RiskScore       *RiskScore          `json:"risk_score"`
	ReplayLink      string              `json:"replay_link,omitempty"`
	ComplianceMap   []ComplianceMapping `json:"compliance_map"`
	InsurerEnvelope *InsurerEnvelope    `json:"insurer_envelope"`
}

// ChainStep is one ordered step in the causal chain. A step
// captures the kind of operational fact the framework recorded,
// the actor that produced it, and a content hash over the
// underlying audit record's data so the chain can be replayed
// against the audit store.
type ChainStep struct {
	Sequence   int       `json:"sequence"`
	Kind       string    `json:"kind"`
	ActorID    string    `json:"actor_id,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	Message    string    `json:"message,omitempty"`
	DataHash   string    `json:"data_hash"` // hex-encoded sha256 over the canonical JSON of the audit record's Data map
	// Data is a step's own values when it is not an audit record a
	// reader can look up: a settled receipt's answer and tool report.
	// DataHash covers exactly this map. On a decision step it is
	// what the call moved and was on, from the decision's record
	// (amount, on_arg, on_hash; kiff/tafs#167), and DataHash still covers
	// the whole record. Absent on every other step, and on decisions
	// recorded without them, so earlier receipts are unchanged.
	Data map[string]any `json:"data,omitempty"`
}

// Attestation is the cloud's signature over the causal chain plus
// the public-trail transaction that committed the containing
// hourly root. Populated after the next anchor cycle; nil on the
// unanchored draft.
type Attestation struct {
	Algorithm     string    `json:"algorithm"`      // e.g. "secp256k1+keccak256"
	Signature     string    `json:"signature"`      // hex
	WalletAddress string    `json:"wallet_address"` // 0x-lowercase
	AnchorTxHash  string    `json:"anchor_tx_hash"` // hex; the public-trail transaction hash
	AnchoredAt    time.Time `json:"anchored_at"`

	// Network is the chain the anchor transaction was submitted to:
	// "base-sepolia" or "base-mainnet". Empty for a signed-but-unanchored
	// receipt (LocalAttester never anchors). Dashboards render the
	// matching block-explorer link from it; verifiers pick the right chain.
	Network string `json:"network,omitempty"`
}

// Anchor network labels. The chain an attestation was
// submitted to; the dashboard maps these to a tier label and the
// matching block-explorer base URL.
const (
	NetworkBaseSepolia = "base-sepolia"
	NetworkBaseMainnet = "base-mainnet"
)

// RiskScore is the cross-tenant prior for this action shape, plus
// the per-instance score. Nil at v0.1
// because the dataset does not yet exist.
type RiskScore struct {
	Prior          float64 `json:"prior"`
	Instance       float64 `json:"instance"`
	DatasetVersion string  `json:"dataset_version"`
	SampleSize     int     `json:"sample_size"`
}

// ComplianceMapping is one entry mapping the receipt to a named
// compliance framework. v0.1 ships a hand-curated map for the
// demo refund domain only; multi-tenant vocabulary management is
// a v0.2 concern.
type ComplianceMapping struct {
	Framework    string `json:"framework"`     // e.g. "SOC2"
	ControlID    string `json:"control_id"`    // e.g. "CC7.2"
	EvidenceKind string `json:"evidence_kind"` // e.g. "system_monitoring"
}

// InsurerEnvelope is a normalized subset of the receipt structured
// for bulk consumption by a cyber-insurer's underwriting pipeline.
// The shape is negotiated with one named partner;
// nil until that partnership exists.
type InsurerEnvelope struct {
	PartnerID string `json:"partner_id"`
	// Format intentionally minimal at v0.1. Whatever the partner
	// asks for goes here once the conversation lands.
}

// CausalChainHash returns a hex-encoded sha256 over the canonical
// (sorted, no-whitespace) JSON of the causal chain. This is the
// value that gets signed by the cloud's per-tenant wallet at
// anchor time. The hash is deterministic across runs given the
// same chain content.
//
// Note: the hash is over CausalChain only, not the whole receipt.
// Fields above the chain (SchemaVersion, TraceID, etc.) identify
// the receipt; fields below (Attestation, RiskScore, etc.) are
// populated *after* the chain is fixed.
func (r Receipt) CausalChainHash() (string, error) {
	canonical, err := canonicalJSON(r.CausalChain)
	if err != nil {
		return "", fmt.Errorf("canonicalize causal chain: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON encodes v as JSON with sorted map keys and no
// extraneous whitespace. The Go stdlib encoder already sorts map
// keys, but its output uses no whitespace by default — we just
// need to make sure the value is encoded through json.Marshal
// (not json.MarshalIndent) and that nested maps follow the same
// rule. ChainStep contains no maps, so plain Marshal suffices.
//
// Kept separate as a function so future fields containing maps
// (Data passthrough, etc.) can be canonicalized through the same
// helper.
func canonicalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// SortChain sorts steps by Sequence in place. Callers building a
// receipt from disparate sources should call this before computing
// CausalChainHash so the hash is stable.
func SortChain(steps []ChainStep) {
	sort.SliceStable(steps, func(i, j int) bool {
		return steps[i].Sequence < steps[j].Sequence
	})
}
