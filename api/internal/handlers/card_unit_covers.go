package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/mandate"
)

// unitCovers are the currency asks signed Cards answer (kiff/tafs#464 item
// 11): each holder of a Card in force, given an action whose amount is in a
// unit. A Card's limits have no currency of their own, the action's amount
// has it, so a Card gains a currency as another action: the same tool in
// that unit. The asks close with the Cards, whatever their action; if a call
// on the original action is still refused, the next refusal opens a fresh
// ask. Best effort: a domain that can't be read closes nothing, and the
// Cards are issued all the same.
func (h *MandatesHandler) unitCovers(r *http.Request, tenantID string, cards []mandate.Mandate) []mandate.UnitCover {
	var out []mandate.UnitCover
	seen := map[string]bool{}
	for _, m := range cards {
		if !m.Active(h.now()) {
			continue
		}
		actions, _, err := h.actionsFor(r, tenantID, m.Domain)
		if err != nil {
			h.logger.WarnContext(r.Context(), "mandates: currency asks not closed: domain unread", slog.String("tenant", tenantID), slog.String("err", err.Error()))
			continue
		}
		units := map[string]string{}
		for _, a := range actions {
			for _, p := range a.Parameters {
				if p.Type == "int" && p.Unit != "" {
					units[strings.ToLower(a.Name)] = strings.ToUpper(p.Unit)
				}
			}
		}
		for _, g := range m.Grants {
			unit := units[strings.ToLower(g.Action)]
			if unit == "" {
				continue
			}
			for _, holder := range m.HolderList() {
				if k := holder + "\x00" + unit; !seen[k] {
					seen[k] = true
					out = append(out, mandate.UnitCover{Subject: holder, Action: g.Action, Unit: unit})
				}
			}
		}
	}
	return out
}
