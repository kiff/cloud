package egress_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/egress"
)

func TestBlocked(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.31.0.5", "192.168.1.1", "169.254.169.254",
		"100.100.1.1", "0.0.0.0", "::1", "fd00:ec2::254", "fe80::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b::a9fe:a9fe", "224.0.0.1", "255.255.255.255"} {
		if !egress.Blocked(netip.MustParseAddr(ip)) {
			t.Errorf("%s not blocked", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "52.94.236.248", "2606:4700:4700::1111"} {
		if egress.Blocked(netip.MustParseAddr(ip)) {
			t.Errorf("%s blocked", ip)
		}
	}
}

func TestValidateURL(t *testing.T) {
	p := egress.Policy{}
	ctx := context.Background()
	for raw, want := range map[string]string{
		"http://example.com/mcp":           "https",
		"https://user:pw@example.com/mcp":  "username",
		"https://169.254.169.254/latest":   "not allowed",
		"https://[::ffff:10.0.0.1]/mcp":    "not allowed",
		"https://localhost/mcp":            "not allowed",
		"ftp://example.com":                "https",
		"/relative":                        "absolute",
		"https://no-such-host.invalid/mcp": "could not be resolved",
	} {
		_, err := p.ValidateURL(ctx, raw)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
	if _, err := p.ValidateURL(ctx, "https://1.1.1.1/mcp"); err != nil {
		t.Errorf("public IP refused: %v", err)
	}
}

// The dial guard holds even when the URL was never validated (or a name
// now resolves elsewhere): a loopback server is unreachable.
func TestClientRefusesInternalAtDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	_, err := egress.Policy{}.Client(5 * time.Second).Get(srv.URL)
	if !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("loopback dial: %v", err)
	}
	if _, err := (egress.Policy{AllowPrivate: true}).Client(5 * time.Second).Get(srv.URL); err != nil {
		t.Fatalf("dev policy: %v", err)
	}
}

func TestClientRefusesRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer target.Close()
	srv := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer srv.Close()
	if _, err := (egress.Policy{AllowPrivate: true}).Client(5*time.Second).Post(srv.URL, "application/json", nil); err == nil ||
		!strings.Contains(err.Error(), "may not redirect") {
		t.Fatalf("redirect followed: %v", err)
	}
}
