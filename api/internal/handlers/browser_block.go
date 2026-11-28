package handlers

import (
	"bytes"
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
	"github.com/kiff/cloud/api/internal/purchase"
	"github.com/kiff/cloud/api/internal/words"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
)

// A held sealed fill's browser block is planque's description of the call:
// what the owner reads on the hold (the site, the element's words, an excerpt
// of the page, which Chrome) and the fields the phone's release signs. planque
// reads the page, so planque signs the block, with its step key under
// planqueDescribeLabel. KIFF keeps a block only when that signature holds
// under the pinned key and the block is for this account, this call's
// sealed step (sealedFillStep) and the Chrome it names: whoever carries it (the gateway, on the
// decide that holds the call) can't change it, and no other call's block fits.

// planqueStepKey is planque's step public key (SPKI, base64url): the one the
// tafs Chrome extension's production build pins to check planque's steps. A
// new key ships as a new build of both. It's public: nothing here is secret.
// planqueStepKey is it, and only the end-to-end build (-tags e2e) may
// change it.
var planqueStepKey = pinnedPlanqueStepKey

const pinnedPlanqueStepKey = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAExS_lxBTwDdO8wf8ksu6kkIF8r3Z-Vp3tWZUKjlBjQezW0BhCCW_0tkGqRewu0qBL8KDggls0cjFF-NDcnAjH7g"

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

// sealedFillStep is a sealed fill's step in its call's run: planque opens the
// page first, in every call (step 1), and a sealed fill is its call's only
// act, so it's step 2.
const sealedFillStep = 2

// chromeStepID is planque's name for a run's nth Chrome step (planque
// chrome_driver.go); the run is KIFF's operation.
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
	return verifyWith(k, block, sig)
}

// verifyWith reports whether sig is k's signature over JCS(block) under
// planqueDescribeLabel.
func verifyWith(k *ecdsa.PublicKey, block json.RawMessage, sig string) bool {
	return verifyLabel(k, planqueDescribeLabel, block, sig)
}

// verifyLabel reports whether sig is k's signature over JCS(block) under
// label: a describe and a checkout are signed under their own, so neither
// passes for the other.
func verifyLabel(k *ecdsa.PublicKey, label string, block json.RawMessage, sig string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(raw) != 64 {
		return false
	}
	canon, err := permit.Canonicalize(block)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(append(append([]byte(label), 0), canon...))
	return ecdsa.Verify(k, sum[:], new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:]))
}

// browserBlockRefusal says why raw isn't a block KIFF keeps for the held
// sealed fill proposalID in tenantID, or "". verify is verifyPlanque, set
// apart so tests can sign with their own key.
func browserBlockRefusal(raw json.RawMessage, tenantID, proposalID string, verify func(json.RawMessage, string) bool) string {
	if len(raw) == 0 || len(raw) > maxBrowserBlock {
		return "no block, or one over 8 KiB"
	}
	if !plainKeys(raw) {
		return "a block's keys are each written once, in one case"
	}
	var s signedBrowserBlock
	if strictDecode(raw, &s) != nil || len(s.Block) == 0 || s.Sig == "" {
		return "a block travels as {block, sig}"
	}
	if !verify(s.Block, s.Sig) {
		return "the block isn't signed by planque"
	}
	var b browserBlock
	if strictDecode(s.Block, &b) != nil || b.V != planqueDescribeLabel {
		return "the block isn't planque/describe/v1"
	}
	switch {
	// planque's account id is KIFF's tenant id, by construction: planque
	// takes a call only with KIFF's permit for its own account (permit's
	// tenant equal to the connection's), and the describe is such a call.
	case b.Tenant != tenantID:
		return "the block is for another account"
	case b.Op != chromeStepID(proposalID, sealedFillStep) || b.Release.Op != b.Op:
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

// strictDecode reads raw into v with no field v doesn't name.
func strictDecode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// plainKeys reports whether every object in raw, at any depth, writes each
// key once, with no two keys the same but for case. Go reads a key in any
// case and keeps the last of two, while the apps draw the block as written:
// a block that could read two ways is refused, so what KIFF checked is what
// the owner reads.
func plainKeys(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var walk func() bool
	walk = func() bool {
		t, err := dec.Token()
		if err != nil {
			return false
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				k, err := dec.Token()
				key, ok := k.(string)
				if err != nil || !ok || seen[strings.ToLower(key)] {
					return false
				}
				seen[strings.ToLower(key)] = true
				if !walk() {
					return false
				}
			}
			_, err = dec.Token()
			return err == nil
		case json.Delim('['):
			for dec.More() {
				if !walk() {
					return false
				}
			}
			_, err = dec.Token()
			return err == nil
		}
		return true
	}
	if !walk() {
		return false
	}
	_, err := dec.Token()
	return err != nil // nothing after the value
}

// releaseFitsBlock is why an allow's release isn't for the call the hold
// shows, or "": its Chrome, step and origin are the block's release fields,
// so the owner releases the fill the card drew and no other.
func releaseFitsBlock(release, block json.RawMessage) string {
	var rel sealedRelease
	var s signedBrowserBlock
	var b browserBlock
	if json.Unmarshal(release, &rel) != nil || json.Unmarshal(block, &s) != nil || json.Unmarshal(s.Block, &b) != nil {
		return "the release doesn't read against the hold's block"
	}
	if rel.ChromeID != b.Release.ChromeID || rel.Op != b.Release.Op || rel.Origin != b.Release.Origin {
		return "the release is for another Chrome, step or site than the hold shows: allow again from the phone"
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
	verify := h.verifyBlock
	if verify == nil {
		verify = verifyPlanque
	}
	if why := browserBlockRefusal(raw, tenantID, prop.ID, verify); why != "" {
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

// withBrowserBlock adds a held sealed fill's block to its view: browser, as
// planque signed it (the apps draw it and nothing else), and browser_sha256,
// the hex SHA-256 of its JCS, which the phone's answer signs over. A hold
// with no block shows none, and can't be allowed (refuseSealedFill).
func (h *MandatesHandler) withBrowserBlock(ctx context.Context, tenantID string, v map[string]any, e mandate.Exception) map[string]any {
	if e.Request.OwnerAsk != words.OwnerAskSealedFill {
		return v
	}
	blocks, ok := h.store.(mandate.BrowserBlocks)
	if !ok {
		return v
	}
	raw, err := blocks.BrowserBlock(ctx, tenantID, e.ProposalID)
	if err != nil || len(raw) == 0 {
		return v
	}
	var s signedBrowserBlock
	if json.Unmarshal(raw, &s) != nil || len(s.Block) == 0 {
		return v
	}
	canon, err := permit.Canonicalize(s.Block)
	if err != nil {
		return v
	}
	sum := sha256.Sum256(canon)
	v["browser"] = s.Block
	v["browser_sha256"] = hex.EncodeToString(sum[:])
	return v
}

// withPurchase adds a held purchase's approval (contracts/purchase.json
// approval.value) to its hold as `purchase`, for the apps to draw and sign
// over. A hold that isn't a purchase's, or a case that can't be read, is
// left as it is: drawn as any held call, and its answer is refused for a
// purchase that can't be read.
func (h *MandatesHandler) withPurchase(ctx context.Context, tenantID string, v map[string]any, e mandate.Exception) map[string]any {
	if h.purchaseHolds == nil {
		return v
	}
	r, err := h.purchaseHolds.ByException(ctx, tenantID, e.ID)
	if err != nil {
		return v
	}
	v["purchase"] = purchase.ApprovalOf(r)
	return v
}
