package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tooloutcome"
	"github.com/kiff/cloud/api/internal/words"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
)

// A sealed fill is a browser step that moves a sign-in saved in the owner's
// Chrome into a page (the tafs Chrome extension's "sealed values"). The
// extension fills it only with the paired phone's own release
// (tafs-chrome/release/v1), which the phone signs when the owner allows the
// held step, and which the allow carries as {release: {chrome_id, op,
// origin, nonce, exp, sig}}.
//
// KIFF records the allow, so it refuses one that comes without its release:
// a client that can't make a release (an older app, a desktop) would
// otherwise record an approval the owner never released, even though the
// extension would still refuse the fill. And no lasting answer lets a sealed
// fill go: not Extra room, not "Allow this from now on". Each fill is
// allowed once, by the owner, with its own release.

// releaseAhead is how far ahead a release's exp may be: the extension takes
// at most 2 minutes, and the phone signs 60 s ahead.
const releaseAhead = 2 * time.Minute

// sealedRelease is the allow's release, as the phone sends it.
type sealedRelease struct {
	ChromeID string `json:"chrome_id"`
	Op       string `json:"op"`
	Origin   string `json:"origin"`
	Nonce    string `json:"nonce"`
	Exp      int64  `json:"exp"`
	Sig      string `json:"sig"`
}

// sealedFillHold reports whether e holds a sealed fill: a gateway call whose
// arguments name a sealed handle (a "sealed" value "sec_…:…", at any depth).
// It reads the call's arguments, as the gateway stored them; a call the
// gateway has no record of can't be sent, so it isn't one. An error is a
// record that couldn't be read: the caller refuses rather than guess.
func (h *MandatesHandler) sealedFillHold(ctx context.Context, tenantID string, e mandate.Exception) (bool, error) {
	if e.ProposalID == "" || e.Request.Domain != gatewayDomain {
		return false, nil
	}
	ar, ok := h.outcomes.(tooloutcome.ArgumentsReader)
	if !ok {
		return false, nil // no gateway record here, so no gateway calls either
	}
	args, err := ar.Arguments(ctx, tenantID, []string{e.ProposalID})
	if err != nil {
		return false, err
	}
	raw, ok := args[e.ProposalID]
	if !ok {
		return false, nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false, errors.New("held call arguments don't read")
	}
	return namesSealed(v), nil
}

// namesSealed finds a "sealed" key whose value is a sealed handle's shape,
// "sec_<id>:<part>", in v at any depth. The part isn't checked here: a
// malformed handle is still a fill that asks for a release, and the
// extension refuses it.
func namesSealed(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if s, ok := x.(string); ok && k == "sealed" && strings.HasPrefix(s, "sec_") && strings.Contains(s, ":") {
				return true
			}
			if namesSealed(x) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if namesSealed(x) {
				return true
			}
		}
	}
	return false
}

// releaseRefusal is why raw isn't a release the extension could take, or "".
// KIFF can't check its signature (the step's id and the read it names are
// planque's), so it checks what the extension would refuse on sight: every
// field present, an https origin, a nonce of at least 16 bytes, and an exp
// still ahead and at most 2 minutes away.
func releaseRefusal(raw json.RawMessage, now time.Time) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "this call fills a sign-in saved in Chrome: the allow must carry the phone's release, which this app doesn't make. Allow it on a phone that shows the fill"
	}
	var rel sealedRelease
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&rel) != nil {
		return "the release is {chrome_id, op, origin, nonce, exp, sig}"
	}
	for _, f := range []string{rel.ChromeID, rel.Op, rel.Origin, rel.Sig} {
		if f == "" || len(f) > 1024 || strings.IndexFunc(f, unicode.IsControl) >= 0 {
			return "the release is {chrome_id, op, origin, nonce, exp, sig}, each one line"
		}
	}
	if !strings.HasPrefix(rel.Origin, "https://") {
		return "the release's origin is an https origin"
	}
	if n, err := base64.RawURLEncoding.DecodeString(rel.Nonce); err != nil || len(n) < 16 {
		return "the release's nonce is at least 16 random bytes, base64url without padding"
	}
	exp := time.UnixMilli(rel.Exp)
	if !exp.After(now) || exp.After(now.Add(releaseAhead)) {
		return "the release has expired, or isn't due yet: check that the phone's clock is right, then allow again"
	}
	return ""
}

// refuseSealedFill answers an allow of hold id and reports refused when it
// is a sealed fill the allow can't take: any answer but approve-once with a
// release (change-card, an external approver's approve). A refusal is
// always taken: it takes power away. sealed says the hold is a sealed fill
// whose allow was taken, with its release checked: the caller keeps that
// release once the hold is approved (keepSealedRelease).
func (h *MandatesHandler) refuseSealedFill(w http.ResponseWriter, r *http.Request, tenantID, id, answer string, release json.RawMessage) (refused, sealed bool) {
	if answer == "reject" {
		return false, false
	}
	e, err := h.store.Exception(r.Context(), tenantID, id)
	if errors.Is(err, mandate.ErrExceptionNotFound) {
		return false, false // answered as before: 404
	}
	if err == nil {
		var sealed bool
		sealed, err = h.sealedFillHold(r.Context(), tenantID, e)
		if err == nil && !sealed {
			return false, false
		}
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "mandates: held call not read for a sealed fill",
			slog.String("tenant", tenantID), slog.String("exception", id), slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "KIFF couldn't read the held call to check it: try again"})
		return true, true
	}
	if answer != "approve-once" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "sealed_fill_once",
			"detail": "this call fills a sign-in saved in Chrome: it's allowed once, with the phone's release, never by a change to the Card"})
		return true, true
	}
	// The owner allows a fill only from a card that showed it: planque's
	// signed block, kept on the hold (browser_block.go).
	if blocks, ok := h.store.(mandate.BrowserBlocks); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "release_required", "detail": "the hold can't show what it fills, so it can't be allowed"})
		return true, true
	} else if b, err := blocks.BrowserBlock(r.Context(), tenantID, e.ProposalID); err != nil || len(b) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "release_required", "detail": "the hold can't show what it fills, so it can't be allowed"})
		return true, true
	}
	if why := releaseRefusal(release, h.now()); why != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "release_required", "detail": why})
		return true, true
	}
	return false, true
}

// keepSealedRelease keeps an approved sealed fill's release until the
// gateway sends the call (mandate.SealedReleases). A release not kept fails
// closed: the call goes without one, and the Chrome fills nothing
// (release_missing), so the owner is asked to allow again.
func (h *MandatesHandler) keepSealedRelease(ctx context.Context, tenantID, id string, release json.RawMessage) {
	keeper, ok := h.store.(mandate.SealedReleases)
	if !ok {
		h.logger.ErrorContext(ctx, "mandates: sealed release not kept: the store keeps none", slog.String("tenant", tenantID), slog.String("exception", id))
		return
	}
	if err := keeper.KeepSealedRelease(ctx, tenantID, id, release); err != nil {
		h.logger.ErrorContext(ctx, "mandates: sealed release not kept",
			slog.String("tenant", tenantID), slog.String("exception", id), slog.String("err", err.Error()))
	}
}

// handSealedRelease puts the release the owner's phone gave for this sealed
// fill on its allowed decision, taken once (mandate.SealedReleases). Only a
// gateway call the gateway marked sealed_fill asks, so no other decision
// reads the store. A release not there, or past its exp, leaves none: the
// Chrome then fills nothing.
func (h *ProposalsHandler) handSealedRelease(ctx context.Context, tenantID string, prop kiffproposal.ActionProposal, spec domain.Spec, resp *proposalResponse) {
	if resp.Outcome != string(outcomeAllowed) || spec.Domain != gatewayDomain {
		return
	}
	if code, _ := prop.Parameters[GatewayOwnerAskParam].(string); code != words.OwnerAskSealedFill {
		return
	}
	if h.sealedReleases == nil {
		return
	}
	raw, err := h.sealedReleases.TakeSealedRelease(ctx, tenantID, prop.ID, h.now())
	if err != nil {
		h.logger.ErrorContext(ctx, "proposals: sealed release not read", slog.String("tenant", tenantID), slog.String("proposal", prop.ID), slog.String("err", err.Error()))
		return
	}
	resp.SealedRelease = raw
}

// WithProposalsSealedReleases is where the decide path takes a sealed fill's
// release from, once the owner allowed it (handSealedRelease).
func WithProposalsSealedReleases(r mandate.SealedReleases) ProposalsOption {
	return func(h *ProposalsHandler) { h.sealedReleases = r }
}
