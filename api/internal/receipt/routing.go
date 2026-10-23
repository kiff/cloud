package receipt

import (
	"context"
	"log/slog"
)

// Per-tenant chain routing for the anchor pipeline.
//
// Before this, the pipeline held one process-wide Attester and every
// tenant's receipts anchored to the same chain. With the chain-tier
// entitlement,
// the pipeline must pick the attester per tenant. AttesterResolver is
// the seam; RoutingAttester is the production implementation.

// AttesterResolver returns the Attester to use for a tenant's
// receipts. The pipeline consults it in AttestPending; when no
// resolver is wired the pipeline falls back to its single default
// attester (tests, local mode, single-chain anchor mode).
type AttesterResolver interface {
	AttesterFor(ctx context.Context, tenantID string) (Attester, error)
}

// ChainTierResolver returns a tenant's chain — "sepolia" or
// "mainnet". The string values mirror tenant.ChainTier so the
// receipt package stays decoupled from the tenant package (no import
// of plan/metadata logic here); the cloud wiring adapts tenant.PlanFor
// to this interface.
type ChainTierResolver interface {
	ChainTierFor(ctx context.Context, tenantID string) (string, error)
}

// Chain values (mirror tenant.ChainSepolia / tenant.ChainMainnet).
const (
	chainSepolia = "sepolia"
	chainMainnet = "mainnet"
)

// RoutingAttester selects the Sepolia or mainnet attester by the
// tenant's chain tier.
//
//   - sepolia is required: the always-on chain. The signed
//     receipt is the core beneath it; routing
//     never disables anchoring.
//   - mainnet is optional — nil until the mainnet KiffAnchor contract
//     is deployed and the mainnet attester configured. A
//     mainnet-entitled tenant whose mainnet attester is nil falls
//     back to Sepolia: the receipt is still anchored (honest), just
//     not yet on mainnet, and a warning is logged so the gap is
//     visible. This is the deliberate behavior the slice-1 PR noted.
type RoutingAttester struct {
	tiers   ChainTierResolver
	sepolia Attester
	mainnet Attester
	logger  *slog.Logger
}

// NewRoutingAttester wires the tier resolver and the two attesters.
// sepolia is required; mainnet may be nil. A nil tiers resolver makes
// every tenant route to sepolia (safe default). A nil logger
// discards.
func NewRoutingAttester(tiers ChainTierResolver, sepolia, mainnet Attester, logger *slog.Logger) *RoutingAttester {
	if logger == nil {
		logger = slog.New(discardHandler{})
	}
	return &RoutingAttester{tiers: tiers, sepolia: sepolia, mainnet: mainnet, logger: logger}
}

// AttesterFor implements AttesterResolver. It never returns an error:
// every uncertain path falls back to the Sepolia attester so a
// resolve blip or an unconfigured mainnet downgrades to "anchored on
// Sepolia," never to "not anchored."
func (r *RoutingAttester) AttesterFor(ctx context.Context, tenantID string) (Attester, error) {
	if r.tiers == nil {
		return r.sepolia, nil
	}
	tier, err := r.tiers.ChainTierFor(ctx, tenantID)
	if err != nil {
		r.logger.WarnContext(ctx, "receipt: chain tier resolve failed; anchoring on sepolia",
			slog.String("tenant", tenantID),
			slog.String("err", err.Error()),
		)
		return r.sepolia, nil
	}
	if tier == chainMainnet {
		if r.mainnet != nil {
			return r.mainnet, nil
		}
		r.logger.WarnContext(ctx, "receipt: tenant entitled to mainnet but mainnet attester not configured; anchoring on sepolia (pending mainnet)",
			slog.String("tenant", tenantID),
		)
		return r.sepolia, nil
	}
	return r.sepolia, nil
}
