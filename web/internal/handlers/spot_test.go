package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/spots"
)

// TestSpotIndex asserts /spot lists every enablement flow with a link to its
// infographic, loads the stylesheet, and is noindex (a pitch artifact).
func TestSpotIndex(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/spot", nil)
	rec := httptest.NewRecorder()
	Spot(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Errorf("spot index missing noindex header")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "css/spot.css") {
		t.Errorf("spot index missing spot.css")
	}
	for _, s := range spots.All() {
		if !strings.Contains(body, `href="/spot/`+s.Slug+`"`) {
			t.Errorf("spot index missing card link for %q", s.Slug)
		}
	}
}

// TestSpotPage asserts a known slug renders the enablement infographic with
// the cross-the-line band, the decision loop, the action, and the framework.
func TestSpotPage(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/spot/close-deals", nil)
	req.SetPathValue("slug", "close-deals")
	rec := httptest.NewRecorder()
	Spot(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	mustContain := []string{
		`class="spot-cross"`,
		`class="spot-loop"`,
		"APPLY_DISCOUNT",
		"Agno",
		"now you ship",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("spot page missing %q", want)
		}
	}
}

// TestSpotUnknownSlug asserts an unknown spot 404s.
func TestSpotUnknownSlug(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/spot/nope", nil)
	req.SetPathValue("slug", "nope")
	rec := httptest.NewRecorder()
	Spot(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: got %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestSpotVoice enforces the cloud-copy rule on the enablement
// infographics too: property-led, not crypto-led.
func TestSpotVoice(t *testing.T) {
	t.Parallel()

	banned := []string{"Merkle", "Sepolia", "on-chain", "Base Sepolia"}
	for _, s := range spots.All() {
		req := httptest.NewRequest(http.MethodGet, "/spot/"+s.Slug, nil)
		req.SetPathValue("slug", s.Slug)
		rec := httptest.NewRecorder()
		Spot(nil).ServeHTTP(rec, req)
		body := rec.Body.String()
		for _, tok := range banned {
			if strings.Contains(body, tok) {
				t.Errorf("spot %q contains banned cloud-copy token %q", s.Slug, tok)
			}
		}
	}
}
