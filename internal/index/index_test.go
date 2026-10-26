package index

import (
	"errors"
	"slices"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/eventstore/lmdb"
	"fiatjaf.com/nostr/nip77/negentropy"
	"fiatjaf.com/nostr/nip77/negentropy/storage/vector"
)

func TestIndexFollowsTheStore(t *testing.T) {
	backend := newBackend(t)
	sk := nostr.Generate()
	if err := backend.SaveEvent(list(t, sk, 1000)); err != nil {
		t.Fatal(err)
	}
	store := New(backend)
	if store.Len() != 1 {
		t.Fatalf("expected the stored list to be indexed, got %d", store.Len())
	}

	newer := list(t, sk, 2000)
	if _, err := store.ReplaceEvent(newer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceEvent(list(t, sk, 1500)); !errors.Is(err, eventstore.ErrDupEvent) {
		t.Errorf("an older list must be reported as a duplicate, got %v", err)
	}
	if _, err := store.ReplaceEvent(newer); !errors.Is(err, eventstore.ErrDupEvent) {
		t.Errorf("a known list must be reported as a duplicate, got %v", err)
	}
	expiring := list(t, nostr.Generate(), 3000)
	if _, err := store.ReplaceEvent(expiring); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceEvent(list(t, nostr.Generate(), 4000)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteEvent(expiring.ID); err != nil {
		t.Fatal(err)
	}

	var stored []negentropy.Item
	for event := range backend.QueryEvents(nostr.Filter{Kinds: relayLists}, 100) {
		stored = append(stored, item(event))
	}
	slices.SortFunc(stored, func(a, b negentropy.Item) int {
		if less(a, b) {
			return -1
		}
		return 1
	})
	if indexed := store.Vector(0, 0).items; !slices.Equal(indexed, stored) {
		t.Errorf("index %v does not match the store %v", indexed, stored)
	}
}

func TestVectorAndCountSelectByDate(t *testing.T) {
	store := New(newBackend(t))
	for _, at := range []nostr.Timestamp{1000, 2000, 2000, 3000} {
		if _, err := store.ReplaceEvent(list(t, nostr.Generate(), at)); err != nil {
			t.Fatal(err)
		}
	}

	for _, window := range []struct {
		since, until nostr.Timestamp
		want         int
	}{{0, 0, 4}, {2000, 0, 3}, {0, 2000, 3}, {2000, 2000, 2}, {2001, 2999, 0}} {
		filter := nostr.Filter{Kinds: relayLists, Since: window.since, Until: window.until}
		count, err := store.CountEvents(filter)
		if err != nil {
			t.Fatal(err)
		}
		lmdbCount, _ := store.Store.CountEvents(filter)
		vec := store.Vector(window.since, window.until)
		if int(count) != window.want || int(lmdbCount) != window.want || vec.Size() != window.want {
			t.Errorf("[%d, %d]: want %d lists, counted %d, LMDB counted %d, vector holds %d",
				window.since, window.until, window.want, count, lmdbCount, vec.Size())
		}
	}

	pubkey := nostr.Generate().Public()
	if count, _ := store.CountEvents(nostr.Filter{Kinds: relayLists, Authors: []nostr.PubKey{pubkey}}); count != 0 {
		t.Errorf("a filter on authors must go to LMDB, got %d", count)
	}
}

func TestVectorFingerprintsLikeTheLibraryVector(t *testing.T) {
	store := New(newBackend(t))
	reference := vector.New()
	for i := range 50 {
		event := list(t, nostr.Generate(), nostr.Timestamp(1000+i%7))
		if _, err := store.ReplaceEvent(event); err != nil {
			t.Fatal(err)
		}
		reference.Insert(event.CreatedAt, event.ID)
	}
	reference.Seal()
	ours := store.Vector(0, 0)

	if ours.Fingerprint(3, 41) != reference.Fingerprint(3, 41) {
		t.Error("fingerprints differ")
	}
	for i := range 51 {
		bound := reference.GetBound(i)
		if got, want := ours.FindLowerBound(0, 50, bound), reference.FindLowerBound(0, 50, bound); got != want {
			t.Errorf("lower bound of item %d: got %d, want %d", i, got, want)
		}
		prefix := negentropy.Bound{Timestamp: bound.Timestamp, IDPrefix: bound.IDPrefix[:min(4, len(bound.IDPrefix))]}
		if got, want := ours.FindLowerBound(0, 50, prefix), reference.FindLowerBound(0, 50, prefix); got != want {
			t.Errorf("lower bound of the prefix of item %d: got %d, want %d", i, got, want)
		}
	}
}

func newBackend(t *testing.T) *lmdb.LMDBBackend {
	t.Helper()
	backend := &lmdb.LMDBBackend{Path: t.TempDir()}
	if err := backend.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	return backend
}

func list(t *testing.T, sk nostr.SecretKey, createdAt nostr.Timestamp) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: nostr.KindRelayListMetadata, CreatedAt: createdAt, Tags: nostr.Tags{{"r", "wss://relay.example.com"}}}
	if err := event.Sign(sk); err != nil {
		t.Fatal(err)
	}
	return event
}
