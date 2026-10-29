package crawler

import (
	"nostr-indexer-relay/internal/registry"
)

// classify flags the indexers against the current size of our index.
func (c *Crawler) classify() {
	ours, err := c.store.CountEvents(relayListsFilter)
	if err != nil {
		c.log.Printf("failed to count our lists: %v", err)
		return
	}
	relays := c.registry.All()
	filled := indexFilled(relays)
	c.filled.Store(filled)
	share := c.registry.Settings().IndexerShare

	if err := c.registry.UpdateAll(func(r *registry.Relay) bool {
		indexer := isIndexer(*r, int(ours), filled, share)
		if r.Indexer == indexer {
			return false
		}
		r.Indexer = indexer
		switch {
		case !indexer:
			c.log.Printf("%s is no longer an indexer: %d lists, %d on our side", r.URL, r.Lists, ours)
		case r.ListsSource == registry.ListsUnmeasured:
			c.log.Printf("%s is an indexer until measured", r.URL)
		default:
			c.log.Printf("%s is now an indexer: %d lists, %d on our side", r.URL, r.Lists, ours)
		}
		return true
	}); err != nil {
		c.log.Printf("failed to store the indexers: %v", err)
	}
}

// indexFilled reports whether every seed that supports NIP-77 has been visited.
func indexFilled(relays []registry.Relay) bool {
	visited := false
	for _, relay := range relays {
		if !relay.Seed || relay.NIP77 != registry.NIP77Supported {
			continue
		}
		if relay.SyncedAt.IsZero() {
			return false
		}
		visited = true
	}
	return visited
}

// isIndexer tells whether relay holds share of our lists. Until our index is filled, and
// until they are measured, the seeds are the indexers.
func isIndexer(relay registry.Relay, ours int, filled bool, share float64) bool {
	if !filled || relay.ListsSource == registry.ListsUnmeasured {
		return relay.Seed
	}
	return relay.Lists > 0 && float64(relay.Lists) >= share*float64(ours)
}
