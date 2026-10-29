package crawler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr/khatru"

	"nostr-indexer-relay/internal/index"
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

	syncWorkers       = 4
	syncIdleWait      = 10 * time.Second
	minSyncInterval   = 10 * time.Minute
	firstSyncInterval = time.Hour
	maxSyncInterval   = 24 * time.Hour
)

type Crawler struct {
	store      *index.Store
	relay      *khatru.Relay
	registry   *registry.Registry
	client     *http.Client
	log        *log.Logger
	rediscover chan struct{}

	// Whether our index is filled enough for shares of it to mean something.
	filled        atomic.Bool
	contributions contributions
}

// New returns a crawler that stores what it fetches through relay, so that fetched lists
// take the same path as published ones.
func New(store *index.Store, relay *khatru.Relay, registry *registry.Registry, logger *log.Logger) *Crawler {
	return &Crawler{
		store:      store,
		relay:      relay,
		registry:   registry,
		client:     newGuardedClient(),
		log:        logger,
		rediscover: make(chan struct{}, 1),
	}
}

// Rediscover runs a discovery without waiting for the next one, so that new seeds and
// allowed ports apply right away.
func (c *Crawler) Rediscover() {
	select {
	case c.rediscover <- struct{}{}:
	default:
	}
}

// Run discovers relays from the stored lists, tests their NIP-77 support, syncs from the
// ones that pass and follows the indexers live, until ctx is done.
func (c *Crawler) Run(ctx context.Context) {
	c.discover(ctx)

	var wg sync.WaitGroup
	wg.Go(func() {
		timer := time.NewTimer(discoveryInterval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			case <-c.rediscover:
				timer.Stop()
			}
			c.discover(ctx)
			timer.Reset(discoveryInterval)
		}
	})
	wg.Go(func() { c.testRelays(ctx) })
	wg.Go(func() { c.syncRelays(ctx) })
	wg.Go(func() { c.followRelays(ctx) })
	wg.Wait()
}

func (c *Crawler) discover(ctx context.Context) {
	settings := c.registry.Settings()
	counts, err := countRelayMentions(ctx, c.store, settings.AllowedPorts)
	if err != nil {
		return
	}
	if err := c.registry.SetPopularity(counts, settings.Seeds); err != nil {
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
				result := probeNIP77(ctx, c.client, relay.URL, relay.ListsSource != registry.ListsSynced)
				if ctx.Err() != nil {
					return
				}
				if result.status == registry.NIP77Supported {
					c.log.Printf("%s supports NIP-77", relay.URL)
				}
				now := time.Now()
				if err := c.registry.Update(relay.URL, func(r *registry.Relay) {
					recordTest(r, result.status, result.detail, now)
					if result.counted && r.ListsSource != registry.ListsSynced {
						r.Lists, r.ListsSource, r.ListsAt = result.lists, registry.ListsCounted, now
					}
				}); err != nil {
					c.log.Printf("failed to record the test of %s: %v", relay.URL, err)
				}
			})
		}
		wg.Wait()
	}
}

func (c *Crawler) syncRelays(ctx context.Context) {
	var mu sync.Mutex
	inFlight := make(map[string]bool)
	next := func() (registry.Relay, bool) {
		mu.Lock()
		defer mu.Unlock()
		for _, relay := range c.registry.DueForSync(time.Now(), syncWorkers) {
			if !inFlight[relay.URL] {
				inFlight[relay.URL] = true
				return relay, true
			}
		}
		return registry.Relay{}, false
	}

	var wg sync.WaitGroup
	for range syncWorkers {
		wg.Go(func() {
			for ctx.Err() == nil {
				relay, ok := next()
				if !ok {
					sleep(ctx, syncIdleWait)
					continue
				}
				c.syncOne(ctx, relay)
				mu.Lock()
				delete(inFlight, relay.URL)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
}

func (c *Crawler) syncOne(ctx context.Context, relay registry.Relay) {
	result, err := c.syncRelay(ctx, relay.URL, relay.SyncBoundaries)
	if ctx.Err() != nil {
		// Shutting down: keep the windows learned so far, but leave the schedule alone.
		c.registry.Update(relay.URL, func(r *registry.Relay) { r.SyncBoundaries = result.boundaries })
		return
	}
	now := time.Now()

	update := func(r *registry.Relay) { recordSync(r, result, err, now) }
	var connect *connectError
	if errors.As(err, &connect) {
		c.log.Printf("sync of %s could not connect, handing it back to the tester: %v", relay.URL, err)
		update = func(r *registry.Relay) { handBack(r, err, now) }
	} else if err != nil {
		c.log.Printf("sync of %s failed after %d new lists: %v", relay.URL, result.stored, err)
	} else {
		c.log.Printf("synced %s: %d new lists out of %d fetched, %d windows, %d lists held", relay.URL, result.stored, result.fetched, len(result.boundaries)+1, result.lists)
	}
	if err := c.registry.Update(relay.URL, update); err != nil {
		c.log.Printf("failed to record the sync of %s: %v", relay.URL, err)
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

// handBack records a relay that could not be reached as a failed test, so that the tester's
// backoff decides when it is worth another try.
func handBack(relay *registry.Relay, err error, now time.Time) {
	status := registry.NIP77Unreachable
	if errors.Is(err, errForbiddenAddress) {
		status = registry.NIP77Forbidden
	}
	recordTest(relay, status, err.Error(), now)
}

func recordSync(relay *registry.Relay, result syncResult, err error, now time.Time) {
	relay.SyncedAt, relay.LastSyncNew, relay.SyncBoundaries = now, result.stored, result.boundaries
	relay.SyncDetail = ""
	if err != nil {
		relay.SyncDetail = err.Error()
	} else {
		relay.Lists, relay.ListsSource, relay.ListsAt = result.lists, registry.ListsSynced, now
	}
	relay.SyncInterval = nextSyncInterval(relay.SyncInterval, result.stored)
	relay.NextSyncAt = now.Add(relay.SyncInterval)
}

// nextSyncInterval halves the interval after a visit that brought new lists and doubles it otherwise.
func nextSyncInterval(current time.Duration, stored int) time.Duration {
	if current == 0 {
		current = firstSyncInterval
	}
	if stored > 0 {
		return max(minSyncInterval, current/2)
	}
	return min(maxSyncInterval, current*2)
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
