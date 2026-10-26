package registry

import (
	"testing"
	"time"

	"fiatjaf.com/nostr"
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
	counts := map[string]int{"wss://supported.example.com": 1, "wss://unsupported.example.com": 5, "wss://later.example.com": 9}
	if err := r.SetPopularity(counts, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for url, update := range map[string]func(*Relay){
		"wss://supported.example.com":   func(relay *Relay) { relay.NIP77 = NIP77Supported },
		"wss://unsupported.example.com": func(relay *Relay) { relay.NIP77 = NIP77Unsupported },
		"wss://later.example.com":       func(relay *Relay) { relay.NIP77, relay.NextSyncAt = NIP77Supported, now.Add(time.Hour) },
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
