package receipt_test

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient/simulated"

	"github.com/kiff/cloud/api/internal/anchor"
	"github.com/kiff/cloud/api/internal/receipt"
)

// TestAnchorAttester_ResolvesSaltFromTenant exercises the per-tenant
// salt path end-to-end: a SaltResolver that returns a known salt for the
// receipt's tenant id is consulted, and the resulting on-chain
// transaction reflects the salted leaf form (verified by replaying
// the same proof against the same root with the same salt).
func TestAnchorAttester_ResolvesSaltFromTenant(t *testing.T) {
	t.Parallel()

	wallet, err := anchor.NewRandomWallet()
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

	sub, _, deployTx, err := anchor.DeployAnchor(ctx, client, wallet)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	sim.Commit()
	if _, err := waitMinedReceipt(ctx, t, client, deployTx); err != nil {
		t.Fatalf("deploy receipt: %v", err)
	}

	// Build a real receipt out of an in-memory audit trace.
	traceID := "trace-anchor-salt-001"
	tenantID := "ten-anchor-salt"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID:      tenantID,
		TraceID:       traceID,
		ComplianceMap: refundDomainCompliance(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Fixed salt + tracking resolver. The test asserts (a) the
	// resolver was called with the correct tenant id and (b) the
	// salt actually flows to the on-chain leaf via the recovered
	// signature path, by re-running VerifyAttestation.
	wantSalt := []byte("test-salt-32-bytes-padding-aaaa!")
	var calledWith string
	salts := receipt.SaltResolverFunc(func(_ context.Context, tid string) ([]byte, error) {
		calledWith = tid
		return wantSalt, nil
	})

	at := time.Date(2026, time.May, 26, 14, 0, 0, 0, time.UTC)
	attester := receipt.NewAnchorAttester(sub, wallet, salts, fixedClock(at), nil)

	att, err := attester.Attest(ctx, r)
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}
	sim.Commit()

	if calledWith != tenantID {
		t.Errorf("SaltResolver was called with %q, want %q", calledWith, tenantID)
	}
	if att == nil {
		t.Fatal("Attestation: got nil")
	}
	if att.AnchorTxHash == "" || !strings.HasPrefix(att.AnchorTxHash, "0x") {
		t.Errorf("AnchorTxHash: got %q, want non-empty hex with 0x prefix", att.AnchorTxHash)
	}
	if err := receipt.VerifyAttestation(r, att); err != nil {
		t.Errorf("VerifyAttestation off-chain: %v", err)
	}
}

// TestAnchorAttester_NilResolverFallsBackToUnsalted guards the
// rollout-window contract: a nil SaltResolver (or a resolver that
// hands back nil bytes for a tenant pending backfill) anchors with
// unsalted leaves so legacy tenants still get receipts. The
// behavior is byte-equivalent to the pre-#107 form.
func TestAnchorAttester_NilResolverFallsBackToUnsalted(t *testing.T) {
	t.Parallel()

	wallet, err := anchor.NewRandomWallet()
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

	sub, _, deployTx, err := anchor.DeployAnchor(ctx, client, wallet)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	sim.Commit()
	if _, err := waitMinedReceipt(ctx, t, client, deployTx); err != nil {
		t.Fatalf("deploy receipt: %v", err)
	}

	traceID := "trace-anchor-nilsalt-001"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID:      "ten-anchor-nilsalt",
		TraceID:       traceID,
		ComplianceMap: refundDomainCompliance(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	at := time.Date(2026, time.May, 26, 14, 0, 0, 0, time.UTC)
	// salts==nil: the legacy unsalted path. Should still anchor.
	attester := receipt.NewAnchorAttester(sub, wallet, nil, fixedClock(at), nil)

	att, err := attester.Attest(ctx, r)
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}
	sim.Commit()
	if att == nil {
		t.Fatal("Attestation: got nil")
	}
	if err := receipt.VerifyAttestation(r, att); err != nil {
		t.Errorf("VerifyAttestation off-chain: %v", err)
	}
}

// TestAnchorAttester_SaltResolverErrorFailsAttest pins the
// fail-loud contract: if the resolver returns an error, the
// attestation must fail rather than silently degrade to unsalted.
// The salt boundary is a security primitive; better to refuse than
// to anchor under the wrong contract.
func TestAnchorAttester_SaltResolverErrorFailsAttest(t *testing.T) {
	t.Parallel()

	wallet, err := anchor.NewRandomWallet()
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

	sub, _, deployTx, err := anchor.DeployAnchor(ctx, client, wallet)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	sim.Commit()
	if _, err := waitMinedReceipt(ctx, t, client, deployTx); err != nil {
		t.Fatalf("deploy receipt: %v", err)
	}

	traceID := "trace-anchor-saltfail-001"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID:      "ten-anchor-saltfail",
		TraceID:       traceID,
		ComplianceMap: refundDomainCompliance(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	wantErr := errors.New("postgres exploded")
	salts := receipt.SaltResolverFunc(func(_ context.Context, _ string) ([]byte, error) {
		return nil, wantErr
	})

	at := time.Date(2026, time.May, 26, 14, 0, 0, 0, time.UTC)
	attester := receipt.NewAnchorAttester(sub, wallet, salts, fixedClock(at), nil)

	if _, err := attester.Attest(ctx, r); err == nil {
		t.Fatal("Attest: expected error from salt resolver, got nil")
	} else if !errors.Is(err, wantErr) {
		// We wrap with fmt.Errorf in the attester; errors.Is
		// requires the wrap chain, so use Unwrap or check the
		// substring. Substring is good enough for a contract
		// test.
		if !strings.Contains(err.Error(), wantErr.Error()) {
			t.Fatalf("Attest: error chain does not contain %q: %v", wantErr.Error(), err)
		}
	}
}
