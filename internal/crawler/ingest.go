package crawler

import (
	"context"
	"sync"
	"time"

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
	// An older list, or one we have, is skipped as a duplicate.
	if duplicate, err := c.relay.AddEvent(ctx, event); duplicate || err != nil {
		return false
	}
	c.relay.BroadcastEvent(event)
	return true
}

// receive ingests a list that url delivered and reports whether it was stored. A non-indexer
// that delivers a list first is credited with it, unless an indexer delivers it too.
func (c *Crawler) receive(ctx context.Context, url string, event nostr.Event) bool {
	stored := c.ingest(ctx, event)
	if relay, _ := c.registry.Get(url); relay.Indexer {
		c.contributions.cancel(event.ID)
	} else if stored && c.filled.Load() {
		c.contributions.add(url, event.ID, time.Now())
	}
	return stored
}

// contributions holds the lists that non-indexers delivered first, until uniqueGrace has
// passed without an indexer delivering them too.
type contributions struct {
	mu      sync.Mutex
	pending map[nostr.ID]contribution
}

type contribution struct {
	relay string
	at    time.Time
}

func (p *contributions) add(relay string, id nostr.ID, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil {
		p.pending = make(map[nostr.ID]contribution)
	}
	p.pending[id] = contribution{relay, at}
}

func (p *contributions) cancel(id nostr.ID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.pending, id)
}

// settle returns, per relay, the lists that no indexer delivered within uniqueGrace.
func (p *contributions) settle(now time.Time) map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	settled := make(map[string]int)
	for id, pending := range p.pending {
		if now.Sub(pending.at) >= uniqueGrace {
			settled[pending.relay]++
			delete(p.pending, id)
		}
	}
	return settled
}
