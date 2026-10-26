package crawler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip77"
	"fiatjaf.com/nostr/nip77/negentropy"
	"fiatjaf.com/nostr/nip77/negentropy/storage/vector"

	"nostr-indexer-relay/internal/scan"
)

const (
	negentropyFrameSize = 60_000
	// directory.yabu.me took 35 s to open a window of about a million lists.
	roundTimeout     = 2 * time.Minute
	fetchBatchSize   = 500
	fetchTimeout     = 30 * time.Second
	syncSubscription = "sync"
)

// No relay list predates 2020: the oldest window is never split below this.
var nostrEpoch = nostr.Timestamp(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix())

var errTooMany = errors.New("too many lists for one negentropy session")

type connectError struct{ err error }

func (e *connectError) Error() string { return e.err.Error() }
func (e *connectError) Unwrap() error { return e.err }

type relayMessage struct{ label, first, second string }

type syncResult struct {
	stored     int
	boundaries []nostr.Timestamp
}

// syncRelay downloads, one since/until window at a time, the lists the relay has and we lack.
// A window the relay refuses as too big is cut in two, and the cuts are returned for next time.
func (c *Crawler) syncRelay(ctx context.Context, url string, boundaries []nostr.Timestamp) (syncResult, error) {
	result := syncResult{boundaries: slices.Clone(boundaries)}

	incoming := make(chan relayMessage, 16)
	forward := func(message relayMessage) {
		// Never block the connection's read loop.
		select {
		case incoming <- message:
		default:
		}
	}
	conn := nostr.NewRelay(ctx, url, nostr.RelayOptions{
		CustomHandler: func(data string) {
			label, first, second := parseMessage([]byte(data))
			forward(relayMessage{label, first, second})
		},
		NoticeHandler: func(_ *nostr.Relay, notice string) { forward(relayMessage{label: "NOTICE", first: notice}) },
		// ingest verifies every event, so that signatures are checked once.
		AssumeValid: true,
	})
	if err := conn.ConnectWithClient(ctx, c.client); err != nil {
		return result, &connectError{err}
	}
	defer conn.Close()

	for i := 0; i <= len(result.boundaries); {
		since, until := window(result.boundaries, i)
		ids, err := c.reconcile(ctx, conn, incoming, since, until)
		if errors.Is(err, errTooMany) {
			cut, ok := split(since, until, nostr.Now())
			if !ok {
				return result, err
			}
			c.log.Printf("%s refuses window [%d, %d] as too big, cutting it at %d", url, since, until, cut)
			result.boundaries = slices.Insert(result.boundaries, i, cut)
			continue
		}
		if err != nil {
			return result, err
		}

		stored, err := c.fetch(ctx, conn, ids)
		result.stored += stored
		if err != nil {
			return result, err
		}
		i++
	}
	return result, nil
}

// window returns the i-th of the len(boundaries)+1 windows, inclusive on both ends.
// A zero since or until leaves that side open.
func window(boundaries []nostr.Timestamp, i int) (since, until nostr.Timestamp) {
	if i > 0 {
		since = boundaries[i-1]
	}
	if i < len(boundaries) {
		until = boundaries[i] - 1
	}
	return since, until
}

// split returns where to cut a window in two, or false when it is too narrow to cut.
func split(since, until, now nostr.Timestamp) (nostr.Timestamp, bool) {
	lo, hi := max(since, nostrEpoch), until
	if hi == 0 {
		hi = now
	}
	if hi-lo < 2 {
		return 0, false
	}
	return lo + (hi-lo)/2, true
}

// reconcile runs one negentropy session on a window and returns the ids the relay has and we lack.
func (c *Crawler) reconcile(ctx context.Context, conn *nostr.Relay, incoming <-chan relayMessage, since, until nostr.Timestamp) ([]nostr.ID, error) {
	filter := nostr.Filter{Kinds: relayListsFilter.Kinds, Since: since, Until: until}
	local := vector.New()
	for event := range scan.Events(c.store, filter, scan.PageSize) {
		local.Insert(event.CreatedAt, event.ID)
	}
	local.Seal()

	neg := negentropy.New(local, negentropyFrameSize, false, true)
	// Reconcile blocks on HaveNots once its buffer is full, so it is drained as it goes.
	done := make(chan struct{})
	defer close(done)
	collected := make(chan []nostr.ID, 1)
	go func() {
		var ids []nostr.ID
		for {
			select {
			case id, ok := <-neg.HaveNots:
				if !ok {
					collected <- ids
					return
				}
				ids = append(ids, id)
			case <-done:
				return
			}
		}
	}()

	for len(incoming) > 0 {
		<-incoming
	}
	open, err := nip77.OpenEnvelope{SubscriptionID: syncSubscription, Filter: filter, Message: neg.Start()}.MarshalJSON()
	if err != nil {
		return nil, err
	}
	if err := conn.WriteWithError(open); err != nil {
		return nil, err
	}
	defer func() {
		if close, err := (nip77.CloseEnvelope{SubscriptionID: syncSubscription}).MarshalJSON(); err == nil {
			conn.Write(close)
		}
	}()

	timer := time.NewTimer(roundTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, errors.New("no negentropy answer")
		case message := <-incoming:
			switch {
			case message.label == "NOTICE":
				return nil, fmt.Errorf("notice: %s", message.first)
			case message.first != syncSubscription:
				continue
			case message.label == "NEG-ERR", message.label == "NEG-ERROR":
				if strings.Contains(message.second, "too many") || strings.Contains(message.second, "too big") {
					return nil, errTooMany
				}
				return nil, fmt.Errorf("negentropy refused: %s", message.second)
			case message.label == "NEG-MSG":
				next, err := neg.Reconcile(message.second)
				if err != nil {
					return nil, err
				}
				if next == "" {
					return <-collected, nil
				}
				reply, err := nip77.MessageEnvelope{SubscriptionID: syncSubscription, Message: next}.MarshalJSON()
				if err != nil {
					return nil, err
				}
				if err := conn.WriteWithError(reply); err != nil {
					return nil, err
				}
				timer.Reset(roundTimeout)
			}
		}
	}
}

// fetch downloads the events by id and returns how many were stored. A relay may return fewer
// events than asked for (its max_limit), so missing ids are asked again until a pass brings none.
func (c *Crawler) fetch(ctx context.Context, conn *nostr.Relay, ids []nostr.ID) (int, error) {
	stored := 0
	for len(ids) > 0 {
		received := make(map[nostr.ID]bool)
		for batch := range slices.Chunk(ids, fetchBatchSize) {
			sub, err := conn.Subscribe(ctx, nostr.Filter{IDs: batch}, nostr.SubscriptionOptions{Label: "fetch", MaxWaitForEOSE: fetchTimeout})
			if err != nil {
				return stored, err
			}
		events:
			for {
				select {
				case event := <-sub.Events:
					received[event.ID] = true
					if c.ingest(ctx, event) {
						stored++
					}
				case <-sub.EndOfStoredEvents:
					break events
				case <-sub.ClosedReason:
					break events
				case <-ctx.Done():
					sub.Unsub()
					return stored, ctx.Err()
				}
			}
			sub.Unsub()
		}
		if len(received) == 0 {
			break
		}
		ids = slices.DeleteFunc(ids, func(id nostr.ID) bool { return received[id] })
	}
	return stored, nil
}
