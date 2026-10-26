package scan

import (
	"slices"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"
)

func TestEventsYieldsEachEventOnceAcrossPages(t *testing.T) {
	store := &lmdb.LMDBBackend{Path: t.TempDir()}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var want []nostr.ID
	for _, createdAt := range []nostr.Timestamp{100, 90, 90, 80, 70, 70, 70} {
		event := nostr.Event{CreatedAt: createdAt, Kind: nostr.KindRelayListMetadata}
		if err := event.Sign(nostr.Generate()); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveEvent(event); err != nil {
			t.Fatal(err)
		}
		want = append(want, event.ID)
	}
	slices.SortFunc(want, compareIDs)

	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.KindRelayListMetadata}}
	// A page of 3 splits the events sharing created_at 90 and 70 across pages.
	for _, pageSize := range []int{3, 4, PageSize} {
		var got []nostr.ID
		for event := range Events(store, filter, pageSize) {
			got = append(got, event.ID)
		}
		slices.SortFunc(got, compareIDs)
		if !slices.Equal(got, want) {
			t.Errorf("page size %d: got %d events, want %d, each once", pageSize, len(got), len(want))
		}
	}

	var windowed int
	for range Events(store, nostr.Filter{Kinds: filter.Kinds, Since: 75, Until: 95}, 2) {
		windowed++
	}
	if windowed != 3 {
		t.Errorf("expected the 3 events between 75 and 95, got %d", windowed)
	}
}

func compareIDs(a, b nostr.ID) int {
	return slices.Compare(a[:], b[:])
}
