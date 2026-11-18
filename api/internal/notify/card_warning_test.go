package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/api/internal/words"
)

func TestCardWarningUsesStableProviderIdempotency(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(200)
	}))
	defer server.Close()
	n, err := NewResendNotifier(ResendConfig{APIKey: "synthetic", From: "KIFF <test@example.invalid>", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = n.CardBudgetWarning(context.Background(), "person@example.invalid", "cw_test", "Synthetic warning.", "https://app.kiff.dev/cards/test", words.EN); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("provider keys %v", keys)
	}
}
