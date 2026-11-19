package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// PricingLinks carries the operator-configured paid-activation
// URLs the pricing page renders as CTAs. Empty values are valid
// and cause the page to fall back to the founder-led mailto: CTAs;
// non-empty values are rendered verbatim as the href on the
// corresponding tier card.
//
// The fields are URLs, not opaque tokens — typically Stripe
// Payment Links, Lemon Squeezy checkouts, or any other hosted
// checkout that survives a `<a href="...">` round-trip. The cloud
// does not interpret them. KIFF never touches card data; whichever
// processor sits behind the link is the system of record for
// payment.
//
// The operator's paid-activation runbook has the
// procedure that turns a payment back into a paid plan
// activation.
type PricingLinks struct {
	// Offer is the app's links, shared with /tafs.
	Offer                  OfferLinks
	LaunchPilotCheckoutURL string
	TeamCheckoutURL        string
	BusinessIntakeURL      string
}

// Pricing serves the public pricing-lite page at /pricing.
//
// The page is the offer in credits: Solo and Company, committed volume, and
// what counts. There is no free plan and no trial. BusinessIntakeURL
// supplies the Company plan's booking link; otherwise it falls back to
// Gabriel's email. The legacy checkout URLs are not rendered.
func Pricing(links PricingLinks) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Pricing(pages.PricingView{
			Offer:                  links.Offer,
			LaunchPilotCheckoutURL: links.LaunchPilotCheckoutURL,
			TeamCheckoutURL:        links.TeamCheckoutURL,
			BusinessIntakeURL:      links.BusinessIntakeURL,
		}).Render(r.Context(), w)
	})
}
