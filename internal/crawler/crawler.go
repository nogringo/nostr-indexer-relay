package crawler

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"fiatjaf.com/nostr/eventstore"

	"nostr-indexer-relay/internal/registry"
)

const (
	discoveryInterval  = 15 * time.Minute
	testBatchSize      = 16
	idleWait           = time.Minute
	retestInterval     = 7 * 24 * time.Hour
	deadRetestInterval = 30 * 24 * time.Hour
	firstBackoff       = time.Hour
	deadAfterFailures  = 8
)

var defaultSeeds = []string{
	"wss://indexer.coracle.social",
	"wss://relay.ditto.pub",
	"wss://relay.nos.social",
	"wss://directory.yabu.me",
	"wss://profiles.nostr1.com",
}

type Crawler struct {
	store        eventstore.Store
	registry     *registry.Registry
	client       *http.Client
	seeds        []string
	allowedPorts []int
	log          *log.Logger
}

func New(store eventstore.Store, registry *registry.Registry, logger *log.Logger) *Crawler {
	return &Crawler{
		store:    store,
		registry: registry,
		client:   newGuardedClient(),
		seeds:    defaultSeeds,
		log:      logger,
	}
}

// Run discovers relays from the stored lists and tests their NIP-77 support until ctx is done.
func (c *Crawler) Run(ctx context.Context) {
	c.discover(ctx)

	var wg sync.WaitGroup
	wg.Go(func() {
		for sleep(ctx, discoveryInterval) {
			c.discover(ctx)
		}
	})
	wg.Go(func() { c.testRelays(ctx) })
	wg.Wait()
}

func (c *Crawler) discover(ctx context.Context) {
	counts, err := countRelayMentions(ctx, c.store, c.allowedPorts, scanPageSize)
	if err != nil {
		return
	}
	if err := c.registry.SetPopularity(counts, c.seeds); err != nil {
		c.log.Printf("failed to store relay popularity: %v", err)
		return
	}
	c.log.Printf("discovery: %d relays mentioned in stored lists", len(counts))
}

func (c *Crawler) testRelays(ctx context.Context) {
	for ctx.Err() == nil {
		due := c.registry.Due(time.Now(), testBatchSize)
		if len(due) == 0 {
			sleep(ctx, idleWait)
			continue
		}

		var wg sync.WaitGroup
		for _, relay := range due {
			wg.Go(func() {
				status, detail := probeNIP77(ctx, c.client, relay.URL)
				if ctx.Err() != nil {
					return
				}
				if status == registry.NIP77Supported {
					c.log.Printf("%s supports NIP-77", relay.URL)
				}
				if err := c.registry.Update(relay.URL, func(r *registry.Relay) { recordTest(r, status, detail, time.Now()) }); err != nil {
					c.log.Printf("failed to record the test of %s: %v", relay.URL, err)
				}
			})
		}
		wg.Wait()
	}
}

func recordTest(relay *registry.Relay, status registry.NIP77Status, detail string, now time.Time) {
	relay.NIP77, relay.Detail, relay.TestedAt = status, detail, now
	if status == registry.NIP77Unreachable {
		relay.Failures++
	} else {
		relay.Failures = 0
	}

	switch {
	case status == registry.NIP77Unreachable && relay.Failures >= deadAfterFailures:
		relay.NextTestAt = now.Add(deadRetestInterval)
	case status == registry.NIP77Unreachable:
		relay.NextTestAt = now.Add(firstBackoff << (relay.Failures - 1))
	case status == registry.NIP77Forbidden:
		// DNS can move a name to a public address.
		relay.NextTestAt = now.Add(deadRetestInterval)
	default:
		relay.NextTestAt = now.Add(retestInterval)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
