package apiclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// a review: a downloaded receipt is whole or an error: never a body
// cut short, over the bound, or not JSON, presented as the signed
// receipt.
func TestGetReceiptRawBearer(t *testing.T) {
	t.Parallel()
	const signed = `{"trace_id":"settled-p","schema_version":"v0.1"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/receipts/whole":
			_, _ = w.Write([]byte(signed))
		case "/v1/receipts/short":
			// Declares more than it sends, then drops the connection.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("no hijacker")
				return
			}
			conn, buf, _ := hj.Hijack()
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 500\r\n\r\n" + signed[:20])
			_ = buf.Flush()
			_ = conn.Close()
		case "/v1/receipts/huge":
			_, _ = w.Write([]byte(`{"pad":"` + strings.Repeat("a", apiclient.ReceiptDownloadMax) + `"}`))
		case "/v1/receipts/notjson":
			_, _ = w.Write([]byte(`{"trace_id":`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := apiclient.NewSessionAwareWithTransport(srv.URL, 0, srv.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := c.GetReceiptRawBearer(ctx, "b", "whole"); err != nil || string(got) != signed {
		t.Errorf("whole receipt: %q %v", got, err)
	}
	for _, trace := range []string{"short", "huge", "notjson"} {
		if got, err := c.GetReceiptRawBearer(ctx, "b", trace); !errors.Is(err, apiclient.ErrReceiptIncomplete) || got != nil {
			t.Errorf("%s: got %d bytes, err %v; want ErrReceiptIncomplete and nothing", trace, len(got), err)
		}
	}
	if _, err := c.GetReceiptRawBearer(ctx, "b", "missing"); !errors.Is(err, apiclient.ErrReceiptNotFound) {
		t.Errorf("missing: %v", err)
	}
}
