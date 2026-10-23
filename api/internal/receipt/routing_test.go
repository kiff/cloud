package receipt_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/kiff/cloud/api/internal/receipt"
)

// recordingAttester is a stub Attester that records which receipts it
// attested, so a test can prove which submitter a tenant routed to.
type recordingAttester struct {
	name     string
	attested []string // trace ids
}

func (a *recordingAttester) Attest(_ context.Context, r receipt.Receipt) (*receipt.Attestation, error) {
	a.attested = append(a.attested, r.TraceID)
	return &receipt.Attestation{
		Algorithm:     receipt.AttestationAlgorithm,
		Signature:     "0xsig-" + a.name,
		WalletAddress: "0x" + a.name,
		AnchorTxHash:  "0xtx-" + a.name,
		AnchoredAt:    time.Now().UTC(),
	}, nil
}

func (a *recordingAttester) Address() common.Address { return common.Address{} }

// stubTiers maps tenant id -> tier, with an optional error.
type stubTiers struct {
	tiers map[string]string
	err   error
}

func (s stubTiers) ChainTierFor(_ context.Context, tenantID string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.tiers[tenantID], nil
}

func TestRoutingAttester_RoutesByTier(t *testing.T) {
	t.Parallel()
	sepolia := &recordingAttester{name: "sepolia"}
	mainnet := &recordingAttester{name: "mainnet"}
	tiers := stubTiers{tiers: map[string]string{
		"free-tenant": "sepolia",
		"paid-tenant": "mainnet",
	}}
	r := receipt.NewRoutingAttester(tiers, sepolia, mainnet, nil)

	got, _ := r.AttesterFor(context.Background(), "free-tenant")
	if got != receipt.Attester(sepolia) {
		t.Errorf("free tenant should route to sepolia")
	}
	got, _ = r.AttesterFor(context.Background(), "paid-tenant")
	if got != receipt.Attester(mainnet) {
		t.Errorf("paid tenant should route to mainnet")
	}
}

// A mainnet-entitled tenant whose mainnet attester is nil (contract
// not deployed yet) falls back to Sepolia — never to "no attester".
func TestRoutingAttester_MainnetEntitledFallsBackWhenUnconfigured(t *testing.T) {
	t.Parallel()
	sepolia := &recordingAttester{name: "sepolia"}
	tiers := stubTiers{tiers: map[string]string{"paid-tenant": "mainnet"}}
	r := receipt.NewRoutingAttester(tiers, sepolia, nil, nil)

	got, err := r.AttesterFor(context.Background(), "paid-tenant")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != receipt.Attester(sepolia) {
		t.Errorf("mainnet-entitled tenant with no mainnet attester should fall back to sepolia")
	}
}

// A tier-resolve error degrades to Sepolia (allow-when-uncertain),
// never an error or a skipped anchor.
func TestRoutingAttester_ResolveErrorFallsBackToSepolia(t *testing.T) {
	t.Parallel()
	sepolia := &recordingAttester{name: "sepolia"}
	mainnet := &recordingAttester{name: "mainnet"}
	r := receipt.NewRoutingAttester(stubTiers{err: errors.New("db blip")}, sepolia, mainnet, nil)

	got, err := r.AttesterFor(context.Background(), "any")
	if err != nil {
		t.Fatalf("resolve error must not propagate; got %v", err)
	}
	if got != receipt.Attester(sepolia) {
		t.Errorf("resolve error should fall back to sepolia")
	}
}

// End-to-end through the pipeline: AttestPending uses the resolved
// per-tenant attester, so a paid tenant's unanchored receipt is
// attested by the mainnet stub and a free tenant's by the sepolia stub.
func TestPipeline_AttestPendingRoutesPerTenant(t *testing.T) {
	t.Parallel()
	store := receipt.NewMemoryStore()
	seed := func(tenantID, traceID string) {
		if err := store.Save(context.Background(), receipt.Receipt{
			TenantID: tenantID, TraceID: traceID, SchemaVersion: "kiff.receipt.v1",
			EntityID: "e1", EntityType: "Order", ActionName: "REFUND_ORDER",
			IssuedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed %s/%s: %v", tenantID, traceID, err)
		}
	}
	seed("free-tenant", "tr-free")
	seed("paid-tenant", "tr-paid")

	sepolia := &recordingAttester{name: "sepolia"}
	mainnet := &recordingAttester{name: "mainnet"}
	tiers := stubTiers{tiers: map[string]string{"free-tenant": "sepolia", "paid-tenant": "mainnet"}}
	routing := receipt.NewRoutingAttester(tiers, sepolia, mainnet, nil)

	// Default attester is sepolia; the resolver routes paid → mainnet.
	p, err := receipt.NewPipeline(store, sepolia, receipt.WithAttesterResolver(routing))
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}

	if _, err := p.AttestPending(context.Background(), "free-tenant"); err != nil {
		t.Fatalf("attest free: %v", err)
	}
	if _, err := p.AttestPending(context.Background(), "paid-tenant"); err != nil {
		t.Fatalf("attest paid: %v", err)
	}

	if len(sepolia.attested) != 1 || sepolia.attested[0] != "tr-free" {
		t.Errorf("sepolia should have attested tr-free; got %v", sepolia.attested)
	}
	if len(mainnet.attested) != 1 || mainnet.attested[0] != "tr-paid" {
		t.Errorf("mainnet should have attested tr-paid; got %v", mainnet.attested)
	}
}
