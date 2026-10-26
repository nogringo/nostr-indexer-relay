package crawler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIsForbidden(t *testing.T) {
	forbidden := []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "100.64.0.1", "169.254.169.254", "0.0.0.0", "224.0.0.1", "255.255.255.255", "::1", "::", "fd00::1", "fe80::1", "::ffff:192.168.0.1"}
	for _, raw := range forbidden {
		if !isForbidden(netip.MustParseAddr(raw)) {
			t.Errorf("expected %s to be forbidden", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700::1111"} {
		if isForbidden(netip.MustParseAddr(raw)) {
			t.Errorf("expected %s to be allowed", raw)
		}
	}
}

func TestGuardedClientRefusesLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	_, err := newGuardedClient().Get(server.URL)
	if !errors.Is(err, errForbiddenAddress) {
		t.Fatalf("expected a forbidden address error, got %v", err)
	}
}
