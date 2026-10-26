package crawler

import (
	"strings"
	"testing"
)

func TestNormalizeRelayURL(t *testing.T) {
	accepted := map[string]string{
		"wss://relay.example.com":           "wss://relay.example.com",
		" WSS://Relay.Example.COM/ ":        "wss://relay.example.com",
		"wss://relay.example.com.":          "wss://relay.example.com",
		"wss://relay.example.com:443/":      "wss://relay.example.com",
		"wss://relay.example.com/inbox/":    "wss://relay.example.com/inbox",
		"wss://relay.example.com/?a=1#frag": "wss://relay.example.com?a=1",
		"wss://8.8.8.8":                     "wss://8.8.8.8",
		"wss://[2606:4700::1111]":           "wss://[2606:4700::1111]",
	}
	for raw, want := range accepted {
		if got, err := normalizeRelayURL(raw, nil); err != nil || got != want {
			t.Errorf("normalizeRelayURL(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}

	rejected := []string{
		"ws://relay.example.com",
		"https://relay.example.com",
		"relay.example.com",
		"wss://",
		"wss://user:pass@relay.example.com",
		"wss://localhost",
		"wss://relay.localhost",
		"wss://abcdef.onion",
		"wss://127.0.0.1",
		"wss://10.1.2.3",
		"wss://192.168.1.1",
		"wss://100.64.0.1",
		"wss://169.254.169.254",
		"wss://[::1]",
		"wss://[::ffff:10.0.0.1]",
		"wss://[fd00::1]",
		"wss://relay.example.com:7777",
		"wss://relay.example.com/" + strings.Repeat("a", maxRelayURLLength),
	}
	for _, raw := range rejected {
		if got, err := normalizeRelayURL(raw, nil); err == nil {
			t.Errorf("normalizeRelayURL(%q) = %q; want an error", raw, got)
		}
	}
}

func TestNormalizeRelayURLAllowsListedPorts(t *testing.T) {
	got, err := normalizeRelayURL("wss://relay.example.com:7777/", []int{7777})
	if err != nil || got != "wss://relay.example.com:7777" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDefaultSeedsAreNormalized(t *testing.T) {
	for _, seed := range defaultSeeds {
		if got, err := normalizeRelayURL(seed, nil); err != nil || got != seed {
			t.Errorf("seed %s normalizes to %q, %v", seed, got, err)
		}
	}
}
