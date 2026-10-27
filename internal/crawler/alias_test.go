package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"nostr-indexer-relay/internal/registry"
	indexer "nostr-indexer-relay/internal/relay"
)

func TestAncestors(t *testing.T) {
	tests := []struct {
		url  string
		want []string
	}{
		{"wss://h.com", nil},
		{"wss://h.com/a", []string{"wss://h.com"}},
		{"wss://h.com/a/b", []string{"wss://h.com", "wss://h.com/a"}},
		{"wss://h.com/a/b?q=1", []string{"wss://h.com", "wss://h.com/a", "wss://h.com/a/b"}},
		{"wss://h.com?q=1", []string{"wss://h.com"}},
		{"wss://[2001:db8::1]:8443/a", []string{"wss://[2001:db8::1]:8443"}},
	}
	for _, test := range tests {
		if got := ancestors(test.url); !slices.Equal(got, test.want) {
			t.Errorf("%s: got %v, want %v", test.url, got, test.want)
		}
	}
}

func TestAliasOf(t *testing.T) {
	// Our relay, like many, serves the same lists on every path.
	serve := func(t *testing.T, config indexer.Config, createdAt ...nostr.Timestamp) string {
		c := newTestCrawler(t)
		for _, at := range createdAt {
			mustAdd(t, c, signedList(t, nostr.Generate(), at, "wss://relay.example.com"))
		}
		server := httptest.NewServer(indexer.New(config, c.store))
		t.Cleanup(server.Close)
		return wsURL(server)
	}
	recent := nostr.Now() - 60
	old := nostr.Timestamp(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	config := indexer.Config{MaxLimit: 100}

	one := serve(t, config, recent, old, old)
	other := serve(t, config, recent, old, old)
	empty := serve(t, config)
	// Like strfry beyond a million lists, it refuses a session on all of them.
	refusing := serve(t, indexer.Config{MaxLimit: 100, NegentropyLimit: 2}, recent, old, old)
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	tests := []struct {
		name       string
		url        string
		candidates []string
		want       string
	}{
		{"same relay on another path", one + "/vertex", []string{one}, one},
		{"the shortest ancestor wins", one + "/a/b", []string{one, one + "/a"}, one},
		{"other lists", one + "/vertex", []string{other}, ""},
		{"no list on either side", empty + "/vertex", []string{empty}, ""},
		{"compared on recent lists when all are refused", refusing + "/vertex", []string{refusing}, refusing},
		{"unreachable ancestor", one + "/vertex", []string{wsURL(gone)}, ""},
		{"no ancestor", one + "/vertex", nil, ""},
	}
	for _, test := range tests {
		if got := aliasOf(context.Background(), http.DefaultClient, test.url, test.candidates); got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestTrackedAncestors(t *testing.T) {
	c := newTestCrawler(t)
	if err := c.registry.SetPopularity(map[string]int{"wss://h.com/a": 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.registry.SetPopularity(map[string]int{"wss://h.com": 1, "wss://h.com/a/b": 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.registry.Update("wss://h.com", func(r *registry.Relay) { r.Banned = true }); err != nil {
		t.Fatal(err)
	}

	got := c.trackedAncestors("wss://h.com/a/b/c")
	if want := []string{"wss://h.com", "wss://h.com/a/b"}; !slices.Equal(got, want) {
		t.Errorf("expected the banned root and the tracked parent, not the forgotten one: got %v, want %v", got, want)
	}
}
