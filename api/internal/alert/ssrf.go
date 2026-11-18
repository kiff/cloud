package alert

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"time"
)

// SSRF guard for outbound alert webhooks.
//
// The webhook URL is tenant-supplied and POSTed from the cloud's
// network, so without a guard a tenant could point it at an internal
// address — cloud metadata (169.254.169.254), RFC 1918, loopback —
// and have the cloud make the request on their behalf. Two layers:
//
//   - Registration: ValidateTargetURL rejects a URL whose host is, or
//     currently resolves to, a blocked address. Fast UX feedback.
//   - Delivery (the hard guard): the HTTP client dials through a
//     net.Dialer whose Control hook inspects the *resolved* IP right
//     before connect and refuses blocked ranges. This runs on the
//     actual dialed address, so it defeats DNS rebinding — a host
//     that resolved to a public IP at registration but flips to an
//     internal IP at delivery is still blocked.
//
// allowPrivate is the dev escape hatch (env-gated in main) so local
// and integration testing against localhost still works. It is false
// in production.

// ErrBlockedAddress is returned when a target URL is, or resolves to,
// an address in a blocked range.
var ErrBlockedAddress = errors.New("alert: webhook address is not allowed (private, loopback, link-local, or metadata range)")

// isBlockedIP reports whether ip is in a range an outbound webhook
// must never reach. Covers loopback, RFC 1918 + IPv6 unique-local
// (net.IP.IsPrivate), link-local unicast (incl. 169.254.169.254) and
// multicast, the unspecified address, interface-local multicast, and
// the 0.0.0.0/8 "this network" block.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 0 {
		// 0.0.0.0/8 — "this network"; not covered by the helpers above
		// except the single unspecified address.
		return true
	}
	return false
}

// ValidateTargetURL checks scheme + host and, unless allowPrivate,
// rejects a URL that is or resolves to a blocked address. A DNS
// lookup failure is NOT fatal here (transient resolver issues
// shouldn't block registration of an otherwise-valid host) — the
// delivery-time dial guard is the hard stop.
func ValidateTargetURL(ctx context.Context, raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url must include a host")
	}
	if allowPrivate {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return ErrBlockedAddress
		}
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		// Transient/unknown — let it register; the dial guard enforces.
		return nil
	}
	for _, a := range addrs {
		if isBlockedIP(a.IP) {
			return ErrBlockedAddress
		}
	}
	return nil
}

// guardedDialControl returns a net.Dialer Control hook that rejects a
// connection to a blocked address. address is the resolved "ip:port"
// the dialer is about to connect to, so the check runs on the real
// destination IP (post-DNS), closing the rebinding gap.
func guardedDialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("alert: bad dial address %q: %w", address, err)
		}
		ip := net.ParseIP(host)
		if ip == nil || isBlockedIP(ip) {
			return ErrBlockedAddress
		}
		return nil
	}
}
