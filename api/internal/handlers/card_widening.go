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

// windowAgent is the taf whose step opened the approval window a
// window-signed request was signed in ("" for a window opened for none).
func windowAgent(r *http.Request) string {
	with, _ := auth.SignedWithFrom(r.Context())
	return with.WindowAgent
}

// outsideWindow reports whether a window opened for agent may not confirm
// or issue cards (kiff/tafs#494): a tap inside a window speaks for the taf
// whose step opened it, so every holder of each Card, and of the Card in
// force it reissues, must be that taf. A window opened for no taf confirms
// and issues no Card.
func (h *MandatesHandler) outsideWindow(ctx context.Context, tenantID, agent string, cards []mandate.Mandate) (bool, error) {
	if agent == "" {
		return true, nil
	}
	inForce, err := h.store.List(ctx, tenantID)
	if err != nil {
		return false, err
	}
	current := make(map[string]mandate.Mandate, len(inForce))
	for _, m := range inForce {
		if m.RevokedAt == nil {
			current[m.ID] = m
		}
	}
	onlyAgent := func(holders []string) bool {
		if len(holders) == 0 {
			return false
		}
		for _, h := range holders {
			if h != agent {
				return false
			}
		}
		return true
	}
	for _, c := range cards {
		if !onlyAgent(c.HolderList()) {
			return true, nil
		}
		if cur, ok := current[c.ID]; ok && !onlyAgent(cur.HolderList()) {
			return true, nil
		}
	}
	return false, nil
}

// windowScope answers a window-signed Card change for a taf other than the
// one whose step opened the window: nothing changes. Its error is
// critical_key_required, which every shipped app already answers by signing
// again with the approval key behind the fingerprint; reason window_scope
// tells it apart from a widening.
func windowScope(w http.ResponseWriter, agent string) {
	detail := "this approval window was opened for " + agent + ": sign Cards for another taf with the phone's approval key, behind its fingerprint or face, or open a window from that taf's step"
	if agent == "" {
		detail = "this approval window was opened for no taf: sign a Card with the phone's approval key, behind its fingerprint or face, or open a window from the taf's step"
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "critical_key_required", "reason": "window_scope", "detail": detail})
}

// criticalKeyRequired answers a window-signed change that widens a Card in
// force: nothing changes, and the words say each way it widens, so the
// app can say why it asks for the fingerprint.
func criticalKeyRequired(w http.ResponseWriter, ws []cardWidening) {
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "critical_key_required",
		"detail": "this widens a Card in force: sign it with the phone's approval key, behind its fingerprint or face",
		"widens": ws})
}
