package crawler

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"nostr-indexer-relay/internal/registry"
)

func TestIndexFilled(t *testing.T) {
	visited := registry.Relay{Seed: true, NIP77: registry.NIP77Supported, SyncedAt: time.Now()}
	unvisited := registry.Relay{Seed: true, NIP77: registry.NIP77Supported}
	dead := registry.Relay{Seed: true, NIP77: registry.NIP77Unreachable}
	other := registry.Relay{Popularity: 1, NIP77: registry.NIP77Supported}

	tests := []struct {
		name   string
		relays []registry.Relay
		want   bool
	}{
		{"every reachable seed visited", []registry.Relay{visited, dead, other}, true},
		{"a seed still to visit", []registry.Relay{visited, unvisited}, false},
		{"no seed to visit", []registry.Relay{dead, other}, false},
	}
	for _, test := range tests {
		if got := indexFilled(test.relays); got != test.want {
			t.Errorf("%s: got %v, want %v", test.name, got, test.want)
		}
	}
}

func TestIsIndexer(t *testing.T) {
	measured := func(seed bool, lists int, source registry.ListsSource) registry.Relay {
		return registry.Relay{Seed: seed, Lists: lists, ListsSource: source}
	}
	tests := []struct {
		name   string
		relay  registry.Relay
		ours   int
		filled bool
		want   bool
	}{
		{"unmeasured seed", measured(true, 0, registry.ListsUnmeasured), 100, true, true},
		{"small seed before the index is filled", measured(true, 10, registry.ListsSynced), 100, false, true},
		{"small seed", measured(true, 49, registry.ListsSynced), 100, true, false},
		{"big relay before the index is filled", measured(false, 90, registry.ListsSynced), 100, false, false},
		{"big relay counted", measured(false, 50, registry.ListsCounted), 100, true, true},
		{"empty relay and empty index", measured(false, 0, registry.ListsSynced), 0, true, false},
		{"alias of a big relay", registry.Relay{Lists: 90, ListsSource: registry.ListsCounted, AliasOf: "wss://big.example.com"}, 100, true, false},
	}
	for _, test := range tests {
		if got := isIndexer(test.relay, test.ours, test.filled, 0.5); got != test.want {
			t.Errorf("%s: got %v, want %v", test.name, got, test.want)
		}
	}
}

func TestLiveRelays(t *testing.T) {
	now := time.Now()
	recent := now.Add(-time.Hour)
	supported := func(url string, indexer bool, unique int, uniqueAt time.Time) registry.Relay {
		return registry.Relay{URL: url, Popularity: 1, NIP77: registry.NIP77Supported, Indexer: indexer, Unique: unique, UniqueAt: uniqueAt}
	}
	relays := []registry.Relay{
		supported("wss://indexer.example.com", true, 0, time.Time{}),
		{URL: "wss://untested-seed.example.com", Seed: true, Indexer: true},
		{URL: "wss://forgotten.example.com", NIP77: registry.NIP77Supported, Indexer: true},
		supported("wss://big.example.com", false, 30, recent),
		supported("wss://middle.example.com", false, 10, recent),
		supported("wss://small.example.com", false, 2, recent),
		supported("wss://quiet.example.com", false, 100, now.Add(-7*24*time.Hour)),
		supported("wss://nothing.example.com", false, 0, time.Time{}),
		{URL: "wss://banned.example.com", Popularity: 1, NIP77: registry.NIP77Supported, Indexer: true, Banned: true},
		{URL: "wss://forced.example.com", NIP77: registry.NIP77Supported, ForceLive: true},
		{URL: "wss://indexer.example.com/vertex", Popularity: 1, NIP77: registry.NIP77Supported, Indexer: true, ForceLive: true, AliasOf: "wss://indexer.example.com"},
	}

	settings := registry.Settings{LiveCap: 2, LiveQuietPeriod: 7 * 24 * time.Hour}
	got := slices.Sorted(maps.Keys(liveRelays(relays, now, settings)))
	want := []string{"wss://big.example.com", "wss://forced.example.com", "wss://indexer.example.com", "wss://middle.example.com"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReceiveCreditsListsThatNoIndexerDelivered(t *testing.T) {
	c := newTestCrawler(t)
	ctx := context.Background()
	const indexerURL, otherURL = "wss://indexer.example.com", "wss://other.example.com"
	if err := c.registry.SetPopularity(map[string]int{indexerURL: 1, otherURL: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.registry.Update(indexerURL, func(r *registry.Relay) { r.Indexer = true }); err != nil {
		t.Fatal(err)
	}

	c.receive(ctx, otherURL, signedList(t, nostr.Generate(), 2000, "wss://relay.example.com"))
	c.filled.Store(true)
	shared := signedList(t, nostr.Generate(), 2000, "wss://relay.example.com")
	c.receive(ctx, otherURL, shared)
	c.receive(ctx, indexerURL, shared)
	c.receive(ctx, otherURL, signedList(t, nostr.Generate(), 2000, "wss://relay.example.com"))

	now := time.Now()
	c.credit(now)
	if other, _ := c.registry.Get(otherURL); other.Unique != 0 {
		t.Fatalf("expected no credit before an indexer had the time to deliver, got %d", other.Unique)
	}
	c.credit(now.Add(uniqueGrace))
	if other, _ := c.registry.Get(otherURL); other.Unique != 1 || !other.UniqueAt.Equal(now.Add(uniqueGrace)) {
		t.Fatalf("expected one unique list, the one no indexer delivered once the index is filled, got %+v", other)
	}
}

func TestFollowStoresLiveLists(t *testing.T) {
	remote := newTestCrawler(t)
	local := newTestCrawler(t)
	server := httptest.NewServer(remote.relay)
	defer server.Close()
	url := wsURL(server)
	if err := local.registry.SetPopularity(map[string]int{url: 1}, nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		local.follow(ctx, url)
	}()
	waitFor(t, "the subscription to open", func() bool {
		relay, _ := local.registry.Get(url)
		return relay.Live
	})

	// Signed after the subscription opened, so that it is not older than its since.
	list := signedList(t, nostr.Generate(), nostr.Now(), "wss://relay.example.com")
	waitFor(t, "the live list", func() bool {
		// The REQ may reach the remote after a broadcast, so the list is broadcast until it arrives.
		remote.relay.BroadcastEvent(list)
		for range local.store.QueryEvents(nostr.Filter{IDs: []nostr.ID{list.ID}}, 1) {
			return true
		}
		return false
	})

	cancel()
	<-done
	if relay, _ := local.registry.Get(url); relay.Live {
		t.Error("expected the live flag to be cleared once the subscription ends")
	}
}

func TestFollowHandsAnUnreachableRelayBackToTheTester(t *testing.T) {
	c := newTestCrawler(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	url := wsURL(closed)
	closed.Close()
	if err := c.registry.SetPopularity(map[string]int{url: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.registry.Update(url, func(r *registry.Relay) { r.NIP77 = registry.NIP77Supported }); err != nil {
		t.Fatal(err)
	}

	c.follow(context.Background(), url)
	if relay, _ := c.registry.Get(url); relay.NIP77 != registry.NIP77Unreachable || relay.Failures != 1 || relay.Live {
		t.Fatalf("expected the relay to go back to the tester, got %+v", relay)
	}
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
