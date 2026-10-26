package crawler

import (
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// LMDB keys are limited to 511 bytes, and a real relay URL is far shorter.
const maxRelayURLLength = 255

// normalizeRelayURL returns the registry form of a relay URL, or an error when the
// crawler must never dial it. Port 443 is always allowed, other ports only when listed.
func normalizeRelayURL(raw string, allowedPorts []int) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Scheme != "wss" {
		return "", fmt.Errorf("scheme %q is not wss", u.Scheme)
	}
	if u.User != nil || u.Opaque != "" {
		return "", fmt.Errorf("unexpected userinfo or opaque part")
	}

	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	switch {
	case host == "":
		return "", fmt.Errorf("missing host")
	case host == "localhost", strings.HasSuffix(host, ".localhost"), strings.HasSuffix(host, ".onion"):
		return "", fmt.Errorf("host %s is not public", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if isForbidden(ip) {
			return "", fmt.Errorf("address %s is not public", ip)
		}
		host = ip.String()
		if ip.Is6() {
			host = "[" + host + "]"
		}
	}

	if port := u.Port(); port != "" && port != "443" {
		n, err := strconv.Atoi(port)
		if err != nil || !slices.Contains(allowedPorts, n) {
			return "", fmt.Errorf("port %s is not allowed", port)
		}
		host += ":" + port
	}

	normalized := "wss://" + host + strings.TrimRight(u.EscapedPath(), "/")
	if u.RawQuery != "" {
		normalized += "?" + u.RawQuery
	}
	if len(normalized) > maxRelayURLLength {
		return "", fmt.Errorf("url longer than %d bytes", maxRelayURLLength)
	}
	return normalized, nil
}
