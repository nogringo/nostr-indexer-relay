package crawler

import (
	"context"
	"maps"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"
)

func TestCountRelayMentions(t *testing.T) {
	store := &lmdb.LMDBBackend{Path: t.TempDir()}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	lists := []struct {
		createdAt nostr.Timestamp
		relays    []string
	}{
		{100, []string{"wss://a.example.com", "wss://A.example.com/", "ws://insecure.example.com", "wss://localhost"}},
		{90, []string{"wss://a.example.com", "wss://b.example.com:443"}},
		{90, []string{"wss://b.example.com"}},
		{80, []string{"wss://c.example.com"}},
	}
	for _, list := range lists {
		event := nostr.Event{CreatedAt: list.createdAt, Kind: nostr.KindRelayListMetadata}
		for _, url := range list.relays {
			event.Tags = append(event.Tags, nostr.Tag{"r", url})
		}
		if err := event.Sign(nostr.Generate()); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveEvent(event); err != nil {
			t.Fatal(err)
		}
	}

	want := map[string]int{"wss://a.example.com": 2, "wss://b.example.com": 2, "wss://c.example.com": 1}
	// A page of 2 splits the lists sharing created_at 90 across two pages.
	for _, pageSize := range []int{2, 3, scanPageSize} {
		counts, err := countRelayMentions(context.Background(), store, nil, pageSize)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(counts, want) {
			t.Errorf("page size %d: got %v, want %v", pageSize, counts, want)
		}
	}
}
