package crawler

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

var errForbiddenAddress = errors.New("forbidden address")

var forbiddenPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func isForbidden(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, prefix := range forbiddenPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// newGuardedClient returns an HTTP client that can only reach public addresses. The
// check runs on the resolved IP right before connecting, which defeats DNS rebinding.
func newGuardedClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || isForbidden(ip) {
				return fmt.Errorf("%w: %s", errForbiddenAddress, host)
			}
			return nil
		},
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			// Every relay is a different host: pooling would only pile up idle connections.
			DisableKeepAlives: true,
		},
		// A redirect could point anywhere, including a port that was never allowed.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
