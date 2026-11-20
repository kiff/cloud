package region

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientAddr is the caller's address. Caddy, on the same box, is the only
// proxy: the last X-Forwarded-For entry is the one it appended, and it's
// trusted only when the connection itself comes from loopback. Only a loopback
// last hop is a call from the box (internal). A connection that isn't from
// loopback is taken by its own address, whatever it sends, and is never
// internal, even from a private address: a balancer put in front later is
// then refused loudly (no country) instead of quietly turning the gate off.
func ClientAddr(r *http.Request) (addr netip.Addr, internal bool) {
	peer := peerAddr(r.RemoteAddr)
	if !peer.IsValid() {
		return netip.Addr{}, false
	}
	if !peer.IsLoopback() {
		return peer, false
	}
	f := r.Header.Values("X-Forwarded-For")
	if len(f) == 0 {
		return peer, true
	}
	parts := strings.Split(f[len(f)-1], ",")
	last, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil {
		return netip.Addr{}, false
	}
	last = last.Unmap()
	return last, last.IsLoopback()
}

func peerAddr(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}
