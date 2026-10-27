package registry

import (
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/PowerDNS/lmdb-go/lmdb"
)

func TestSetPopularityAddsAndResetsRelays(t *testing.T) {
	r := openTestRegistry(t, t.TempDir())

	if err := r.SetPopularity(map[string]int{"wss://a.example.com": 3, "wss://b.example.com": 1}, []string{"wss://seed.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetPopularity(map[string]int{"wss://a.example.com": 5}, []string{"wss://seed.example.com"}); err != nil {
		t.Fatal(err)
	}

	assertPopularity(t, r, "wss://a.example.com", 5)
	assertPopularity(t, r, "wss://b.example.com", 0)
	if seed, _ := r.Get("wss://seed.example.com"); !seed.Seed {
		t.Fatal("expected the seed to be flagged")
	}
}

func TestDueOrdersSeedsThenPopularityAndSkipsForgottenRelays(t *testing.T) {
	r := openTestRegistry(t, t.TempDir())
	counts := map[string]int{"wss://small.example.com": 1, "wss://big.example.com": 10, "wss://tested.example.com": 20}
	if err := r.SetPopularity(counts, []string{"wss://seed.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetPopularity(map[string]int{"wss://big.example.com": 10, "wss://tested.example.com": 20}, []string{"wss://seed.example.com"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := r.Update("wss://tested.example.com", func(relay *Relay) { relay.NextTestAt = now.Add(time.Hour) }); err != nil {
		t.Fatal(err)
	}

	due := r.Due(now, 10)
	var urls []string
	for _, relay := range due {
		urls = append(urls, relay.URL)
	}
	want := []string{"wss://seed.example.com", "wss://big.example.com"}
	if len(urls) != len(want) || urls[0] != want[0] || urls[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, urls)
	}
}

func TestDueForSyncOnlyReturnsSupportedRelays(t *testing.T) {
	r := openTestRegistry(t, t.TempDir())
	counts := map[string]int{"wss://supported.example.com": 1, "wss://unsupported.example.com": 5, "wss://later.example.com": 9, "wss://supported.example.com/vertex": 3}
	if err := r.SetPopularity(counts, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for url, update := range map[string]func(*Relay){
		"wss://supported.example.com":   func(relay *Relay) { relay.NIP77 = NIP77Supported },
		"wss://unsupported.example.com": func(relay *Relay) { relay.NIP77 = NIP77Unsupported },
		"wss://later.example.com":       func(relay *Relay) { relay.NIP77, relay.NextSyncAt = NIP77Supported, now.Add(time.Hour) },
		"wss://supported.example.com/vertex": func(relay *Relay) {
			relay.NIP77, relay.AliasOf = NIP77Supported, "wss://supported.example.com"
		},
	} {
		if err := r.Update(url, update); err != nil {
			t.Fatal(err)
		}
	}

	due := r.DueForSync(now, 10)
	if len(due) != 1 || due[0].URL != "wss://supported.example.com" {
		t.Fatalf("expected only the supported relay due now, got %+v", due)
	}
}

func TestUpdateAllKeepsOnlyReportedChanges(t *testing.T) {
	path := t.TempDir()
	r := openTestRegistry(t, path)
	if err := r.SetPopularity(map[string]int{"wss://a.example.com": 1, "wss://b.example.com": 2}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateAll(func(relay *Relay) bool {
		relay.Live = true
		return relay.URL == "wss://a.example.com"
	}); err != nil {
		t.Fatal(err)
	}
	if relays := r.All(); len(relays) != 2 {
		t.Fatalf("expected 2 relays, got %+v", relays)
	}
	r.Close()

	reopened := openTestRegistry(t, path)
	if a, _ := reopened.Get("wss://a.example.com"); !a.Live {
		t.Error("expected the reported change to be stored")
	}
	if b, _ := reopened.Get("wss://b.example.com"); b.Live {
		t.Error("expected an unreported change to be dropped")
	}
}

func TestRegistrySurvivesReopening(t *testing.T) {
	path := t.TempDir()
	r := openTestRegistry(t, path)
	if err := r.SetPopularity(map[string]int{"wss://a.example.com": 2}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Update("wss://a.example.com", func(relay *Relay) {
		relay.NIP77, relay.SyncInterval, relay.SyncBoundaries = NIP77Supported, time.Hour, []nostr.Timestamp{1700000000}
		relay.Lists, relay.ListsSource, relay.Indexer, relay.Unique = 42, ListsSynced, true, 3
	}); err != nil {
		t.Fatal(err)
	}
	r.Close()

	reopened := openTestRegistry(t, path)
	relay, ok := reopened.Get("wss://a.example.com")
	if !ok || relay.NIP77 != NIP77Supported || relay.Popularity != 2 || relay.SyncInterval != time.Hour || len(relay.SyncBoundaries) != 1 ||
		relay.Lists != 42 || relay.ListsSource != ListsSynced || !relay.Indexer || relay.Unique != 3 {
		t.Fatalf("expected the relay to survive reopening, got %+v", relay)
	}
}

func TestRelaysWithAPathAreRetestedOnceForAliases(t *testing.T) {
	path := t.TempDir()
	r := openTestRegistry(t, path)
	counts := map[string]int{"wss://a.example.com": 1, "wss://a.example.com/vertex": 1, "wss://a.example.com?x=1": 1, "wss://b.example.com/vertex": 1}
	if err := r.SetPopularity(counts, []string{"wss://seed.example.com/relay"}); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	for url, status := range map[string]NIP77Status{
		"wss://a.example.com":          NIP77Supported,
		"wss://a.example.com/vertex":   NIP77Supported,
		"wss://a.example.com?x=1":      NIP77Supported,
		"wss://seed.example.com/relay": NIP77Supported,
		"wss://b.example.com/vertex":   NIP77Unsupported,
	} {
		if err := r.Update(url, func(relay *Relay) { relay.NIP77, relay.NextTestAt = status, later }); err != nil {
			t.Fatal(err)
		}
	}
	// As if the registry predated aliases.
	if err := r.env.Update(func(txn *lmdb.Txn) error { return txn.Del(r.settings, []byte(aliasesRetestedKey), nil) }); err != nil {
		t.Fatal(err)
	}
	r.Close()

	reopened := openTestRegistry(t, path)
	var due []string
	for _, relay := range reopened.Due(time.Now(), 10) {
		due = append(due, relay.URL)
	}
	if want := []string{"wss://a.example.com/vertex", "wss://a.example.com?x=1"}; !slices.Equal(slices.Sorted(slices.Values(due)), want) {
		t.Fatalf("expected the supported relays with a path or a query to be due, got %v", due)
	}
	if err := reopened.Update("wss://a.example.com/vertex", func(relay *Relay) { relay.NextTestAt = later }); err != nil {
		t.Fatal(err)
	}
	reopened.Close()

	if relay, _ := openTestRegistry(t, path).Get("wss://a.example.com/vertex"); relay.NextTestAt.Before(time.Now()) {
		t.Error("expected the retest to happen only once")
	}
}

func openTestRegistry(t *testing.T, path string) *Registry {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func assertPopularity(t *testing.T, r *Registry, url string, want int) {
	t.Helper()
	relay, ok := r.Get(url)
	if !ok || relay.Popularity != want {
		t.Fatalf("expected %s to have popularity %d, got %+v", url, want, relay)
	}
}
