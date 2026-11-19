package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/proofs"
	"github.com/kiff/cloud/web/internal/content/videos"
)

// TestProofIndex asserts /proof is the public Cards at work surface:
// verified Refund Card evidence first, illustrative cards kept honest,
// and the old proofs retained as a distinct domain-evidence group.
func TestProofIndex(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/proof", nil)
	rec := httptest.NewRecorder()
	Proof(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("X-Robots-Tag") != "" {
		t.Errorf("public Cards at work index must be indexable")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "css/proof.css") {
		t.Errorf("proof index missing proof.css")
	}
	for _, want := range []string{
		// The test run leads: video, counts, sequence. It is a synthetic
		// run on the production services with no customer data, and the
		// page must say so.
		"Run on the live service.", "Not a mockup.",
		"A synthetic test run on KIFF’s production services, with no customer data.", `id="verified-run"`,
		"22 Sep 2026", "7 signed receipts",
		"7 decisions, 7 signed receipts",
		"Owner raises the limit to €8,000", "Owner revokes the card",
		"/cards-at-work/demo-720p.mp4", "/cards-at-work/poster.jpg",
		// Honest about what was verified.
		"“Allowed” is not “executed”",
		// The former /videos gallery: now one link per clip, each
		// playing on its own /proof/videos page.
		`id="videos"`, `id="capsules"`, `href="/proof/videos/prod-guardian"`, `href="/proof/videos/govern"`,
		// The guarantees behind the run.
		`href="/invariants"`,
		// Domain proofs, labelled as not card evidence.
		`id="domain-evidence"`, "They are not card evidence.",
		// Default share metadata must survive the new per-video image override.
		`property="og:image" content="https://kiff.dev/static/img/og/kiff-og.png"`,
		`name="twitter:image" content="https://kiff.dev/static/img/og/kiff-og.png"`,
		`property="og:image:width" content="1200"`,
		`property="og:image:height" content="630"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("proof page missing %q", want)
		}
	}
	// The Refund Card clip plays once, at the top, not again in the grid.
	if n := strings.Count(body, "/cards-at-work/"); n != 3 {
		t.Errorf("cards-at-work assets appear %d times, want 3 (poster, source, download link)", n)
	}
	for _, p := range proofs.All() {
		if !strings.Contains(body, `href="/proof/`+p.Slug+`"`) {
			t.Errorf("proof index missing card link for %q", p.Slug)
		}
	}
	for _, clip := range append(videos.Demos(), videos.ProductTours()...) {
		if !strings.Contains(body, `href="`+clip.PagePath()+`"`) {
			t.Errorf("gallery clip %q is missing its own watch/share link", clip.Slug)
		}
	}
}

func TestProofVideoShareMetadata(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle("GET /proof/videos/{video}", ProofVideo(nil))
	for _, clip := range append(videos.Demos(), videos.ProductTours()...) {
		t.Run(clip.Slug, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, clip.PagePath(), nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			body := rec.Body.String()
			for _, want := range []string{
				`rel="canonical" href="` + clip.PageURL() + `"`,
				`property="og:url" content="` + clip.PageURL() + `"`,
				`property="og:image" content="` + clip.Poster() + `"`,
				`name="twitter:image" content="` + clip.Poster() + `"`,
				`href="/proof#videos"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("video page missing %q", want)
				}
			}
			for _, unwanted := range []string{`property="og:image:width"`, `property="og:image:height"`} {
				if strings.Contains(body, unwanted) {
					t.Errorf("poster must not inherit default dimensions: %s", unwanted)
				}
			}
		})
	}
	for _, slug := range []string{"unknown", "not-a-video"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proof/videos/"+slug, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("unknown video %q: status = %d, want 404", slug, rec.Code)
		}
	}
}

// TestProofPage asserts a known slug renders the infographic with its
// without/with contrast, the decision loop, and the framework chip.
func TestProofPage(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/proof/duplicate-payment", nil)
	req.SetPathValue("slug", "duplicate-payment")
	rec := httptest.NewRecorder()
	Proof(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Errorf("legacy domain-evidence detail must remain noindex")
	}
	body := rec.Body.String()
	mustContain := []string{
		`class="proof-contrast`,
		`class="proof-loop"`,
		"$100,000",
		"$10,000",
		"PAY_INVOICE",
		"OpenClaw",
		"Why it was refused",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("proof page missing %q", want)
		}
	}
}

// TestProofRegulationChip asserts a regulated proof surfaces its
// named regime (the FCA / FDCPA framing the deck promises).
func TestProofRegulationChip(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/proof/collections-promise", nil)
	req.SetPathValue("slug", "collections-promise")
	rec := httptest.NewRecorder()
	Proof(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "FDCPA / CONC") {
		t.Errorf("regulated proof missing its regime chip")
	}
}

// TestProofUnknownSlug asserts an unknown proof 404s.
func TestProofUnknownSlug(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/proof/nope", nil)
	req.SetPathValue("slug", "nope")
	rec := httptest.NewRecorder()
	Proof(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: got %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestProofPageVoice enforces the cloud-copy rule on the
// infographics too: property-led, not crypto-led.
func TestProofPageVoice(t *testing.T) {
	t.Parallel()

	banned := []string{"Merkle", "Sepolia", "on-chain", "Base Sepolia"}
	for _, p := range proofs.All() {
		req := httptest.NewRequest(http.MethodGet, "/proof/"+p.Slug, nil)
		req.SetPathValue("slug", p.Slug)
		rec := httptest.NewRecorder()
		Proof(nil).ServeHTTP(rec, req)
		body := rec.Body.String()
		for _, tok := range banned {
			if strings.Contains(body, tok) {
				t.Errorf("proof %q contains banned cloud-copy token %q", p.Slug, tok)
			}
		}
	}
}
