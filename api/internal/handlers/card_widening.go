package handlers

import (
	"context"
	"net/http"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
)

// The widening rule of the approval window (kiff/tafs#494): a Card change
// is routine, signed with a tap inside an open window, only when every term
// of every Card it revises is the same as or narrower than the Card in
// force (mandate.Widens, which fails closed). Anything that widens is
// critical: the phone's approval key, behind its fingerprint, every time.
// A new Card is routine only for a holder with no Card in force (a new
// taf's first Cards); beside one it holds already, it widens. Retiring a
// Card only narrows.

// cardWidening is one way a change widens a Card in force, with the Card.
type cardWidening struct {
	Card string `json:"card"`
	mandate.Widening
}

// widensInForce says how cards widen the Cards in force they revise, each
// as issuing would read it over the Card in force (AsIssuedOver).
func (h *MandatesHandler) widensInForce(ctx context.Context, tenantID string, cards []mandate.Mandate) ([]cardWidening, error) {
	inForce, err := h.store.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]mandate.Mandate, len(inForce))
	for _, m := range inForce {
		if m.RevokedAt == nil {
			byID[m.ID] = m
		}
	}
	// Who holds a Card in force already: a new Card for one of them adds
	// authority beside it, whatever its own terms.
	holds := map[string]bool{}
	for _, m := range byID {
		for _, h := range m.HolderList() {
			holds[h] = true
		}
	}
	var out []cardWidening
	for _, c := range cards {
		cur, ok := byID[c.ID]
		if !ok {
			for _, h := range c.HolderList() {
				if holds[h] {
					out = append(out, cardWidening{Card: c.ID, Widening: mandate.Widening{Kind: "new_card",
						Words: "Adds a Card beside the ones its holder already has"}})
					break
				}
			}
			continue
		}
		for _, w := range mandate.Widens(&cur, mandate.AsIssuedOver(c, &cur)) {
			out = append(out, cardWidening{Card: c.ID, Widening: w})
		}
	}
	return out, nil
}

// windowSigned reports whether the request was signed with the device key
// inside an approval window: a routine signature, which a widening refuses.
func windowSigned(r *http.Request) bool {
	with, signed := auth.SignedWithFrom(r.Context())
	return signed && with.Window != ""
}

// criticalKeyRequired answers a window-signed change that widens a Card in
// force: nothing changes, and the words say each way it widens, so the
// app can say why it asks for the fingerprint.
func criticalKeyRequired(w http.ResponseWriter, ws []cardWidening) {
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "critical_key_required",
		"detail": "this widens a Card in force: sign it with the phone's approval key, behind its fingerprint or face",
		"widens": ws})
}
