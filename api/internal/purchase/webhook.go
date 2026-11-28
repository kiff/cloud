package purchase

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SignatureTolerance is how old a signed webhook may be.
const SignatureTolerance = 5 * time.Minute

// Webhook answers the issuer's live authorization requests (Stripe
// Issuing's issuing_authorization.request, answered in the response body).
// It verifies the issuer's signature and timestamp, refuses a replayed
// event, and declines on anything it can't read or check. It logs the case
// and the answer, never a card number.
type Webhook struct {
	Store  *Store
	Secret string
	Logger *slog.Logger
	Now    func() time.Time
}

type stripeEvent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Account string `json:"account"`
	Data    struct {
		Object struct {
			ID   string `json:"id"`
			Card struct {
				ID string `json:"id"`
			} `json:"card"`
			PendingRequest *struct {
				Amount   int64  `json:"amount"`
				Currency string `json:"currency"`
			} `json:"pending_request"`
			MerchantData struct {
				Name      string `json:"name"`
				NetworkID string `json:"network_id"`
				Country   string `json:"country"`
				Category  string `json:"category"`
			} `json:"merchant_data"`
			RequestHistory   []json.RawMessage `json:"request_history"`
			VerificationData struct {
				ThreeDSecure *struct {
					Result string `json:"result"`
				} `json:"three_d_secure"`
			} `json:"verification_data"`
		} `json:"object"`
	} `json:"data"`
}

func (h *Webhook) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	decline := func(why string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"approved": false})
		if h.Logger != nil {
			h.Logger.InfoContext(r.Context(), "purchase: authorization declined", slog.String("why", why))
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	if !ValidSignature(h.Secret, r.Header.Get("Stripe-Signature"), body, h.now()) {
		http.Error(w, "the signature doesn't hold", http.StatusBadRequest)
		return
	}
	var ev stripeEvent
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" {
		http.Error(w, "not an event", http.StatusBadRequest)
		return
	}
	if fresh, err := h.Store.FirstSeen(r.Context(), ev.ID); err != nil || !fresh {
		// A replayed event is never answered again, and any doubt declines.
		decline("replayed or unrecorded event")
		return
	}
	if ev.Type != "issuing_authorization.request" {
		w.WriteHeader(http.StatusOK)
		return
	}
	o := ev.Data.Object
	if o.PendingRequest == nil {
		decline("no pending request")
		return
	}
	verification := ""
	if o.VerificationData.ThreeDSecure != nil {
		verification = o.VerificationData.ThreeDSecure.Result
	}
	a := Auth{
		ID:                o.ID + "#" + strconv.Itoa(len(o.RequestHistory)),
		Card:              o.Card.ID,
		Account:           ev.Account,
		Amount:            o.PendingRequest.Amount,
		Currency:          strings.ToUpper(o.PendingRequest.Currency),
		MerchantName:      o.MerchantData.Name,
		MerchantNetworkID: o.MerchantData.NetworkID,
		MerchantCountry:   o.MerchantData.Country,
		Category:          o.MerchantData.Category,
		Incremental:       len(o.RequestHistory) > 0,
		Verification:      issuerVerification(verification),
	}
	ans := h.Store.Authorize(r.Context(), a)
	if h.Logger != nil {
		h.Logger.InfoContext(r.Context(), "purchase: authorization answered", slog.String("authorization", o.ID),
			slog.Bool("approved", ans.Approved), slog.String("reason", ans.Reason))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"approved": ans.Approved})
}

// issuerVerification maps the issuer's 3-D Secure result to the case's
// words: a challenge KIFF can't answer is required, and declines.
func issuerVerification(result string) string {
	switch result {
	case "authenticated", "attempt_acknowledged":
		return "authenticated"
	case "exempted":
		return "exempted"
	case "required", "failed":
		return "required"
	}
	return ""
}

// ValidSignature checks the issuer's signature header ("t=<unix>,v1=<hex>"):
// HMAC-SHA256 of "<t>.<body>" with the endpoint's secret, within
// SignatureTolerance of now.
func ValidSignature(secret, header string, body []byte, now time.Time) bool {
	if secret == "" || header == "" {
		return false
	}
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return false
	}
	if d := now.Sub(time.Unix(t, 0)); d > SignatureTolerance || d < -SignatureTolerance {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, s := range sigs {
		got, err := hex.DecodeString(s)
		if err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// FirstSeen records an issuer event's id: false when it was seen before.
func (s *Store) FirstSeen(ctx context.Context, eventID string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO purchase.events (event_id, at) VALUES ($1, $2) ON CONFLICT DO NOTHING`, eventID, s.now())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetAccount records a tenant's connected account at the issuer.
func (s *Store) SetAccount(ctx context.Context, tenant, account string) error {
	if tenant == "" || account == "" {
		return errors.New("purchase: a tenant and its account")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO purchase.accounts (tenant_id, account) VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO UPDATE SET account = EXCLUDED.account`, tenant, account)
	return err
}
