package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// GatewayToolClasses reads the class the gateway recorded for each of an
// account's tools (K4). The tool titles reader may implement it.
type GatewayToolClasses interface {
	ToolClasses(ctx context.Context, tenantID string) (map[string]tooloutcome.ToolClass, error)
}

// toolClasses is the account's tools' recorded classes, by the name a Card
// grants them under. None when they can't be read: a draft then opens
// nothing, and its tool's gated calls are refused until the owner opens them.
func (h *MandatesHandler) toolClasses(r *http.Request, tenantID string) map[string]tooloutcome.ToolClass {
	tc, ok := h.titles.(GatewayToolClasses)
	if !ok || h.titles == nil {
		return nil
	}
	m, err := tc.ToolClasses(r.Context(), tenantID)
	if err != nil {
		h.logger.WarnContext(r.Context(), "drafts: tool classes unreadable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return nil
	}
	return m
}

// draftOpenings fills a drafted gateway Card's openings (K4) for the tools
// it grants and that would be refused unopened: the narrowest the tool's
// reviewed table gives (change_client_invoice_status for operation =
// finalize), and its whole class only when there is none. A grant that
// names its own openings keeps them. Each opening is a widening the owner
// reads, line by line, before the fingerprint (OpensOnNew, Widens). It
// reports whether it opened anything.
func draftOpenings(body *mandateBody, classes map[string]tooloutcome.ToolClass) bool {
	if body.Domain != gatewayDomain || len(classes) == 0 {
		return false
	}
	filled := false
	for i := range body.Grants {
		g := &body.Grants[i]
		tc, ok := classes[g.Action]
		if len(g.Opens) > 0 || !ok || !mandate.GatedClass(tc.Class) {
			continue
		}
		if len(tc.Opens) == 0 {
			g.Opens = []mandateOpeningBody{{Class: tc.Class}}
		} else {
			for _, o := range tc.Opens {
				g.Opens = append(g.Opens, mandateOpeningBody{Class: tc.Class, Argument: o.Argument, Value: o.Value})
			}
		}
		if len(g.Opens) > mandate.MaxOpens {
			// More one-value openings than a grant holds: none, and the
			// owner opens what the taf needs.
			g.Opens = nil
			continue
		}
		filled = true
	}
	return filled
}

// fillOpenings fills the openings of a draft's Cards as it is made or
// previewed (draftOpenings), never as it is confirmed: the Cards confirmed
// are the ones the owner read. A Card that doesn't decode is left for
// draftCards to refuse.
func (h *MandatesHandler) fillOpenings(r *http.Request, tenantID string, raw []json.RawMessage) ([]json.RawMessage, int, string) {
	if len(raw) == 0 {
		return raw, 0, ""
	}
	classes := h.toolClasses(r, tenantID)
	if len(classes) == 0 {
		return raw, 0, ""
	}
	out := slices.Clone(raw)
	for i, c := range raw {
		var body mandateBody
		dec := json.NewDecoder(bytes.NewReader(c))
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || !draftOpenings(&body, classes) {
			continue
		}
		filled, err := json.Marshal(body)
		if err != nil {
			return nil, http.StatusInternalServerError, "fill the draft's openings failed"
		}
		out[i] = filled
	}
	return out, 0, ""
}
