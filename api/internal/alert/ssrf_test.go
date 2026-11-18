package alert_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kiff/cloud/api/internal/alert"
)

// ValidateTargetURL rejects IP-literal hosts in blocked ranges and
// accepts a public IP literal; allowPrivate bypasses the check.
// IP literals are used so the test needs no DNS.
func TestValidateTargetURL_BlocksPrivateRanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	blocked := []string{
		"http://169.254.169.254/latest/meta-data/", // cloud metadata (link-local)
		"http://127.0.0.1/hook",                    // loopback
		"https://10.0.0.5/hook",                    // RFC 1918
		"http://192.168.1.1/hook",                  // RFC 1918
		"http://172.16.0.1/hook",                   // RFC 1918
		"http://[::1]/hook",                        // IPv6 loopback
		"http://0.0.0.0/hook",                      // unspecified / this-network
	}
	for _, raw := range blocked {
		if err := alert.ValidateTargetURL(ctx, raw, false); !errors.Is(err, alert.ErrBlockedAddress) {
			t.Errorf("%s: got %v, want ErrBlockedAddress", raw, err)
		}
		// allowPrivate bypasses the range check.
		if err := alert.ValidateTargetURL(ctx, raw, true); err != nil {
			t.Errorf("%s with allowPrivate: got %v, want nil", raw, err)
		}
	}

	// Public IP literal passes (no DNS needed).
	if err := alert.ValidateTargetURL(ctx, "https://8.8.8.8/hook", false); err != nil {
		t.Errorf("public IP: got %v, want nil", err)
	}
}

func TestValidateTargetURL_SchemeAndHost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, raw := range []string{
		"ftp://example.com/x",
		"file:///etc/passwd",
		"https://",
	} {
		if err := alert.ValidateTargetURL(ctx, raw, false); err == nil {
			t.Errorf("%s: expected an error", raw)
		}
	}
}

// The delivery dial guard refuses a connection to a blocked address,
// closing the DNS-rebinding gap (the check is at connect time on the
// resolved IP). A loopback literal is used so no real network is hit.
func TestHTTPSink_DialGuardBlocksLoopback(t *testing.T) {
	t.Parallel()
	sink := alert.NewHTTPSink(false)
	err := sink.Send(context.Background(), alert.Target{URL: "http://127.0.0.1:9/hook"}, []byte(`{}`))
	if err == nil {
		t.Fatal("expected delivery to a loopback address to be blocked")
	}
	if !errors.Is(err, alert.ErrBlockedAddress) {
		t.Fatalf("got %v, want it to wrap ErrBlockedAddress", err)
	}
}
