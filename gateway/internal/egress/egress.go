// Package egress is the gateway's only way out to connected tools. Tool
// URLs are submitted by account owners and are untrusted: without a
// guard, one could point the gateway at cloud metadata, the database, or
// the API on loopback, and have KIFF call it with KIFF's network access.
//
// Two layers, as apps/api's webhook guard:
//   - ValidateURL at connect time, for a clear error;
//   - the dialer's Control hook on every connection, on the resolved IP
//     actually dialed, which defeats DNS rebinding.
//
// Redirects and proxies are refused, so neither can route around the
// dial check.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrBlocked: the address is internal, reserved or metadata.
var ErrBlocked = errors.New("egress: tool address is not allowed (internal, loopback, link-local, metadata or reserved range)")

var blocked = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001:db8::/32",
		"fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// Blocked reports whether ip must never be dialed. IPv4-mapped IPv6
// addresses are checked as IPv4.
func Blocked(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	ip = ip.Unmap()
	for _, p := range blocked {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Policy is the egress policy. AllowPrivate is the local-development
// escape hatch (plain http and private addresses); false in production.
type Policy struct {
	AllowPrivate bool
}

// ValidateURL checks a submitted tool URL.
func (p Policy) ValidateURL(ctx context.Context, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("the tool URL is not a valid absolute URL")
	}
	if u.User != nil {
		return nil, errors.New("the tool URL must not contain a username or password; give the credential separately")
	}
	if u.Fragment != "" {
		return nil, errors.New("the tool URL must not contain a fragment")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && p.AllowPrivate:
	default:
		return nil, errors.New("the tool URL must use https")
	}
	if p.AllowPrivate {
		return u, nil
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if Blocked(ip) {
			return nil, ErrBlocked
		}
		return u, nil
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(lctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("the tool host %q could not be resolved", host)
	}
	for _, a := range addrs {
		if Blocked(a) {
			return nil, ErrBlocked
		}
	}
	return u, nil
}

func (p Policy) control(_, address string, _ syscall.RawConn) error {
	if p.AllowPrivate {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil || Blocked(ap.Addr()) {
		return ErrBlocked
	}
	return nil
}

// Client is the HTTP client for calls to tools.
func (p Policy) Client(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: p.control}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           d.DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          50,
			IdleConnTimeout:       90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("egress: tool servers may not redirect")
		},
	}
}
