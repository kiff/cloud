package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strings"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/words"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
)

// A held sealed fill's browser block is planque's description of the call:
// what the owner reads on the hold (the site, the element's words, an excerpt
// of the page, which Chrome) and the fields the phone's release signs. planque
// reads the page, so planque signs the block, with its step key under
// planqueDescribeLabel. KIFF keeps a block only when that signature holds
// under the pinned key and the block is for this account, this call's first
// Chrome step and the Chrome it names: whoever carries it (the gateway, on the
// decide that holds the call) can't change it, and no other call's block fits.

// planqueStepKey is planque's step public key (SPKI, base64url): the one the
// tafs Chrome extension's production build pins to check planque's steps. A
// new key ships as a new build of both. It's public: nothing here is secret.
const planqueStepKey = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAExS_lxBTwDdO8wf8ksu6kkIF8r3Z-Vp3tWZUKjlBjQezW0BhCCW_0tkGqRewu0qBL8KDggls0cjFF-NDcnAjH7g"

// planqueDescribeLabel is what planque signs a block under: label ‖ 0x00 ‖
// JCS(block), SHA-256, raw r‖s, as its steps are signed.
const planqueDescribeLabel = "planque/describe/v1"

// maxBrowserBlock bounds a signed block as it travels.
const maxBrowserBlock = 8 << 10

// browserBlock is planque's signed description of a held sealed fill.
type browserBlock struct {
	V       string `json:"v"`
	Tenant  string `json:"tenant"`
	Op      string `json:"op"`
	Site    string `json:"site"`
	Origin  string `json:"origin"`
	Action  string `json:"action_words"`
	Context struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	} `json:"context"`
	Chrome struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"chrome"`
	Release struct {
		ChromeID string `json:"chrome_id"`
		Op       string `json:"op"`
		Sealed   string `json:"sealed"`
		Origin   string `json:"origin"`
		FieldRef string `json:"field_ref"`
		ReadID   string `json:"read_id"`
	} `json:"release_fields"`
}

// signedBrowserBlock is a block as it travels: planque's JSON and its signature.
type signedBrowserBlock struct {
	Block json.RawMessage `json:"block"`
	Sig   string          `json:"sig"`
}

// chromeStepID is planque's name for a run's nth Chrome step (planque
// chrome_driver.go): the run is KIFF's operation, so a sealed fill, its call's
// only step, is step 1.
func chromeStepID(run string, n int) string {
	id := fmt.Sprintf("%s.%d", run, n)
	if len(id) <= 64 {
		return id
	}
	h := sha256.Sum256([]byte(run))
	return fmt.Sprintf("op_%s.%d", hex.EncodeToString(h[:])[:40], n)
}

// verifyPlanque reports whether sig (raw r‖s, base64url) is planque's step
// key's signature over JCS(block) under planqueDescribeLabel.
func verifyPlanque(block json.RawMessage, sig string) bool {
	der, err := base64.RawURLEncoding.DecodeString(planqueStepKey)
	if err != nil {
		return false
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return false
	}
	k, ok := pub.(*ecdsa.PublicKey)
	if !ok || k.Curve.Params().Name != "P-256" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(raw) != 64 {
		return false
	}
	canon, err := permit.Canonicalize(block)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(append(append([]byte(planqueDescribeLabel), 0), canon...))
	return ecdsa.Verify(k, sum[:], new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:]))
}

// browserBlockRefusal says why raw isn't a block KIFF keeps for the held
// sealed fill proposalID in tenantID, or "". verify is verifyPlanque, set
// apart so tests can sign with their own key.
func browserBlockRefusal(raw json.RawMessage, tenantID, proposalID string, verify func(json.RawMessage, string) bool) string {
	if len(raw) == 0 || len(raw) > maxBrowserBlock {
		return "no block, or one over 8 KiB"
	}
	var s signedBrowserBlock
	if json.Unmarshal(raw, &s) != nil || len(s.Block) == 0 || s.Sig == "" {
		return "a block travels as {block, sig}"
	}
	if !verify(s.Block, s.Sig) {
		return "the block isn't signed by planque"
	}
	var b browserBlock
	if json.Unmarshal(s.Block, &b) != nil || b.V != planqueDescribeLabel {
		return "the block isn't planque/describe/v1"
	}
	switch {
	case b.Tenant != tenantID:
		return "the block is for another account"
	case b.Op != chromeStepID(proposalID, 1) || b.Release.Op != b.Op:
		return "the block is for another call"
	case b.Chrome.ID == "" || b.Release.ChromeID != b.Chrome.ID:
		return "the block names another Chrome than the one it releases on"
	case !strings.HasPrefix(b.Origin, "https://") || b.Release.Origin != b.Origin || b.Site == "":
		return "the block's origin isn't one https origin"
	case b.Release.Sealed == "" || b.Release.FieldRef == "" || b.Release.ReadID == "":
		return "the block names no sealed value, field or read"
	}
	return ""
}

// keepBrowserBlock keeps a held sealed fill's block, as the gateway sent it on
// the decide that held the call, once it holds (browserBlockRefusal). A block
// not kept leaves the hold without one, and its allow is then refused
// (refuseSealedFill): the owner is never asked to release a fill the card
// can't show.
func (h *ProposalsHandler) keepBrowserBlock(ctx context.Context, tenantID string, prop kiffproposal.ActionProposal, spec domain.Spec, resp proposalResponse, raw json.RawMessage) {
	if len(raw) == 0 || h.browserBlocks == nil || spec.Domain != gatewayDomain || resp.Outcome != string(outcomeOwnerRequired) {
		return
	}
	if code, _ := prop.Parameters[GatewayOwnerAskParam].(string); code != words.OwnerAskSealedFill {
		return
	}
	if why := browserBlockRefusal(raw, tenantID, prop.ID, verifyPlanque); why != "" {
		h.logger.WarnContext(ctx, "proposals: browser block not kept", slog.String("tenant", tenantID), slog.String("proposal", prop.ID), slog.String("why", why))
		return
	}
	if err := h.browserBlocks.KeepBrowserBlock(ctx, tenantID, prop.ID, raw); err != nil {
		h.logger.WarnContext(ctx, "proposals: browser block not kept", slog.String("tenant", tenantID), slog.String("proposal", prop.ID), slog.String("err", err.Error()))
	}
}

// WithProposalsBrowserBlocks is where a held sealed fill's block is kept.
func WithProposalsBrowserBlocks(b mandate.BrowserBlocks) ProposalsOption {
	return func(h *ProposalsHandler) { h.browserBlocks = b }
}
