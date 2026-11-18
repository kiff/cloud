package billing

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Event is a minimal projection of a Stripe webhook event — only the
// fields the processor reads. We deliberately do not pull the
// stripe-go SDK: the endpoint needs a handful of fields, and a narrow
// hand-parsed shape keeps the dependency surface (and the trust
// surface) small. Unknown fields are ignored.
type Event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object EventObject `json:"object"`
	} `json:"data"`
}

// EventObject is the union of the data.object fields we read across
// the event types we handle (checkout.session.completed and the
// customer.subscription.* / invoice.* lifecycle events). Fields not
// present for a given event type stay zero.
type EventObject struct {
	// Common.
	ID       string `json:"id"`
	Customer string `json:"customer"`
	Status   string `json:"status"`

	// checkout.session.completed: the subscription created by the
	// session, the Payment Link metadata copied onto the session
	// (server-controlled — this is where kiff_plan lives), and the
	// buyer-entered custom fields (tenant_slug correlation).
	Subscription string            `json:"subscription"`
	Metadata     map[string]string `json:"metadata"`

	// A pack's one-time checkout (mode "payment"): its payment, paid
	// once payment_status is "paid". On charge.refunded, the charge's
	// payment and whether all of it was refunded.
	Mode          string `json:"mode"`
	PaymentStatus string `json:"payment_status"`
	PaymentIntent string `json:"payment_intent"`
	Refunded      bool   `json:"refunded"`
	Created       int64  `json:"created"`
	CustomFields  []struct {
		Key  string `json:"key"`
		Text struct {
			Value string `json:"value"`
		} `json:"text"`
	} `json:"custom_fields"`
}

// ParseEvent decodes a verified webhook payload into an Event.
// Call only after VerifySignature has passed.
func ParseEvent(payload []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(payload, &e); err != nil {
		return Event{}, fmt.Errorf("billing: decode event: %w", err)
	}
	if e.ID == "" || e.Type == "" {
		return Event{}, fmt.Errorf("billing: event missing id or type")
	}
	return e, nil
}

// CustomField returns the buyer-entered value for a custom field key
// (e.g. "tenant_slug"), trimmed. Empty when absent.
func (o EventObject) CustomField(key string) string {
	for _, f := range o.CustomFields {
		if f.Key == key {
			return strings.TrimSpace(f.Text.Value)
		}
	}
	return ""
}
