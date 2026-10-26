package crawler

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"fiatjaf.com/nostr"

	"nostr-indexer-relay/internal/registry"
)

const (
	liveCheckInterval = time.Minute
	liveCap           = 50
	// A non-indexer that brings no list the indexers miss for this long loses its live subscription.
	liveQuietPeriod = 7 * 24 * time.Hour
	// A new list often reaches several relays at once, so an indexer has this long to deliver
	// it too before it counts as unique to the non-indexer that delivered it first.
	uniqueGrace = time.Minute
)

type liveSubscription struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// followRelays keeps a live subscription to every indexer and to the non-indexers that bring
// lists the indexers miss, until ctx is done.
func (c *Crawler) followRelays(ctx context.Context) {
	if err := c.registry.UpdateAll(func(r *registry.Relay) bool {
		wasLive := r.Live
		r.Live = false
		return wasLive
	}); err != nil {
		c.log.Printf("failed to reset the live subscriptions: %v", err)
	}

	subscriptions := make(map[string]liveSubscription)
	stop := func(url string) {
		subscriptions[url].cancel()
		<-subscriptions[url].done
		delete(subscriptions, url)
	}
	defer func() {
		for url := range subscriptions {
			stop(url)
		}
	}()

	for {
		now := time.Now()
		c.classify()
		c.credit(now)
		want := liveRelays(c.registry.All(), now, liveCap)

		for url, subscription := range subscriptions {
			select {
			case <-subscription.done:
				delete(subscriptions, url)
				continue
			default:
			}
			if !want[url] {
				stop(url)
			}
		}
		for url := range want {
			if _, ok := subscriptions[url]; ok {
				continue
			}
			subscriptionCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				c.follow(subscriptionCtx, url)
			}()
			subscriptions[url] = liveSubscription{cancel, done}
		}

		if !sleep(ctx, liveCheckInterval) {
			return
		}
	}
}

// liveRelays returns the relays to follow live: every indexer, and the n non-indexers that
// brought the most lists the indexers missed, as long as their last one is recent.
func liveRelays(relays []registry.Relay, now time.Time, n int) map[string]bool {
	want := make(map[string]bool)
	var contributors []registry.Relay
	for _, relay := range relays {
		switch {
		case relay.NIP77 != registry.NIP77Supported || relay.Forgotten():
		case relay.Indexer:
			want[relay.URL] = true
		case relay.Unique > 0 && now.Sub(relay.UniqueAt) < liveQuietPeriod:
			contributors = append(contributors, relay)
		}
	}
	slices.SortFunc(contributors, func(a, b registry.Relay) int {
		return cmp.Or(cmp.Compare(b.Unique, a.Unique), strings.Compare(a.URL, b.URL))
	})
	for _, relay := range contributors[:min(n, len(contributors))] {
		want[relay.URL] = true
	}
	return want
}

// follow keeps a live subscription to url until ctx is done or the connection drops. The
// next negentropy sync fills the gap left by a drop.
func (c *Crawler) follow(ctx context.Context, url string) {
	conn := nostr.NewRelay(ctx, url, nostr.RelayOptions{
		NoticeHandler: func(_ *nostr.Relay, notice string) { c.log.Printf("notice from %s: %s", url, notice) },
		// receive verifies every event, so that signatures are checked once.
		AssumeValid: true,
	})
	if err := conn.ConnectWithClient(ctx, c.client); err != nil {
		if ctx.Err() == nil {
			c.log.Printf("live subscription to %s could not connect, handing it back to the tester: %v", url, err)
			now := time.Now()
			c.update(url, func(r *registry.Relay) { handBack(r, err, now) })
		}
		return
	}
	defer conn.Close()

	sub, err := conn.Subscribe(ctx, nostr.Filter{Kinds: relayListsFilter.Kinds, Since: nostr.Now()}, nostr.SubscriptionOptions{Label: "live"})
	if err != nil {
		c.log.Printf("live subscription to %s failed: %v", url, err)
		return
	}
	c.update(url, func(r *registry.Relay) { r.Live = true })
	defer c.update(url, func(r *registry.Relay) { r.Live = false })
	c.log.Printf("following %s live", url)

	for event := range sub.Events {
		c.receive(ctx, url, event)
	}
	if ctx.Err() == nil {
		c.log.Printf("live subscription to %s ended: %v", url, context.Cause(sub.Context))
	}
}

// credit adds to each non-indexer the lists that no indexer delivered within uniqueGrace.
func (c *Crawler) credit(now time.Time) {
	for url, unique := range c.contributions.settle(now) {
		c.update(url, func(r *registry.Relay) { r.Unique, r.UniqueAt = r.Unique+unique, now })
	}
}

func (c *Crawler) update(url string, update func(*registry.Relay)) {
	if err := c.registry.Update(url, update); err != nil {
		c.log.Printf("failed to update %s: %v", url, err)
	}
}
