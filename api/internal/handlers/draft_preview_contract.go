package handlers

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/membership"
)

// A draft's preview can be read against a gateway contract not applied yet:
// the one a batch of tools would apply, as the gateway builds it from the
// batch it would connect (POST /v1/connections/batch/preview). A first start
// then shows the owner KIFF's words for Cards over tools the same signature
// connects, before anything is connected or drafted. The overlay stands in
// for the stored gateway contract, and the batch's tool titles for theirs,
// for that one request; nothing is stored.

// contractOverlay is the gateway contract and tool titles a preview reads.
type contractOverlay struct {
	actions []domain.ActionSpec
	titles  map[string]string
}

type overlayKey struct{}

func withOverlay(r *http.Request, o *contractOverlay) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), overlayKey{}, o))
}

func overlayOf(r *http.Request) *contractOverlay {
	o, _ := r.Context().Value(overlayKey{}).(*contractOverlay)
	return o
}

// maxOverlayTitles bounds the titles a preview takes: one per tool a batch
// connects, and a batch connects at most a few dozen.
const maxOverlayTitles = 100

// overlayFor checks a preview's contract and titles and returns the overlay,
// or a status and why. Only a caller who may change the gateway's domain
// gives one (a connector key, or one that manages domains: the check
// ApplyDomain passes), so it shows nothing that caller couldn't apply.
func overlayFor(id auth.Identity, contract string, titles map[string]string) (*contractOverlay, int, string) {
	if !id.IsConnector() && !can(id, membership.CapDomainsManage) {
		return nil, http.StatusForbidden, "a preview against a contract needs the right to change the gateway's domain"
	}
	parsed, err := domain.ParseAndBuild([]byte(contract), nil)
	if err != nil {
		return nil, http.StatusBadRequest, "contract: " + err.Error()
	}
	if !strings.EqualFold(parsed.Spec.Domain, gatewayDomain) {
		return nil, http.StatusBadRequest, "contract is the gateway's domain"
	}
	if len(titles) > maxOverlayTitles {
		return nil, http.StatusBadRequest, "titles names at most 100 tools"
	}
	for name, t := range titles {
		if strings.TrimSpace(t) == "" || utf8.RuneCountInString(t) > 200 || strings.ContainsAny(t, "\r\n") {
			return nil, http.StatusBadRequest, "titles: " + name + "'s title is one line of at most 200 characters"
		}
	}
	return &contractOverlay{actions: parsed.Spec.Actions, titles: titles}, 0, ""
}

// contractDigest is the SHA-256 of the gateway contract's actions a request
// reads (the preview's overlay, else the stored one), in a normal form: by
// name, so the order tools were listed in doesn't change it. "" when the
// account has no gateway contract yet. A preview answers it, and a draft
// that carries it is taken only while it still holds.
func (h *MandatesHandler) contractDigest(r *http.Request, tenantID string) string {
	actions, _, err := h.actionsFor(r, tenantID, gatewayDomain)
	if err != nil || len(actions) == 0 {
		return ""
	}
	sorted := slices.Clone(actions)
	slices.SortFunc(sorted, func(a, b domain.ActionSpec) int { return cmp.Compare(a.Name, b.Name) })
	b, err := json.Marshal(sorted)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
