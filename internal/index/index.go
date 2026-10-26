// Package index keeps the created_at and id of every stored relay list in memory. Negentropy
// needs nothing else, whereas reading the lists from LMDB decodes each of them: about 1.5 KB
// allocated per list, gigabytes for every session on a full index.
package index

import (
	"bytes"
	"iter"
	"slices"
	"sync"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/nip77/negentropy"
	"github.com/google/btree"

	"nostr-indexer-relay/internal/scan"
)

var relayLists = []nostr.Kind{nostr.KindRelayListMetadata}

// Store is an event store that updates the index with every write. It holds relay lists only.
type Store struct {
	eventstore.Store

	// Held across each write and the index update that follows, so that the index matches LMDB.
	write sync.Mutex
	mu    sync.RWMutex
	items *btree.BTreeG[negentropy.Item]
}

// New indexes the lists already in store, with one full read.
func New(store eventstore.Store) *Store {
	s := &Store{Store: store, items: btree.NewG(64, less)}
	for event := range scan.Events(store, nostr.Filter{Kinds: relayLists}, scan.PageSize) {
		s.items.ReplaceOrInsert(item(event))
	}
	return s
}

func item(event nostr.Event) negentropy.Item {
	return negentropy.Item{Timestamp: event.CreatedAt, ID: event.ID}
}

// less is the negentropy order: by created_at, then by id.
func less(a, b negentropy.Item) bool {
	if a.Timestamp != b.Timestamp {
		return a.Timestamp < b.Timestamp
	}
	return bytes.Compare(a.ID[:], b.ID[:]) < 0
}

// Stale reports whether the author of event already has a list stored that is at least as new.
func (s *Store) Stale(event nostr.Event) bool {
	for current := range s.Store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{event.Kind}, Authors: []nostr.PubKey{event.PubKey}}, 1) {
		return !nostr.IsOlder(current, event)
	}
	return false
}

// ReplaceEvent reports a stale list as eventstore.ErrDupEvent. LMDB would drop it without an
// error, and khatru would then broadcast it as if it were stored.
func (s *Store) ReplaceEvent(event nostr.Event) ([]nostr.Event, error) {
	s.write.Lock()
	defer s.write.Unlock()
	if s.Stale(event) {
		return nil, eventstore.ErrDupEvent
	}
	deleted, err := s.Store.ReplaceEvent(event)
	if err != nil {
		return deleted, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range deleted {
		s.items.Delete(item(old))
	}
	s.items.ReplaceOrInsert(item(event))
	return deleted, nil
}

func (s *Store) SaveEvent(event nostr.Event) error {
	s.write.Lock()
	defer s.write.Unlock()
	if err := s.Store.SaveEvent(event); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.items.ReplaceOrInsert(item(event))
	return nil
}

func (s *Store) DeleteEvent(id nostr.ID) error {
	s.write.Lock()
	defer s.write.Unlock()
	// The index is ordered by created_at, which only the stored event knows.
	stored := slices.Collect(s.Store.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1))
	if err := s.Store.DeleteEvent(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range stored {
		s.items.Delete(item(event))
	}
	return nil
}

// CountEvents answers from the index when filter selects lists by date only.
func (s *Store) CountEvents(filter nostr.Filter) (uint32, error) {
	if !covers(filter) {
		return s.Store.CountEvents(filter)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if filter.Since == 0 && filter.Until == 0 {
		return uint32(s.items.Len()), nil
	}
	var count uint32
	s.ascend(filter.Since, filter.Until, func(negentropy.Item) bool {
		count++
		return true
	})
	return count, nil
}

// Len returns the number of stored lists.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.items.Len()
}

// Vector returns the lists created between since and until, both inclusive, for a negentropy
// session. A zero bound leaves that side open.
func (s *Store) Vector(since, until nostr.Timestamp) *Vector {
	s.mu.RLock()
	defer s.mu.RUnlock()
	size := 0
	s.ascend(since, until, func(negentropy.Item) bool {
		size++
		return true
	})
	items := make([]negentropy.Item, 0, size)
	s.ascend(since, until, func(item negentropy.Item) bool {
		items = append(items, item)
		return true
	})
	return &Vector{items: items}
}

// NegentropyEvents yields the events filter matches for a negentropy session. When the index
// covers filter, only their id and created_at are set, which is all such a session reads.
func (s *Store) NegentropyEvents(filter nostr.Filter) iter.Seq[nostr.Event] {
	if !covers(filter) {
		return scan.Events(s.Store, filter, scan.PageSize)
	}
	return func(yield func(nostr.Event) bool) {
		for _, item := range s.Vector(filter.Since, filter.Until).items {
			if !yield(nostr.Event{ID: item.ID, CreatedAt: item.Timestamp}) {
				return
			}
		}
	}
}

func (s *Store) ascend(since, until nostr.Timestamp, yield func(negentropy.Item) bool) {
	s.items.AscendGreaterOrEqual(negentropy.Item{Timestamp: since}, func(item negentropy.Item) bool {
		return (until == 0 || item.Timestamp <= until) && yield(item)
	})
}

// covers reports whether filter selects relay lists by date alone, which the index answers.
func covers(filter nostr.Filter) bool {
	return slices.Equal(filter.Kinds, relayLists) && len(filter.IDs) == 0 && len(filter.Authors) == 0 &&
		len(filter.Tags) == 0 && filter.Search == ""
}
