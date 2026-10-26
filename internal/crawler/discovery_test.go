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

	for _, relays := range [][]string{
		{"wss://a.example.com", "wss://A.example.com/", "ws://insecure.example.com", "wss://localhost"},
		{"wss://a.example.com", "wss://b.example.com:443"},
		{"wss://b.example.com"},
		{"wss://c.example.com"},
	} {
		if err := store.SaveEvent(signedList(t, nostr.Generate(), nostr.Now(), relays...)); err != nil {
			t.Fatal(err)
		}
	}

	counts, err := countRelayMentions(context.Background(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"wss://a.example.com": 2, "wss://b.example.com": 2, "wss://c.example.com": 1}; !maps.Equal(counts, want) {
		t.Errorf("got %v, want %v", counts, want)
	}
}

func signedList(t *testing.T, sk nostr.SecretKey, createdAt nostr.Timestamp, relays ...string) nostr.Event {
	t.Helper()
	event := nostr.Event{CreatedAt: createdAt, Kind: nostr.KindRelayListMetadata}
	for _, url := range relays {
		event.Tags = append(event.Tags, nostr.Tag{"r", url})
	}
	if err := event.Sign(sk); err != nil {
		t.Fatal(err)
	}
	return event
}
