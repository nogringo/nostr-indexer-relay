package crawler

import (
	"context"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip70"

	indexer "nostr-indexer-relay/internal/relay"
)

// ingest stores a fetched list when it is valid and newer than our copy, then pushes it to
// our live subscribers. It reports whether the list was stored.
func (c *Crawler) ingest(ctx context.Context, event nostr.Event) bool {
	if event.Kind != nostr.KindRelayListMetadata || nip70.IsProtected(event) || len(event.String()) > indexer.MaxMessageSize {
		return false
	}
	// VerifySignature trusts the announced id, so the id is recomputed first.
	if !event.CheckID() || !event.VerifySignature() {
		return false
	}
	for current := range c.store.QueryEvents(nostr.Filter{Kinds: relayListsFilter.Kinds, Authors: []nostr.PubKey{event.PubKey}}, 1) {
		if !nostr.IsOlder(current, event) {
			return false
		}
	}
	if _, err := c.relay.AddEvent(ctx, event); err != nil {
		return false
	}
	c.relay.BroadcastEvent(event)
	return true
}
